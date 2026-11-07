package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SeverityCounts struct{ Critical, High, Medium, Low, Negligible, Unknown int }

func (c SeverityCounts) Total() int {
	return c.Critical + c.High + c.Medium + c.Low + c.Negligible + c.Unknown
}

type FindingKey struct{ VulnID, PackageName string }

type Finding struct {
	VulnID         string   `json:"vuln_id"`
	Severity       string   `json:"severity"`
	CVSS           *float64 `json:"cvss,omitempty"`
	PackageName    string   `json:"package_name"`
	PackageVersion string   `json:"package_version"`
	PackageType    string   `json:"package_type"`
	Location       string   `json:"location"`
	FixedVersion   string   `json:"fixed_version"`
	FixState       string   `json:"fix_state"`
	DataSource     string   `json:"data_source"`
	URL            string   `json:"url"`
}

func (f Finding) Key() FindingKey { return FindingKey{VulnID: f.VulnID, PackageName: f.PackageName} }

type Result struct {
	Digest          string    `json:"digest"`
	PlatformDigest  string    `json:"platform_digest"`
	Platform        string    `json:"platform"`
	Status          string    `json:"status"`
	SyftVersion     string    `json:"syft_version"`
	SBOMGeneratedAt time.Time `json:"sbom_generated_at"`
	Packages        int       `json:"packages"`
	Findings        []Finding `json:"findings"`
	Truncated       bool      `json:"truncated"`
}

type ImageOutcome struct {
	Digest        string `json:"digest"`
	Status        string `json:"status"`
	FailureCode   string `json:"failure_code"`
	FailureDetail string `json:"failure_detail"`
}

type Manifest struct {
	RunID              int64          `json:"run_id"`
	Kind               string         `json:"kind"`
	Status             string         `json:"status"`
	ScannerImageDigest string         `json:"scanner_image_digest"`
	SyftVersion        string         `json:"syft_version"`
	GrypeVersion       string         `json:"grype_version"`
	DBChecksum         string         `json:"db_checksum"`
	DBBuiltAt          time.Time      `json:"db_built_at"`
	Error              string         `json:"error"`
	Images             []ImageOutcome `json:"images"`
}

var (
	digestPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	vulnIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	platformPattern = regexp.MustCompile(`^linux/[a-z0-9]+(/[a-z0-9]+)?$`)
	versionPattern  = regexp.MustCompile(`^[A-Za-z0-9.+_-]{1,64}$`)
	dbKeyPattern    = regexp.MustCompile(`^grype-db/v6/[A-Za-z0-9._-]+\.tar\.zst$`)
	dbPathPattern   = regexp.MustCompile(`^[A-Za-z0-9._:+-]+\.tar\.zst$`)
)

// maxClockSkew is how far in the future a scanner-supplied time may be.
const maxClockSkew = 5 * time.Minute

// SanitizeText drops control characters and invalid UTF-8, then cuts to MaxStringLen bytes.
func SanitizeText(s string) string { return sanitizeLimit(s, MaxStringLen) }

func sanitizeLimit(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func normaliseSeverity(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNegligible:
		return v
	}
	return SeverityUnknown
}

func decodeBounded(data []byte, limit int, what string, into any) error {
	if len(data) > limit {
		return fmt.Errorf("%s is larger than %d bytes", what, limit)
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(into); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", what, err)
	}
	return nil
}

// ParseResult validates one image's result file. The input is untrusted.
func ParseResult(data []byte, wantDigest string) (Result, error) {
	var raw Result
	if err := decodeBounded(data, MaxResultBytes, "result", &raw); err != nil {
		return Result{}, err
	}
	if raw.Digest != wantDigest || !digestPattern.MatchString(raw.Digest) {
		return Result{}, errors.New("result digest does not match the scanned image")
	}
	if raw.Status != StatusScanned {
		return Result{}, errors.New("result status is not scanned")
	}

	res := Result{
		Digest:          raw.Digest,
		Status:          StatusScanned,
		SBOMGeneratedAt: raw.SBOMGeneratedAt,
		Packages:        max(raw.Packages, 0),
		Truncated:       raw.Truncated,
	}
	if digestPattern.MatchString(raw.PlatformDigest) {
		res.PlatformDigest = raw.PlatformDigest
	}
	if platformPattern.MatchString(raw.Platform) {
		res.Platform = raw.Platform
	}
	if versionPattern.MatchString(raw.SyftVersion) {
		res.SyftVersion = raw.SyftVersion
	}

	type dedupeKey struct{ vuln, pkg, version string }
	seen := make(map[dedupeKey]struct{}, len(raw.Findings))
	for _, f := range raw.Findings {
		if !vulnIDPattern.MatchString(f.VulnID) {
			res.Truncated = true
			continue
		}
		clean := Finding{
			VulnID:         f.VulnID,
			Severity:       normaliseSeverity(f.Severity),
			PackageName:    SanitizeText(f.PackageName),
			PackageVersion: SanitizeText(f.PackageVersion),
			PackageType:    SanitizeText(f.PackageType),
			Location:       SanitizeText(f.Location),
			FixedVersion:   SanitizeText(f.FixedVersion),
			FixState:       SanitizeText(f.FixState),
			DataSource:     SanitizeText(f.DataSource),
		}
		if f.CVSS != nil && !math.IsNaN(*f.CVSS) {
			score := math.Min(math.Max(*f.CVSS, 0), 10)
			clean.CVSS = &score
		}
		if url := sanitizeLimit(f.URL, MaxURLLen); strings.HasPrefix(url, "https://") {
			clean.URL = url
		}
		key := dedupeKey{clean.VulnID, clean.PackageName, clean.PackageVersion}
		if _, dup := seen[key]; dup {
			continue
		}
		if len(res.Findings) >= MaxFindings {
			res.Truncated = true
			break
		}
		seen[key] = struct{}{}
		res.Findings = append(res.Findings, clean)
	}
	return res, nil
}

// ParseManifest validates a run manifest. Images outside wantDigests are
// dropped and named (sanitised) in the returned warnings.
func ParseManifest(data []byte, wantRunID int64, wantDigests []string) (Manifest, []string, error) {
	var raw Manifest
	if err := decodeBounded(data, MaxManifestBytes, "manifest", &raw); err != nil {
		return Manifest{}, nil, err
	}
	if raw.RunID != wantRunID {
		return Manifest{}, nil, fmt.Errorf("manifest run id %d does not match run %d", raw.RunID, wantRunID)
	}
	switch raw.Status {
	case ManifestSucceeded, ManifestFailed, ManifestDBStale:
	default:
		return Manifest{}, nil, errors.New("manifest has an unknown status")
	}

	m := Manifest{
		RunID:  raw.RunID,
		Kind:   SanitizeText(raw.Kind),
		Status: raw.Status,
		Error:  SanitizeText(raw.Error),
	}
	if digestPattern.MatchString(raw.ScannerImageDigest) {
		m.ScannerImageDigest = raw.ScannerImageDigest
	}
	if digestPattern.MatchString(raw.DBChecksum) {
		m.DBChecksum = raw.DBChecksum
	}
	if versionPattern.MatchString(raw.SyftVersion) {
		m.SyftVersion = raw.SyftVersion
	}
	if versionPattern.MatchString(raw.GrypeVersion) {
		m.GrypeVersion = raw.GrypeVersion
	}
	if !raw.DBBuiltAt.After(time.Now().Add(maxClockSkew)) {
		m.DBBuiltAt = raw.DBBuiltAt
	}

	wanted := make(map[string]struct{}, len(wantDigests))
	for _, digest := range wantDigests {
		wanted[digest] = struct{}{}
	}
	var warnings []string
	listed := make(map[string]struct{}, len(raw.Images))
	for _, image := range raw.Images {
		if _, ok := wanted[image.Digest]; !ok {
			warnings = append(warnings, fmt.Sprintf("manifest names image %q that is not in the job; dropped", SanitizeText(image.Digest)))
			continue
		}
		if _, dup := listed[image.Digest]; dup {
			continue
		}
		listed[image.Digest] = struct{}{}

		outcome := ImageOutcome{Digest: image.Digest, Status: image.Status, FailureDetail: SanitizeText(image.FailureDetail)}
		switch image.Status {
		case OutcomeScanned, OutcomeFailed, OutcomeSkipped:
		default:
			outcome.Status = OutcomeFailed
		}
		if isKnownCode(image.FailureCode) {
			outcome.FailureCode = image.FailureCode
		} else if image.FailureCode != "" || outcome.Status == OutcomeFailed {
			outcome.FailureCode = CodeTaskFailed
		}
		m.Images = append(m.Images, outcome)
	}
	return m, warnings, nil
}

func isKnownCode(code string) bool {
	switch code {
	case CodePullDenied, CodeRegistryNotConnected, CodeRateLimited, CodeTimeout, CodeUnsupportedRegistry,
		CodeTaskFailed, CodeImageTooLarge, CodeResultTooLarge, CodeDBStale, CodeTaskMissing:
		return true
	}
	return false
}

// DBLatest is grype-db/v6/latest.json in the bucket.
type DBLatest struct {
	BuiltAt    time.Time `json:"built_at"`
	Checksum   string    `json:"checksum"`
	ArchiveKey string    `json:"archive_key"`
}

// ParseDBLatest validates the bucket's latest.json.
func ParseDBLatest(data []byte, now time.Time) (DBLatest, error) {
	var db DBLatest
	if err := decodeBounded(data, MaxDBLatestBytes, "db latest", &db); err != nil {
		return DBLatest{}, err
	}
	if !digestPattern.MatchString(db.Checksum) {
		return DBLatest{}, errors.New("db latest has an invalid checksum")
	}
	if !dbKeyPattern.MatchString(db.ArchiveKey) {
		return DBLatest{}, errors.New("db latest has an invalid archive key")
	}
	if db.BuiltAt.IsZero() || db.BuiltAt.After(now.Add(maxClockSkew)) {
		return DBLatest{}, errors.New("db latest has an invalid build time")
	}
	return db, nil
}

// UpstreamDB is Anchore's latest.json.
type UpstreamDB struct {
	Built    time.Time `json:"built"`
	Path     string    `json:"path"`
	Checksum string    `json:"checksum"`
}

// ParseUpstreamDB validates Anchore's latest.json. Path must be a bare file name.
func ParseUpstreamDB(data []byte, now time.Time) (UpstreamDB, error) {
	var db UpstreamDB
	if err := decodeBounded(data, MaxDBLatestBytes, "upstream db listing", &db); err != nil {
		return UpstreamDB{}, err
	}
	if !dbPathPattern.MatchString(db.Path) || strings.Contains(db.Path, "..") {
		return UpstreamDB{}, errors.New("upstream db listing has an invalid path")
	}
	if !digestPattern.MatchString(db.Checksum) {
		return UpstreamDB{}, errors.New("upstream db listing has an invalid checksum")
	}
	if db.Built.IsZero() || db.Built.After(now.Add(maxClockSkew)) {
		return UpstreamDB{}, errors.New("upstream db listing has an invalid build time")
	}
	return db, nil
}
