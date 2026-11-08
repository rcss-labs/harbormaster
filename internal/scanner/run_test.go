package scanner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

const (
	testRunID    = int64(7)
	testAccount  = "111111111111"
	testHost     = testAccount + ".dkr.ecr.us-east-1.amazonaws.com"
	testRepoRef  = testHost + "/web"
	testPassword = "ecr-secret-password"
)

var (
	digestIndex  = "sha256:" + strings.Repeat("1", 64)
	digestSingle = "sha256:" + strings.Repeat("2", 64)
	digestAMD64  = "sha256:" + strings.Repeat("a", 64)
	digestARM64  = "sha256:" + strings.Repeat("b", 64)
	digestSBOM   = "sha256:" + strings.Repeat("c", 64)
	archiveBytes = []byte("grype database archive")
)

const (
	mediaIndex  = "application/vnd.oci.image.index.v1+json"
	mediaDocker = "application/vnd.docker.distribution.manifest.v2+json"
)

func testdata(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// memS3 is a bucket in a map that records what was read and written.
type memS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    []string
	puts    []string
}

func (m *memS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := aws.ToString(in.Key)
	m.gets = append(m.gets, key)
	data, ok := m.objects[key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (m *memS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := aws.ToString(in.Key)
	m.objects[key] = body
	m.puts = append(m.puts, key)
	return &s3.PutObjectOutput{}, nil
}

type call struct {
	name    string
	args    []string
	env     []string
	timeout time.Duration // time left on the context when the call started; 0 when it had no deadline
}

// fakeCmd stands in for Syft and Grype: it writes testdata where the real
// tools would write files.
type fakeCmd struct {
	t          *testing.T
	calls      []call
	grypeFiles []string // report to write per grype scan, in order
	grypeData  [][]byte // takes precedence over grypeFiles when set
	syftFn     func(c call) (CommandResult, error, bool)
	syftScans  int
	grypeScans int
	onSyftScan func()
}

func (f *fakeCmd) Run(ctx context.Context, name string, args []string, env []string, _ int64) (CommandResult, error) {
	c := call{name: name, args: args, env: env}
	if deadline, ok := ctx.Deadline(); ok {
		c.timeout = time.Until(deadline)
	}
	f.calls = append(f.calls, c)
	isSyft := strings.HasSuffix(name, "syft")
	switch {
	case len(args) > 0 && args[0] == "version":
		tool := "grype"
		if isSyft {
			tool = "syft"
		}
		return CommandResult{Stdout: []byte("Application:         " + tool + "\nVersion:             1.57.0\nBuildDate:           2026-01-01\n")}, nil
	case isSyft:
		f.syftScans++
		if f.onSyftScan != nil {
			f.onSyftScan()
		}
		if f.syftFn != nil {
			if res, err, handled := f.syftFn(c); handled {
				return res, err
			}
		}
		for _, arg := range args {
			if path, ok := strings.CutPrefix(arg, "syft-json="); ok {
				if err := os.WriteFile(path, testdata(f.t, "syft_minimal.json"), 0o600); err != nil {
					f.t.Fatal(err)
				}
			}
		}
	case len(args) > 1 && args[0] == "db" && args[1] == "import":
	default: // grype scan
		out := ""
		for i, arg := range args {
			if arg == "--file" {
				out = args[i+1]
			}
		}
		var data []byte
		if f.grypeScans < len(f.grypeData) {
			data = f.grypeData[f.grypeScans]
		} else {
			data = testdata(f.t, f.grypeFiles[f.grypeScans])
		}
		f.grypeScans++
		if err := os.WriteFile(out, data, 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	return CommandResult{}, nil
}

func (f *fakeCmd) scanCalls(tool string) []call {
	var out []call
	for _, c := range f.calls {
		if strings.HasSuffix(c.name, tool) && len(c.args) > 0 && c.args[0] != "version" && c.args[0] != "db" {
			out = append(out, c)
		}
	}
	return out
}

type fakeECR struct {
	manifests map[string]struct {
		media string
		body  []byte
	}
	sizes       map[string]int64
	describeErr error
	tokenErr    error
	batchErr    map[string]error
	batchFail   map[string]string // digest -> BatchGetImage failure code, returned with no error
	tokenCalls  int
	batchCalls  []string
}

func (f *fakeECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	f.tokenCalls++
	if f.tokenErr != nil {
		return nil, f.tokenErr
	}
	token := base64.StdEncoding.EncodeToString([]byte("AWS:" + testPassword))
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []ecrtypes.AuthorizationData{
		{AuthorizationToken: aws.String(token), ProxyEndpoint: aws.String("https://" + testHost)}}}, nil
}

func (f *fakeECR) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	digest := aws.ToString(in.ImageIds[0].ImageDigest)
	size, ok := f.sizes[digest]
	if !ok {
		size = 50 << 20
	}
	return &ecr.DescribeImagesOutput{ImageDetails: []ecrtypes.ImageDetail{{ImageSizeInBytes: aws.Int64(size)}}}, nil
}

func (f *fakeECR) BatchGetImage(_ context.Context, in *ecr.BatchGetImageInput, _ ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error) {
	digest := aws.ToString(in.ImageIds[0].ImageDigest)
	f.batchCalls = append(f.batchCalls, digest)
	if err := f.batchErr[digest]; err != nil {
		return nil, err
	}
	if code := f.batchFail[digest]; code != "" {
		return &ecr.BatchGetImageOutput{Failures: []ecrtypes.ImageFailure{{FailureCode: ecrtypes.ImageFailureCode(code)}}}, nil
	}
	m, ok := f.manifests[digest]
	if !ok {
		return nil, errors.New("image not found")
	}
	return &ecr.BatchGetImageOutput{Images: []ecrtypes.Image{{ImageManifest: aws.String(string(m.body)), ImageManifestMediaType: aws.String(m.media)}}}, nil
}

type harness struct {
	t       *testing.T
	s3      *memS3
	cmd     *fakeCmd
	ecr     *fakeECR
	runner  *Runner
	job     scan.Job
	now     time.Time
	assumed []string
}

func image(digest, tag string) scan.JobImage {
	return scan.JobImage{Digest: digest, ImageRef: testRepoRef, Tag: tag, Platform: "linux/amd64", AccountID: testAccount, Region: "us-east-1"}
}

// newHarness builds a job with the images and a bucket holding the job file and the DB archive.
func newHarness(t *testing.T, images ...scan.JobImage) *harness {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	sum := sha256.Sum256(archiveBytes)
	h := &harness{t: t, now: now, s3: &memS3{objects: map[string][]byte{}}}
	h.job = scan.Job{
		RunID: testRunID, Kind: scan.ModeScan, Deadline: now.Add(30 * time.Minute),
		Limits: scan.JobLimits{ImageTimeoutSeconds: 600, TaskBudgetSeconds: 1800, MaxImageMB: 4096, MaxFindings: scan.MaxFindings},
		DB: scan.JobDB{ArchiveKey: "grype-db/v6/db.tar.zst", Checksum: fmt.Sprintf("sha256:%x", sum),
			BuiltAt: now.Add(-6 * time.Hour), MaxAgeHours: 120},
		Registries: map[string]scan.JobRegistry{testAccount: {RoleARN: "arn:aws:iam::" + testAccount + ":role/harbormaster-scan", ExternalID: "ext-1", Region: "us-east-1"}},
		Images:     images,
	}
	h.s3.objects["grype-db/v6/db.tar.zst"] = archiveBytes
	h.cmd = &fakeCmd{t: t, grypeFiles: []string{"grype_edge_nginx.json", "grype_empty.json"}}
	h.ecr = &fakeECR{
		manifests: map[string]struct {
			media string
			body  []byte
		}{
			digestIndex:  {mediaIndex, testdata(t, "manifest_index.json")},
			digestSingle: {mediaDocker, testdata(t, "manifest_image.json")},
		},
		sizes: map[string]int64{}, batchErr: map[string]error{}, batchFail: map[string]string{},
	}
	h.runner = &Runner{
		Opts: Options{Bucket: "bucket", WorkDir: t.TempDir(), SyftPath: "/bin/syft", GrypePath: "/bin/grype",
			RunID: testRunID, Mode: scan.ModeScan, Now: func() time.Time { return h.now }, Version: "test"},
		S3: h.s3, Cmd: h.cmd, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewECR: func(_ context.Context, role, externalID, region string) (ECRClient, error) {
			h.assumed = append(h.assumed, role+"|"+externalID+"|"+region)
			return h.ecr, nil
		},
	}
	return h
}

func (h *harness) putJob() {
	h.t.Helper()
	data, err := json.Marshal(h.job)
	if err != nil {
		h.t.Fatal(err)
	}
	h.s3.objects[scan.JobKey(testRunID)] = data
}

// run stores the job, runs the scanner and returns its exit code.
func (h *harness) run() int {
	h.t.Helper()
	h.putJob()
	return h.runner.Run(context.Background())
}

func (h *harness) manifest() scan.Manifest {
	h.t.Helper()
	data, ok := h.s3.objects[scan.ManifestKey(testRunID)]
	if !ok {
		h.t.Fatalf("no manifest written; puts: %v", h.s3.puts)
	}
	digests := make([]string, 0, len(h.job.Images))
	for _, img := range h.job.Images {
		digests = append(digests, img.Digest)
	}
	manifest, _, err := scan.ParseManifest(data, testRunID, digests)
	if err != nil {
		h.t.Fatalf("manifest rejected by scan.ParseManifest: %v", err)
	}
	return manifest
}

func (h *harness) outcome(digest string) scan.ImageOutcome {
	h.t.Helper()
	for _, o := range h.manifest().Images {
		if o.Digest == digest {
			return o
		}
	}
	h.t.Fatalf("manifest has no outcome for %s", digest)
	return scan.ImageOutcome{}
}

func (h *harness) result(digest string) scan.Result {
	h.t.Helper()
	data, ok := h.s3.objects[scan.ResultKey(testRunID, digest)]
	if !ok {
		h.t.Fatalf("no result object for %s; puts: %v", digest, h.s3.puts)
	}
	res, err := scan.ParseResult(data, digest)
	if err != nil {
		h.t.Fatalf("result rejected by scan.ParseResult: %v", err)
	}
	return res
}

func (h *harness) hasResult(digest string) bool {
	_, ok := h.s3.objects[scan.ResultKey(testRunID, digest)]
	return ok
}

func TestRun_ScanWritesResultAndManifest(t *testing.T) {
	h := newHarness(t, image(digestIndex, "edge"), image(digestSingle, "stable"))
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	manifest := h.manifest()
	if manifest.Status != scan.ManifestSucceeded || len(manifest.Images) != 2 {
		t.Fatalf("manifest = %+v, want succeeded with two images", manifest)
	}
	for _, o := range manifest.Images {
		if o.Status != scan.OutcomeScanned {
			t.Errorf("image %s outcome = %+v, want scanned", o.Digest, o)
		}
	}
	if manifest.SyftVersion != "1.57.0" || manifest.GrypeVersion != "1.57.0" {
		t.Errorf("versions = %q / %q, want parsed from the version commands", manifest.SyftVersion, manifest.GrypeVersion)
	}
	if want := h.job.DB.Checksum; manifest.DBChecksum != want {
		t.Errorf("db checksum = %q, want %q", manifest.DBChecksum, want)
	}

	first := h.result(digestIndex)
	if len(first.Findings) != 3 || first.Packages != 3 || first.PlatformDigest != digestAMD64 {
		t.Errorf("first result = %d findings, %d packages, platform digest %s; want 3, 3, %s",
			len(first.Findings), first.Packages, first.PlatformDigest, digestAMD64)
	}
	second := h.result(digestSingle)
	if len(second.Findings) != 0 || second.Packages != 3 || second.PlatformDigest != digestSingle {
		t.Errorf("second result = %d findings, %d packages, platform digest %s; want 0, 3, %s",
			len(second.Findings), second.Packages, second.PlatformDigest, digestSingle)
	}

	for _, key := range h.s3.puts {
		if !strings.HasPrefix(key, "results/") {
			t.Errorf("wrote %q outside results/", key)
		}
	}

	scans := h.cmd.scanCalls("syft")
	if len(scans) != 2 {
		t.Fatalf("syft scans = %d, want 2", len(scans))
	}
	if !contains(scans[0].args, "--platform=linux/amd64") {
		t.Errorf("syft args = %v, want --platform=linux/amd64 in one argument", scans[0].args)
	}
	if got := scans[0].args[0]; got != "registry:"+testRepoRef+"@"+digestAMD64 {
		t.Errorf("syft source = %q, want the amd64 platform digest", got)
	}
	if got := scans[1].args[0]; got != "registry:"+testRepoRef+"@"+digestSingle {
		t.Errorf("syft source = %q, want the single manifest digest", got)
	}
	env := strings.Join(scans[0].env, "\n")
	for _, want := range []string{"SYFT_REGISTRY_AUTH_AUTHORITY=" + testHost, "SYFT_REGISTRY_AUTH_USERNAME=AWS", "SYFT_REGISTRY_AUTH_PASSWORD=" + testPassword} {
		if !strings.Contains(env, want) {
			t.Errorf("syft env lacks %q", want)
		}
	}
	if !contains(scans[0].env, "SYFT_CHECK_FOR_APP_UPDATE=false") {
		t.Error("syft env lacks SYFT_CHECK_FOR_APP_UPDATE=false")
	}
	for _, kv := range scans[0].env {
		if strings.HasPrefix(kv, "AWS_") {
			t.Errorf("syft env leaks %q", strings.SplitN(kv, "=", 2)[0])
		}
	}
	for _, c := range h.cmd.calls {
		if strings.Contains(strings.Join(c.args, " "), testPassword) {
			t.Errorf("registry password on the command line of %s", c.name)
		}
	}
	if h.ecr.tokenCalls != 1 {
		t.Errorf("authorization token requests = %d, want 1 (cached per registry host)", h.ecr.tokenCalls)
	}
	if len(h.assumed) == 0 || h.assumed[0] != h.job.Registries[testAccount].RoleARN+"|ext-1|us-east-1" {
		t.Errorf("assumed = %v, want the job's role with the external id", h.assumed)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestRun_FallsBackWhenECRCannotServeTheDigest(t *testing.T) {
	for _, failure := range []string{"ImageNotFound", "ImageReferencedByManifestList"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t, image(digestIndex, "edge"))
			h.ecr.batchFail[digestIndex] = failure
			if code := h.run(); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			scans := h.cmd.scanCalls("syft")
			if len(scans) != 1 || scans[0].args[0] != "registry:"+testRepoRef+"@"+digestIndex || !contains(scans[0].args, "--platform=linux/amd64") {
				t.Fatalf("syft scans = %+v, want one scan anchored on the job digest with a platform", scans)
			}
			if got := h.result(digestIndex).PlatformDigest; got != digestSBOM {
				t.Errorf("platform digest = %s, want the SBOM's source manifestDigest %s", got, digestSBOM)
			}
		})
	}
}

func TestRun_FallbackNeverFiledUnderADifferentImage(t *testing.T) {
	otherSBOM := func(extra string) func(c call) (CommandResult, error, bool) {
		return func(c call) (CommandResult, error, bool) {
			doc := `{"artifacts":[{"id":"1"}],"source":{"target":{` + extra + `}}}`
			for _, arg := range c.args {
				if path, ok := strings.CutPrefix(arg, "syft-json="); ok {
					if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			return CommandResult{}, nil, true
		}
	}
	other := "sha256:" + strings.Repeat("f", 64)
	cases := map[string]string{
		"the scanned image is another digest": `"repoDigests":["` + testRepoRef + `@` + other + `"],"manifestDigest":"` + other + `"`,
		"no manifest digest in the SBOM":      `"repoDigests":["` + testRepoRef + `@` + digestIndex + `"]`,
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, image(digestIndex, "edge"))
			h.ecr.batchFail[digestIndex] = "ImageNotFound"
			h.cmd.syftFn = otherSBOM(target)
			if code := h.run(); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if o := h.outcome(digestIndex); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeTaskFailed {
				t.Errorf("outcome = %+v, want failed/task_failed", o)
			}
			if h.hasResult(digestIndex) {
				t.Error("no result may be written for an image the job did not name")
			}
		})
	}
}

func TestRun_UnknownBatchGetImageErrorDoesNotFallBack(t *testing.T) {
	h := newHarness(t, image(digestIndex, "edge"))
	h.ecr.batchErr[digestIndex] = errors.New("connection reset by peer")
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if o := h.outcome(digestIndex); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeTaskFailed {
		t.Errorf("outcome = %+v, want failed/task_failed", o)
	}
	if n := len(h.cmd.scanCalls("syft")); n != 0 {
		t.Errorf("syft ran %d times after an unknown ECR error", n)
	}
}

func TestRun_RefusesImagesOutsideTheJobsECRRegistry(t *testing.T) {
	foreign := image(digestSingle, "stable")
	foreign.ImageRef = "evil.example.com/web"
	wrongRegion := image(digestIndex, "edge")
	wrongRegion.ImageRef = testAccount + ".dkr.ecr.eu-west-1.amazonaws.com/web"
	h := newHarness(t, foreign, wrongRegion)
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, digest := range []string{digestSingle, digestIndex} {
		if o := h.outcome(digest); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeUnsupportedRegistry {
			t.Errorf("outcome for %s = %+v, want failed/unsupported_registry", digest, o)
		}
	}
	if h.ecr.tokenCalls != 0 || len(h.assumed) != 0 {
		t.Errorf("tokens=%d assumed=%v, want no credentials used for a foreign host", h.ecr.tokenCalls, h.assumed)
	}
}

func TestRun_ToolFilesOverTheCapFailTheImage(t *testing.T) {
	t.Run("sbom", func(t *testing.T) {
		old := maxSBOMBytes
		maxSBOMBytes = 100
		t.Cleanup(func() { maxSBOMBytes = old })
		h := newHarness(t, image(digestSingle, "stable"))
		if code := h.run(); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if o := h.outcome(digestSingle); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeResultTooLarge {
			t.Errorf("outcome = %+v, want failed/result_too_large", o)
		}
		if h.hasResult(digestSingle) || len(h.cmd.scanCalls("grype")) != 0 {
			t.Error("grype must not run and no result may be written")
		}
	})
	t.Run("grype report", func(t *testing.T) {
		old := maxGrypeReportBytes
		maxGrypeReportBytes = 100
		t.Cleanup(func() { maxGrypeReportBytes = old })
		h := newHarness(t, image(digestSingle, "stable"))
		if code := h.run(); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if o := h.outcome(digestSingle); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeResultTooLarge {
			t.Errorf("outcome = %+v, want failed/result_too_large", o)
		}
		if h.hasResult(digestSingle) {
			t.Error("no result may be written")
		}
	})
}

func TestRun_ImageTimeoutIsCappedByTheRemainingBudget(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	h.job.Limits.TaskBudgetSeconds = 60
	h.job.Limits.ImageTimeoutSeconds = 600
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	scans := h.cmd.scanCalls("syft")
	if len(scans) != 1 || scans[0].timeout <= 0 || scans[0].timeout > 60*time.Second {
		t.Fatalf("syft timeout = %v, want at most the 60s budget", scans[0].timeout)
	}
}

func TestRun_ToolsDoNotCheckForUpdates(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, c := range h.cmd.calls {
		want := "GRYPE_CHECK_FOR_APP_UPDATE=false"
		if strings.HasSuffix(c.name, "syft") {
			want = "SYFT_CHECK_FOR_APP_UPDATE=false"
		}
		if !contains(c.env, want) {
			t.Errorf("%s %v env lacks %s", c.name, c.args, want)
		}
	}
}

func TestRun_TruncatesResultToFitTheByteCap(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	severities := []string{"Negligible", "Low", "Medium", "High", "Critical"}
	var matches []map[string]any
	criticals := 0
	for i := 0; i < 4500; i++ {
		severity := severities[i%len(severities)]
		if severity == "Critical" {
			criticals++
		}
		matches = append(matches, map[string]any{
			"vulnerability": map[string]any{
				"id": fmt.Sprintf("CVE-0000-%04d", i), "severity": severity,
				"dataSource": "https://example.com/" + strings.Repeat("d", 200),
				"urls":       []string{"https://example.com/" + strings.Repeat("u", 200)},
				"fix":        map[string]any{"versions": []string{strings.Repeat("9", 100)}, "state": "fixed"},
			},
			"artifact": map[string]any{"name": strings.Repeat("p", 120), "version": "1", "type": "deb",
				"locations": []map[string]string{{"path": "/" + strings.Repeat("l", 200)}}},
		})
	}
	big, err := json.Marshal(map[string]any{"matches": matches})
	if err != nil {
		t.Fatal(err)
	}
	h.cmd.grypeData = [][]byte{big}

	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	raw := h.s3.objects[scan.ResultKey(testRunID, digestSingle)]
	if len(raw) > scan.MaxResultBytes {
		t.Fatalf("result is %d bytes, over the %d cap", len(raw), scan.MaxResultBytes)
	}
	res := h.result(digestSingle)
	if !res.Truncated || len(res.Findings) == 0 || len(res.Findings) >= 4500 {
		t.Fatalf("truncated=%v findings=%d, want a truncated partial list", res.Truncated, len(res.Findings))
	}
	kept := 0
	for _, f := range res.Findings {
		if f.Severity == scan.SeverityCritical {
			kept++
		}
	}
	if kept != criticals {
		t.Errorf("kept %d of %d critical findings; the lowest severities must go first", kept, criticals)
	}
}

func TestRun_CapsFindingsAtJobLimit(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	h.job.Limits.MaxFindings = 2
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res := h.result(digestSingle)
	if len(res.Findings) != 2 || !res.Truncated {
		t.Fatalf("findings=%d truncated=%v, want 2 and truncated", len(res.Findings), res.Truncated)
	}
	if res.Findings[0].Severity != scan.SeverityCritical || res.Findings[1].Severity != scan.SeverityHigh {
		t.Errorf("kept %s and %s, want the two highest severities", res.Findings[0].Severity, res.Findings[1].Severity)
	}
}

func TestRun_ImageTimeoutIsReportedAndOthersContinue(t *testing.T) {
	h := newHarness(t, image(digestIndex, "edge"), image(digestSingle, "stable"))
	h.cmd.syftFn = func(c call) (CommandResult, error, bool) {
		if strings.Contains(c.args[0], digestAMD64) {
			return CommandResult{}, context.DeadlineExceeded, true
		}
		return CommandResult{}, nil, false
	}
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if o := h.outcome(digestIndex); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeTimeout {
		t.Errorf("timed out image outcome = %+v, want failed/timeout", o)
	}
	if h.hasResult(digestIndex) {
		t.Error("a failed image must not have a result object")
	}
	if o := h.outcome(digestSingle); o.Status != scan.OutcomeScanned {
		t.Errorf("second image outcome = %+v, want scanned", o)
	}
	h.result(digestSingle)
}

func TestRun_BudgetSkipsRemainingImages(t *testing.T) {
	h := newHarness(t, image(digestIndex, "edge"), image(digestSingle, "stable"))
	h.job.Limits.TaskBudgetSeconds = 60
	h.cmd.onSyftScan = func() { h.now = h.now.Add(90 * time.Second) }
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if o := h.outcome(digestIndex); o.Status != scan.OutcomeScanned {
		t.Errorf("first image outcome = %+v, want scanned", o)
	}
	if o := h.outcome(digestSingle); o.Status != scan.OutcomeSkipped {
		t.Errorf("second image outcome = %+v, want skipped", o)
	}
	if h.hasResult(digestSingle) {
		t.Error("a skipped image must not have a result object")
	}
}

func TestRun_ImageTooLarge(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	h.job.Limits.MaxImageMB = 10
	h.ecr.sizes[digestSingle] = 11 << 20
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if o := h.outcome(digestSingle); o.Status != scan.OutcomeFailed || o.FailureCode != scan.CodeImageTooLarge {
		t.Errorf("outcome = %+v, want failed/image_too_large", o)
	}
	if n := len(h.cmd.scanCalls("syft")); n != 0 {
		t.Errorf("syft ran %d times for an oversized image", n)
	}
}

func TestRun_FailureCodes(t *testing.T) {
	cases := []struct {
		name     string
		arrange  func(h *harness)
		wantCode string
		wantText string
	}{
		{"access denied on describe", func(h *harness) {
			h.ecr.describeErr = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
		}, scan.CodePullDenied, ""},
		{"access denied on token", func(h *harness) {
			h.ecr.tokenErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "no"}
		}, scan.CodePullDenied, ""},
		{"access denied on batch get", func(h *harness) {
			h.ecr.batchErr[digestSingle] = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
		}, scan.CodePullDenied, ""},
		{"throttled", func(h *harness) {
			h.ecr.describeErr = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}
		}, scan.CodeRateLimited, ""},
		{"syft unauthorized", func(h *harness) {
			h.cmd.syftFn = func(call) (CommandResult, error, bool) {
				return CommandResult{ExitCode: 1, StderrTail: "unauthorized: authentication required"}, nil, true
			}
		}, scan.CodePullDenied, ""},
		{"syft other failure", func(h *harness) {
			h.cmd.syftFn = func(call) (CommandResult, error, bool) {
				return CommandResult{ExitCode: 1, StderrTail: "boom: layer is corrupt"}, nil, true
			}
		}, scan.CodeTaskFailed, "boom: layer is corrupt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, image(digestSingle, "stable"))
			tc.arrange(h)
			if code := h.run(); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			o := h.outcome(digestSingle)
			if o.Status != scan.OutcomeFailed || o.FailureCode != tc.wantCode || !strings.Contains(o.FailureDetail, tc.wantText) {
				t.Errorf("outcome = %+v, want failed/%s with %q", o, tc.wantCode, tc.wantText)
			}
			if h.hasResult(digestSingle) {
				t.Error("a failed image must not have a result object")
			}
		})
	}
}

func TestRun_RefusesForeignRunID(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	h.job.RunID = testRunID + 1
	if code := h.run(); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(h.s3.puts) != 0 || len(h.cmd.scanCalls("syft")) != 0 {
		t.Errorf("puts=%v syft scans=%d, want nothing", h.s3.puts, len(h.cmd.scanCalls("syft")))
	}
}

func TestRun_RefusesUnknownMode(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	h.runner.Opts.Mode = "refresh"
	if code := h.run(); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(h.s3.puts) != 0 || len(h.s3.gets) != 0 {
		t.Errorf("puts=%v gets=%v, want no bucket access", h.s3.puts, h.s3.gets)
	}
}

func TestRun_ExitsOneWithoutAJob(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	if code := h.runner.Run(context.Background()); code != 1 {
		t.Fatalf("exit code = %d, want 1 when there is no job", code)
	}
}

// The scanner may import only internal/scan from this module (D8).
func TestScannerImportsOnlyScanFromTheModule(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	allowed := map[string]bool{
		module + "internal/scan": true, module + "internal/scanner": true, module + "cmd/harbormaster-scanner": true,
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".", "../../cmd/harbormaster-scanner").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, module) && !allowed[pkg] {
			t.Errorf("scanner depends on %s; only internal/scan is allowed", pkg)
		}
	}
	if !strings.Contains(string(out), module+"cmd/harbormaster-scanner") {
		t.Error("the scanner command was not part of the dependency check")
	}
}
