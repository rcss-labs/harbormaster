package fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
)

const (
	fakeSyftVersion      = "1.57.0"
	fakeGrypeVersion     = "0.121.2"
	fakeScannerImage     = "harbormaster-scanner:fake"
	fakeTaskARNPrefix    = "arn:aws:ecs:us-east-1:000000000000:task/harbormaster/fake-"
	defaultDBBuiltAgo    = 6 * time.Hour
	staleDBBuiltAgo      = 200 * time.Hour
	fakeDBArchivePrefix  = "fake grype database built "
	fakeDBPathTimeFormat = "2006-01-02T15:04:05Z"
)

// Scanner is the fake scanner task and the fake scan bucket in one: it
// implements awsx.Launcher and awsx.Bucket with no network, no Syft and no
// Grype. Starting a task reads the job from the bucket and, after Delay,
// writes the manifest and one result per scanned image into the same bucket.
type Scanner struct {
	Estate *Estate
	// Delay is how long a started task stays RUNNING (0: it is done at once).
	Delay time.Duration
	// DBBuiltAgo is the age of the vulnerability DB the fake reports.
	DBBuiltAgo time.Duration

	mu          sync.Mutex
	objects     map[string][]byte
	writes      []string
	aborted     []string
	tasks       map[string]*fakeTask
	failPull    map[string]bool
	failTimeout map[string]bool
	stale       bool
}

type fakeTask struct {
	state awsx.TaskState
	timer *time.Timer
}

var (
	_ awsx.Launcher = (*Scanner)(nil)
	_ awsx.Bucket   = (*Scanner)(nil)
)

func NewScanner(e *Estate) *Scanner {
	return &Scanner{
		Estate:      e,
		DBBuiltAgo:  defaultDBBuiltAgo,
		objects:     map[string][]byte{},
		tasks:       map[string]*fakeTask{},
		failPull:    map[string]bool{},
		failTimeout: map[string]bool{},
	}
}

// FailPull makes every image of the repository fail with pull_denied.
func (s *Scanner) FailPull(repository string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPull[repository] = true
}

// FailTimeout makes every image of the repository fail with timeout.
func (s *Scanner) FailTimeout(repository string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failTimeout[repository] = true
}

// ForceStale makes the DB too old: manifests say db_stale and the upstream
// listing offers only an old build.
func (s *Scanner) ForceStale(stale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stale = stale
}

// Writes lists the keys written to the bucket, oldest first.
func (s *Scanner) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// AbortedUploads lists the keys of streamed uploads that were aborted.
func (s *Scanner) AbortedUploads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.aborted...)
}

// Objects is a copy of the bucket's contents.
func (s *Scanner) Objects() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.objects))
	for key, data := range s.objects {
		out[key] = append([]byte(nil), data...)
	}
	return out
}

// dbBuiltAt is when the fake DB was built, as of now.
func (s *Scanner) dbBuiltAt() time.Time {
	s.mu.Lock()
	age := s.DBBuiltAgo
	if s.stale {
		age = staleDBBuiltAgo
	}
	s.mu.Unlock()
	return s.Estate.now().Add(-age).UTC().Truncate(time.Second)
}

func (s *Scanner) upstreamArchive() (path string, data []byte) {
	built := s.dbBuiltAt()
	return fmt.Sprintf("vulnerability-db_v6_%s.tar.zst", built.Format(fakeDBPathTimeFormat)),
		[]byte(fakeDBArchivePrefix + built.Format(time.RFC3339))
}

// FetchLatest is Anchore's latest.json as the fake serves it.
func (s *Scanner) FetchLatest(context.Context) ([]byte, error) {
	path, archive := s.upstreamArchive()
	return json.Marshal(scan.UpstreamDB{
		Built: s.dbBuiltAt(), Path: path, Checksum: fmt.Sprintf("sha256:%x", sha256.Sum256(archive))})
}

// OpenArchive streams the archive that FetchLatest names.
func (s *Scanner) OpenArchive(_ context.Context, path string) (io.ReadCloser, error) {
	current, archive := s.upstreamArchive()
	if path != current {
		return nil, awsx.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(archive)), nil
}

// RunTask starts a fake task that processes the run's job.
func (s *Scanner) RunTask(_ context.Context, spec awsx.LaunchSpec) (string, error) {
	s.mu.Lock()
	arn := fmt.Sprintf("%s%d", fakeTaskARNPrefix, len(s.tasks)+1)
	task := &fakeTask{state: awsx.TaskState{LastStatus: "RUNNING", StartedAt: s.Estate.now()}}
	s.tasks[arn] = task
	delay := s.Delay
	s.mu.Unlock()

	if delay <= 0 {
		s.finish(arn, spec.RunID)
		return arn, nil
	}
	s.mu.Lock()
	task.timer = time.AfterFunc(delay, func() { s.finish(arn, spec.RunID) })
	s.mu.Unlock()
	return arn, nil
}

// finish writes the run's output and stops the task, unless it was stopped already.
func (s *Scanner) finish(arn string, runID int64) {
	s.mu.Lock()
	task := s.tasks[arn]
	if task == nil || task.state.LastStatus == "STOPPED" {
		s.mu.Unlock()
		return
	}
	jobData, haveJob := s.objects[scan.JobKey(runID)]
	s.mu.Unlock()

	var job scan.Job
	exitCode := int32(0)
	if !haveJob || json.Unmarshal(jobData, &job) != nil {
		exitCode = 1
	} else {
		s.writeRun(runID, job)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if task.state.LastStatus == "STOPPED" {
		return
	}
	task.state.LastStatus = "STOPPED"
	task.state.StopCode = "EssentialContainerExited"
	task.state.ExitCode = &exitCode
	task.state.StoppedAt = s.Estate.now()
}

// writeRun writes one result per scanned image, then the manifest.
func (s *Scanner) writeRun(runID int64, job scan.Job) {
	s.mu.Lock()
	stale := s.stale
	failPull, failTimeout := s.failPull, s.failTimeout
	s.mu.Unlock()

	now := s.Estate.now().UTC()
	manifest := scan.Manifest{
		RunID: runID, Kind: scan.ModeScan, Status: scan.ManifestSucceeded,
		ScannerImageDigest: s.Estate.ImageDigestFor(fakeScannerImage),
		SyftVersion:        fakeSyftVersion, GrypeVersion: fakeGrypeVersion,
		DBChecksum: job.DB.Checksum, DBBuiltAt: job.DB.BuiltAt,
	}
	if manifest.DBBuiltAt.IsZero() {
		manifest.DBBuiltAt = s.dbBuiltAt()
	}
	if stale {
		manifest.Status = scan.ManifestDBStale
	}

	for _, image := range job.Images {
		repository := scan.ParseImageRef(image.ImageRef).Repository
		outcome := scan.ImageOutcome{Digest: image.Digest, Status: scan.OutcomeScanned}
		switch {
		case stale:
			outcome.Status, outcome.FailureCode = scan.OutcomeSkipped, scan.CodeDBStale
		case failPull[repository]:
			outcome.Status, outcome.FailureCode, outcome.FailureDetail = scan.OutcomeFailed, scan.CodePullDenied, "fake: pull denied"
		case failTimeout[repository]:
			outcome.Status, outcome.FailureCode, outcome.FailureDetail = scan.OutcomeFailed, scan.CodeTimeout, "fake: image scan timed out"
		}
		manifest.Images = append(manifest.Images, outcome)
		if outcome.Status != scan.OutcomeScanned {
			continue
		}

		findings := s.FindingsFor(repository, image.Tag)
		packages := fakeCleanPackageCount
		if len(findings) > 0 {
			packages = fakeFindingsPackageCount
		}
		platform := image.Platform
		if platform == "" {
			platform = "linux/amd64"
		}
		s.putJSON(scan.ResultKey(runID, image.Digest), scan.Result{
			Digest: image.Digest, PlatformDigest: image.Digest, Platform: platform, Status: scan.StatusScanned,
			SyftVersion: fakeSyftVersion, SBOMGeneratedAt: now, Packages: packages, Findings: findings,
		})
	}
	s.putJSON(scan.ManifestKey(runID), manifest)
}

func (s *Scanner) putJSON(key string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // the values are built from plain structs in this file
	}
	_ = s.Put(context.Background(), key, data)
}

func (s *Scanner) DescribeTask(_ context.Context, taskARN string) (awsx.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskARN]
	if !ok {
		return awsx.TaskState{}, awsx.ErrTaskNotFound
	}
	return task.state, nil
}

func (s *Scanner) StopTask(_ context.Context, taskARN, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskARN]
	if !ok {
		return awsx.ErrTaskNotFound
	}
	if task.state.LastStatus == "STOPPED" {
		return nil
	}
	if task.timer != nil {
		task.timer.Stop()
	}
	task.state.LastStatus = "STOPPED"
	task.state.StopCode = "UserInitiated"
	task.state.StoppedReason = reason
	task.state.StoppedAt = s.Estate.now()
	return nil
}

func (s *Scanner) Put(_ context.Context, key string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), body...)
	s.writes = append(s.writes, key)
	return nil
}

func (s *Scanner) Get(_ context.Context, key string, maxBytes int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, awsx.ErrObjectNotFound
	}
	if int64(len(data)) > maxBytes {
		return nil, awsx.ErrObjectTooLarge
	}
	return append([]byte(nil), data...), nil
}

func (s *Scanner) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

// PutStream stores the object only when the stream ends cleanly and verify
// passes; otherwise it records an aborted upload and stores nothing.
func (s *Scanner) PutStream(ctx context.Context, key string, body io.Reader, verify func() error) error {
	data, err := io.ReadAll(body)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = verify()
	}
	if err != nil {
		s.mu.Lock()
		s.aborted = append(s.aborted, key)
		s.mu.Unlock()
		return err
	}
	return s.Put(ctx, key, data)
}
