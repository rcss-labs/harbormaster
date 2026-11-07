package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
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

// maxWarnings caps the manifest warnings kept before they are summarised.
const maxWarnings = 20

// SanitizeText drops control characters and invalid UTF-8, then cuts to MaxStringLen bytes.
func SanitizeText(s string) string { return sanitizeLimit(s, MaxStringLen) }

func sanitizeLimit(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || isInvisible(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isInvisible covers control, format (bidi, zero-width, tag) and line or
// paragraph separator characters, none of which belong in displayed text.
func isInvisible(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// safeHTTPSURL returns raw when it is a plain https URL with a host and no
// credentials; it checks the raw value so that stripping cannot repair a bad URL.
func safeHTTPSURL(raw string) string {
	if raw == "" || len(raw) > MaxURLLen || !strings.HasPrefix(raw, "https://") {
		return ""
	}
	for _, r := range raw {
		if r == '\\' || r == utf8.RuneError || unicode.IsSpace(r) || isInvisible(r) {
			return ""
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return raw
}

func normaliseSeverity(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNegligible:
		return v
	}
	return SeverityUnknown
}

// decodeBounded decodes exactly one JSON document. Errors are fixed text:
// decoder errors can echo attacker-controlled content.
func decodeBounded(data []byte, limit int, what string, into any) error {
	if len(data) > limit {
		return fmt.Errorf("%s is larger than %d bytes", what, limit)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s is not valid JSON", what)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s has data after the JSON document", what)
	}
	return nil
}

// rawResult is Result with the findings left undecoded so they can be read one at a time.
type rawResult struct {
	Digest          string          `json:"digest"`
	PlatformDigest  string          `json:"platform_digest"`
	Platform        string          `json:"platform"`
	Status          string          `json:"status"`
	SyftVersion     string          `json:"syft_version"`
	SBOMGeneratedAt time.Time       `json:"sbom_generated_at"`
	Packages        int64           `json:"packages"`
	Findings        json.RawMessage `json:"findings"`
	Truncated       bool            `json:"truncated"`
}

// readFindings decodes the findings array element by element and stops after
// maxRawFindings elements, so a body of many tiny elements cannot balloon memory.
func readFindings(raw json.RawMessage, handle func(Finding) bool) (cut bool, err error) {
	if len(raw) == 0 {
		return false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, tokenErr := dec.Token()
	if tokenErr != nil {
		return false, errors.New("result findings are not valid")
	}
	if token == nil {
		return false, nil
	}
	if delim, ok := token.(json.Delim); !ok || delim != '[' {
		return false, errors.New("result findings are not a list")
	}
	for read := 0; dec.More(); read++ {
		if read >= 2*MaxFindings {
			return true, nil
		}
		var f Finding
		if err := dec.Decode(&f); err != nil {
			return false, errors.New("result finding is not valid")
		}
		if !handle(f) {
			return true, nil
		}
	}
	return false, nil
}

// ParseResult validates one image's result file. The input is untrusted.
func ParseResult(data []byte, wantDigest string) (Result, error) {
	var raw rawResult
	if err := decodeBounded(data, MaxResultBytes, "result", &raw); err != nil {
		return Result{}, err
	}
	if raw.Digest != wantDigest || !digestPattern.MatchString(raw.Digest) {
		return Result{}, errors.New("result digest does not match the scanned image")
	}
	if raw.Status != StatusScanned {
		return Result{}, errors.New("result status is not scanned")
	}
	if raw.Packages < 0 || raw.Packages > math.MaxInt32 {
		return Result{}, errors.New("result package count is out of range")
	}
	if raw.SBOMGeneratedAt.After(time.Now().Add(maxClockSkew)) {
		return Result{}, errors.New("result SBOM time is in the future")
	}

	res := Result{
		Digest:          raw.Digest,
		Status:          StatusScanned,
		SBOMGeneratedAt: raw.SBOMGeneratedAt,
		Packages:        int(raw.Packages),
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
	seen := make(map[dedupeKey]struct{}, MaxFindings)
	cut, err := readFindings(raw.Findings, func(f Finding) bool {
		if !vulnIDPattern.MatchString(f.VulnID) {
			res.Truncated = true
			return true
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
			URL:            safeHTTPSURL(f.URL),
		}
		if f.CVSS != nil && !math.IsNaN(*f.CVSS) {
			score := math.Min(math.Max(*f.CVSS, 0), 10)
			clean.CVSS = &score
		}
		key := dedupeKey{clean.VulnID, clean.PackageName, clean.PackageVersion}
		if _, dup := seen[key]; dup {
			return true
		}
		if len(res.Findings) >= MaxFindings {
			res.Truncated = true
			return false
		}
		seen[key] = struct{}{}
		res.Findings = append(res.Findings, clean)
		return true
	})
	if err != nil {
		return Result{}, err
	}
	if cut {
		res.Truncated = true
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
		return Manifest{}, nil, errors.New("manifest run id does not match the run")
	}
	switch raw.Status {
	case ManifestSucceeded, ManifestFailed, ManifestDBStale:
	default:
		return Manifest{}, nil, errors.New("manifest has an unknown status")
	}

	m := Manifest{
		RunID:  raw.RunID,
		Status: raw.Status,
		Error:  SanitizeText(raw.Error),
	}
	if raw.Kind == ModeScan {
		m.Kind = raw.Kind
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
	droppedImages := 0
	listed := make(map[string]struct{}, len(raw.Images))
	for _, image := range raw.Images {
		if _, ok := wanted[image.Digest]; !ok {
			droppedImages++
			if len(warnings) < maxWarnings {
				warnings = append(warnings, fmt.Sprintf("manifest names image %q that is not in the job; dropped", SanitizeText(image.Digest)))
			}
			continue
		}
		if _, dup := listed[image.Digest]; dup {
			continue
		}
		listed[image.Digest] = struct{}{}

		outcome := ImageOutcome{Digest: image.Digest, Status: image.Status, FailureDetail: SanitizeText(image.FailureDetail)}
		if image.Status == OutcomeScanned {
			outcome.FailureDetail = ""
		}
		switch image.Status {
		case OutcomeScanned, OutcomeFailed, OutcomeSkipped:
		default:
			outcome.Status = OutcomeFailed
		}
		if outcome.Status == OutcomeScanned {
			// a scanned image has no failure
		} else if isKnownCode(image.FailureCode) {
			outcome.FailureCode = image.FailureCode
		} else if image.FailureCode != "" || outcome.Status == OutcomeFailed {
			outcome.FailureCode = CodeTaskFailed
		}
		m.Images = append(m.Images, outcome)
	}
	if droppedImages > maxWarnings {
		warnings = append(warnings, fmt.Sprintf("and %d more", droppedImages-maxWarnings))
	}
	return m, warnings, nil
}

func isKnownCode(code string) bool {
	switch code {
	case CodePullDenied, CodeRateLimited, CodeTimeout, CodeUnsupportedRegistry,
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
