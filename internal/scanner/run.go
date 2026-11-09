package scanner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

// Caps on files the tools write; variables so tests can lower them.
var (
	maxSBOMBytes        int64 = 256 << 20
	maxGrypeReportBytes int64 = 64 << 20
)

const (
	maxJobBytes         = 1 << 20
	grypeTimeout        = 2 * time.Minute
	defaultImageTimeout = 10 * time.Minute
	toolStdoutMax       = 1 << 20
	versionTimeout      = 30 * time.Second
)

type Options struct {
	Bucket              string
	WorkDir             string
	SyftPath, GrypePath string
	RunID               int64
	Mode                string
	Now                 func() time.Time
	Version             string
	ScannerImageDigest  string // HM_SCAN_IMAGE_DIGEST, echoed in the manifest
}

type Runner struct {
	Opts   Options
	S3     S3Client
	Cmd    Command
	NewECR func(ctx context.Context, role, externalID, region string) (ECRClient, error)
	Log    *slog.Logger
	// Test seams for base image fetches; nil means the real resolver, dialer and trust roots.
	baseLookup func(ctx context.Context, host string) ([]netip.Addr, error)
	baseDial   func(ctx context.Context, network, address string) (net.Conn, error)
	baseTLS    *tls.Config

	bases       map[string]scan.BaseLayers // by base ref and platform, for the run
	syftVersion string
	ecrClients  map[string]ECRClient
	auths       map[string]registryAuth
}

func (r *Runner) tmpDir() string { return filepath.Join(r.Opts.WorkDir, "tmp") }

// Run executes the job and returns the process exit code: 0 when the manifest
// was written, 1 otherwise.
func (r *Runner) Run(ctx context.Context) (exitCode int) {
	if r.Opts.Mode != scan.ModeScan {
		r.Log.Error("unknown scan mode", "mode", r.Opts.Mode)
		return 1
	}
	started := r.Opts.Now()
	r.ecrClients, r.auths, r.bases = map[string]ECRClient{}, map[string]registryAuth{}, map[string]scan.BaseLayers{}
	for _, dir := range []string{r.tmpDir(), filepath.Join(r.Opts.WorkDir, "cache", "syft"), filepath.Join(r.Opts.WorkDir, "cache", "grype")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.Log.Error("create work directory", "dir", dir, "err", err)
			return 1
		}
	}

	jobData, err := r.getObject(ctx, scan.JobKey(r.Opts.RunID), maxJobBytes)
	if err != nil {
		r.Log.Error("read job", "err", err)
		return 1
	}
	var job scan.Job
	if err := json.Unmarshal(jobData, &job); err != nil {
		r.Log.Error("job is not valid JSON")
		return 1
	}
	if job.RunID != r.Opts.RunID {
		r.Log.Error("job is for another run", "job_run_id", job.RunID, "run_id", r.Opts.RunID)
		return 1
	}

	r.syftVersion = r.toolVersion(ctx, r.Opts.SyftPath)
	manifest := scan.Manifest{
		RunID: r.Opts.RunID, Kind: scan.ModeScan, Status: scan.ManifestSucceeded,
		ScannerImageDigest: r.Opts.ScannerImageDigest,
		SyftVersion:        r.syftVersion, GrypeVersion: r.toolVersion(ctx, r.Opts.GrypePath),
		DBChecksum: job.DB.Checksum, DBBuiltAt: job.DB.BuiltAt,
	}

	switch {
	case dbStale(job.DB, r.Opts.Now()):
		manifest.Status = scan.ManifestDBStale
		for _, img := range job.Images {
			manifest.Images = append(manifest.Images, scan.ImageOutcome{
				Digest: img.Digest, Status: scan.OutcomeSkipped, FailureCode: scan.CodeDBStale})
		}
	default:
		if failure := r.prepareDB(ctx, job.DB); failure != "" {
			manifest.Status, manifest.Error = scan.ManifestFailed, failure
		} else {
			manifest.Images = r.scanImages(ctx, job, started)
		}
	}

	data, err := json.Marshal(manifest)
	if err == nil {
		err = r.putResult(ctx, scan.ManifestKey(r.Opts.RunID), data)
	}
	if err != nil {
		r.Log.Error("write manifest", "err", err)
		return 1
	}
	return 0
}

func (r *Runner) toolVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	res, err := r.Cmd.Run(ctx, path, []string{"version"}, []string{"HOME=" + r.Opts.WorkDir, "SYFT_CHECK_FOR_APP_UPDATE=false", "GRYPE_CHECK_FOR_APP_UPDATE=false"}, toolStdoutMax)
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return parseToolVersion(res.Stdout)
}

// scanImages scans every image in order and reports one outcome per image.
func (r *Runner) scanImages(ctx context.Context, job scan.Job, started time.Time) []scan.ImageOutcome {
	budget := time.Duration(job.Limits.TaskBudgetSeconds) * time.Second
	outcomes := make([]scan.ImageOutcome, 0, len(job.Images))
	for _, img := range job.Images {
		if budget > 0 && r.Opts.Now().Sub(started) >= budget {
			outcomes = append(outcomes, scan.ImageOutcome{
				Digest: img.Digest, Status: scan.OutcomeSkipped, FailureDetail: "task time budget used up"})
			continue
		}
		outcomes = append(outcomes, r.scanImage(ctx, job, img, started))
	}
	return outcomes
}

func failed(digest, code, detail string) scan.ImageOutcome {
	return scan.ImageOutcome{Digest: digest, Status: scan.OutcomeFailed, FailureCode: code, FailureDetail: scan.SanitizeText(detail)}
}

// awsOutcome turns an AWS error into a coded outcome.
func awsOutcome(digest string, err error) scan.ImageOutcome {
	if code, ok := awsFailureCode(err); ok {
		return failed(digest, code, strings.ReplaceAll(code, "_", " "))
	}
	return failed(digest, scan.CodeTaskFailed, err.Error())
}

func (r *Runner) scanImage(ctx context.Context, job scan.Job, img scan.JobImage, started time.Time) scan.ImageOutcome {
	reg, ok := job.Registries[img.AccountID]
	if !ok {
		return failed(img.Digest, scan.CodeUnsupportedRegistry, "no scan role for this registry")
	}
	ref := scan.ParseImageRef(img.ImageRef)
	if ref.Kind != scan.KindECR || ref.AccountID != img.AccountID || ref.Region != img.Region {
		return failed(img.Digest, scan.CodeUnsupportedRegistry, "image is not in the job's ECR registry")
	}
	client, err := r.ecrClient(ctx, reg, img.Region)
	if err != nil {
		return awsOutcome(img.Digest, err)
	}
	auth, err := r.registryAuth(ctx, ref.Host, client)
	if err != nil {
		return awsOutcome(img.Digest, err)
	}

	described, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RegistryId: aws.String(img.AccountID), RepositoryName: aws.String(ref.Repository),
		ImageIds: []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(img.Digest)}},
	})
	if err != nil {
		return awsOutcome(img.Digest, err)
	}
	if len(described.ImageDetails) > 0 && job.Limits.MaxImageMB > 0 &&
		aws.ToInt64(described.ImageDetails[0].ImageSizeInBytes) > int64(job.Limits.MaxImageMB)<<20 {
		return failed(img.Digest, scan.CodeImageTooLarge, fmt.Sprintf("image is larger than %d MB", job.Limits.MaxImageMB))
	}

	platform := img.Platform
	if platform == "" {
		platform = defaultPlatform
	}
	source, platformDigest, fellBack, err := r.resolveSource(ctx, client, img, ref, platform)
	if err != nil {
		return awsOutcome(img.Digest, err)
	}

	workDir, err := os.MkdirTemp(r.Opts.WorkDir, "image-")
	if err != nil {
		return failed(img.Digest, scan.CodeTaskFailed, "cannot create a work directory")
	}
	defer os.RemoveAll(workDir)
	sbomPath := filepath.Join(workDir, "sbom.json")

	imageTimeout := time.Duration(job.Limits.ImageTimeoutSeconds) * time.Second
	if imageTimeout <= 0 {
		imageTimeout = defaultImageTimeout
	}
	if budget := time.Duration(job.Limits.TaskBudgetSeconds) * time.Second; budget > 0 {
		imageTimeout = min(imageTimeout, budget-r.Opts.Now().Sub(started))
	}
	syftCtx, cancelSyft := context.WithTimeout(ctx, imageTimeout)
	defer cancelSyft()
	syftRes, err := r.Cmd.Run(syftCtx, r.Opts.SyftPath,
		[]string{"registry:" + source, "--platform=" + platform, "-o", "syft-json=" + sbomPath},
		r.syftEnv(ref.Host, auth), toolStdoutMax)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failed(img.Digest, scan.CodeTimeout, "image scan timed out")
	case err != nil:
		return failed(img.Digest, scan.CodeTaskFailed, "syft could not run")
	case syftRes.ExitCode != 0:
		if strings.Contains(syftRes.StderrTail, "no such host") || strings.Contains(syftRes.StderrTail, "unauthorized") {
			return failed(img.Digest, scan.CodePullDenied, syftRes.StderrTail)
		}
		return failed(img.Digest, scan.CodeTaskFailed, syftRes.StderrTail)
	}

	sbom, err := readCapped(sbomPath, maxSBOMBytes)
	if errors.Is(err, errTooLarge) {
		return failed(img.Digest, scan.CodeResultTooLarge, "SBOM is larger than the limit")
	}
	if err != nil {
		return failed(img.Digest, scan.CodeTaskFailed, "syft wrote no SBOM")
	}
	packages, err := CountSBOMPackages(sbom)
	if err != nil {
		return failed(img.Digest, scan.CodeTaskFailed, err.Error())
	}
	if fellBack {
		// ECR would not serve the manifest, so Syft chose the platform. Only
		// accept it when the SBOM proves it is the image the job named.
		platformDigest = sbomManifestDigest(sbom)
		if platformDigest == "" || !sbomNamesDigest(sbom, img.Digest) {
			return failed(img.Digest, scan.CodeTaskFailed, "scanned image does not match the job's digest")
		}
	}

	findings, err := r.grypeFindings(ctx, job, sbomPath, filepath.Join(workDir, "grype.json"))
	if err != nil {
		var coded codedError
		if errors.As(err, &coded) {
			return failed(img.Digest, coded.code, coded.detail)
		}
		return failed(img.Digest, scan.CodeTaskFailed, err.Error())
	}

	result := scan.Result{
		Digest: img.Digest, PlatformDigest: platformDigest, Platform: platform, Status: scan.StatusScanned,
		SyftVersion: r.syftVersion, SBOMGeneratedAt: r.Opts.Now().UTC(), Packages: packages,
		Layers: sbomLayers(sbom),
	}
	if img.BaseRef != "" {
		// Bounded by baseTimeout, and a failure is recorded in the result rather than failing the scan.
		base := r.baseLayers(ctx, job.Registries, img.BaseRef, platform)
		result.Base = &base
	}
	body := fitResult(result, findings, job.Limits.MaxFindings)
	if body == nil {
		return failed(img.Digest, scan.CodeResultTooLarge, "result does not fit the size limit")
	}
	if err := r.putResult(ctx, scan.ResultKey(r.Opts.RunID, img.Digest), body); err != nil {
		r.Log.Error("write result", "err", err)
		return failed(img.Digest, scan.CodeTaskFailed, "result could not be written")
	}
	return scan.ImageOutcome{Digest: img.Digest, Status: scan.OutcomeScanned}
}

type codedError struct{ code, detail string }

func (e codedError) Error() string { return e.detail }

// resolveSource decides what Syft pulls. Normally it is the platform manifest
// by digest. When ECR cannot serve the manifest by digest (D1: not found or an
// unsupported type, as an error or a BatchGetImage failure), Syft pulls the job
// digest itself and chooses the platform; fellBack is then true and the caller
// must verify the SBOM. The job's digest is always the anchor, never the tag.
func (r *Runner) resolveSource(ctx context.Context, client ECRClient, img scan.JobImage, ref scan.Ref, platform string) (source, platformDigest string, fellBack bool, err error) {
	got, err := client.BatchGetImage(ctx, &ecr.BatchGetImageInput{
		RegistryId: aws.String(img.AccountID), RepositoryName: aws.String(ref.Repository),
		ImageIds:           []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(img.Digest)}},
		AcceptedMediaTypes: []string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerV2},
	})
	switch {
	case err != nil:
		if !fallbackErrorCodes[apiErrorCode(err)] {
			return "", "", false, err
		}
	case len(got.Images) == 0:
		if len(got.Failures) == 0 || !fallbackFailureCodes[string(got.Failures[0].FailureCode)] {
			return "", "", false, errors.New("ECR did not return the image manifest")
		}
	default:
		resolved, _, err := ResolvePlatformDigest([]byte(aws.ToString(got.Images[0].ImageManifest)),
			aws.ToString(got.Images[0].ImageManifestMediaType), platform)
		if err != nil {
			return "", "", false, err
		}
		if resolved == "" {
			resolved = img.Digest
		}
		return img.ImageRef + "@" + resolved, resolved, false, nil
	}
	return img.ImageRef + "@" + img.Digest, "", true, nil
}

var (
	fallbackErrorCodes   = map[string]bool{"ImageNotFoundException": true, "UnsupportedImageTypeException": true}
	fallbackFailureCodes = map[string]bool{"ImageNotFound": true, "ImageReferencedByManifestList": true}
)

var errTooLarge = errors.New("file is larger than the limit")

// readCapped reads a tool's output file, refusing one larger than limit.
func readCapped(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, errTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}

func (r *Runner) ecrClient(ctx context.Context, reg scan.JobRegistry, region string) (ECRClient, error) {
	key := reg.RoleARN + "|" + region
	if client, ok := r.ecrClients[key]; ok {
		return client, nil
	}
	client, err := r.NewECR(ctx, reg.RoleARN, reg.ExternalID, region)
	if err != nil {
		return nil, err
	}
	r.ecrClients[key] = client
	return client, nil
}

func (r *Runner) registryAuth(ctx context.Context, host string, client ECRClient) (registryAuth, error) {
	if auth, ok := r.auths[host]; ok {
		return auth, nil
	}
	auth, err := fetchRegistryAuth(ctx, client)
	if err != nil {
		return registryAuth{}, err
	}
	r.auths[host] = auth
	return auth, nil
}

// syftEnv is the exact environment of a Syft call. The registry password
// travels only here, never on a command line, and no AWS_ variable is passed.
func (r *Runner) syftEnv(host string, auth registryAuth) []string {
	return []string{
		"HOME=" + r.Opts.WorkDir,
		"TMPDIR=" + r.tmpDir(),
		"SYFT_CHECK_FOR_APP_UPDATE=false",
		"SYFT_CACHE_DIR=" + filepath.Join(r.Opts.WorkDir, "cache", "syft"),
		"SYFT_REGISTRY_AUTH_AUTHORITY=" + host,
		"SYFT_REGISTRY_AUTH_USERNAME=" + auth.username,
		"SYFT_REGISTRY_AUTH_PASSWORD=" + auth.password,
	}
}

func (r *Runner) grypeFindings(ctx context.Context, job scan.Job, sbomPath, outPath string) ([]scan.Finding, error) {
	grypeCtx, cancel := context.WithTimeout(ctx, grypeTimeout)
	defer cancel()
	res, err := r.Cmd.Run(grypeCtx, r.Opts.GrypePath,
		[]string{"sbom:" + sbomPath, "-o", "json", "--file", outPath}, r.grypeEnv(job.DB.MaxAgeHours), toolStdoutMax)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil, codedError{scan.CodeTimeout, "vulnerability matching timed out"}
	case err != nil:
		return nil, errors.New("grype could not run")
	case res.ExitCode != 0:
		return nil, codedError{scan.CodeTaskFailed, res.StderrTail}
	}
	data, err := readCapped(outPath, maxGrypeReportBytes)
	if errors.Is(err, errTooLarge) {
		return nil, codedError{scan.CodeResultTooLarge, "grype report is larger than the limit"}
	}
	if err != nil {
		return nil, errors.New("grype wrote no report")
	}
	return ParseGrypeOutput(data)
}

// fitResult builds the result object with the highest-severity findings that
// fit both the job's finding cap and the 2 MiB object cap, setting Truncated
// when any were cut. It returns nil only if even an empty list does not fit.
func fitResult(result scan.Result, findings []scan.Finding, jobMaxFindings int) []byte {
	sort.SliceStable(findings, func(i, j int) bool { return rankOf(findings[i].Severity) < rankOf(findings[j].Severity) })
	limit := scan.MaxFindings
	if jobMaxFindings > 0 && jobMaxFindings < limit {
		limit = jobMaxFindings
	}
	if len(findings) > limit {
		findings, result.Truncated = findings[:limit], true
	}
	encode := func(n int) []byte {
		candidate := result
		candidate.Findings = findings[:n]
		if n < len(findings) {
			candidate.Truncated = true
		}
		data, err := json.Marshal(candidate)
		if err != nil {
			return nil
		}
		return data
	}
	if data := encode(len(findings)); data != nil && len(data) <= scan.MaxResultBytes {
		return data
	}
	low, high := 0, len(findings) // the largest n that fits lies in [low, high)
	for low < high-1 {
		mid := (low + high) / 2
		if data := encode(mid); data != nil && len(data) <= scan.MaxResultBytes {
			low = mid
		} else {
			high = mid
		}
	}
	if data := encode(low); data != nil && len(data) <= scan.MaxResultBytes {
		return data
	}
	return nil
}
