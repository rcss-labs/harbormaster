// Package scan holds the pure parts of image security: reference parsing,
// validation of untrusted scanner output and freshness bands.
// It does no IO so the server, the scanner and the fake can all import it.
package scan

import (
	"fmt"
	"strings"
	"time"
)

const (
	KindECR       = "ecr"
	KindECRPublic = "ecr_public"
	KindOther     = "other"
)

const (
	SeverityCritical   = "critical"
	SeverityHigh       = "high"
	SeverityMedium     = "medium"
	SeverityLow        = "low"
	SeverityNegligible = "negligible"
	SeverityUnknown    = "unknown"
)

const (
	StatusQueued   = "queued"
	StatusScanning = "scanning"
	StatusScanned  = "scanned"
	StatusFailed   = "failed"
)

const (
	CodePullDenied          = "pull_denied"
	CodeRateLimited         = "rate_limited"
	CodeTimeout             = "timeout"
	CodeUnsupportedRegistry = "unsupported_registry"
	CodeTaskFailed          = "task_failed"
	CodeImageTooLarge       = "image_too_large"
	CodeResultTooLarge      = "result_too_large"
	CodeDBStale             = "db_stale"
	CodeTaskMissing         = "task_missing"
)

// Scanner modes (HM_SCAN_MODE, scan_runs.kind, Job.Kind, Manifest.Kind).
// The server role refreshes the vulnerability DB; the scanner only reads it.
const (
	ModeScan = "scan"
)

// ImageOutcome.Status values.
const (
	OutcomeScanned = "scanned"
	OutcomeFailed  = "failed"
	OutcomeSkipped = "skipped"
)

// Manifest.Status values.
const (
	ManifestSucceeded = "succeeded"
	ManifestFailed    = "failed"
	ManifestDBStale   = "db_stale"
)

const (
	MaxManifestBytes  = 256 << 10
	MaxResultBytes    = 2 << 20
	MaxDBLatestBytes  = 1 << 20
	MaxDBArchiveBytes = 1 << 30
	MaxFindings       = 5000
	MaxStringLen      = 256
	MaxURLLen         = 512

	DBWarnAge   = 36 * time.Hour
	DBLatestKey = "grype-db/v6/latest.json"
)

type Job struct {
	RunID      int64                  `json:"run_id"`
	Kind       string                 `json:"kind"`
	Deadline   time.Time              `json:"deadline"`
	Limits     JobLimits              `json:"limits"`
	DB         JobDB                  `json:"db"`
	Registries map[string]JobRegistry `json:"registries"` // account id ->
	Images     []JobImage             `json:"images"`
}

type JobLimits struct {
	ImageTimeoutSeconds int `json:"image_timeout_seconds"`
	TaskBudgetSeconds   int `json:"task_budget_seconds"`
	MaxImageMB          int `json:"max_image_mb"`
	MaxFindings         int `json:"max_findings"`
}

type JobDB struct {
	ArchiveKey  string    `json:"archive_key"`
	Checksum    string    `json:"checksum"`
	BuiltAt     time.Time `json:"built_at"`
	MaxAgeHours int       `json:"max_age_hours"`
}

type JobRegistry struct {
	RoleARN    string `json:"role_arn"`
	ExternalID string `json:"external_id"`
	Region     string `json:"region"`
}

type JobImage struct {
	Digest    string `json:"digest"`
	ImageRef  string `json:"image_ref"`
	Tag       string `json:"tag"`
	Platform  string `json:"platform"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	Large     bool   `json:"large"`
}

func trimSHA(digest string) string { return strings.TrimPrefix(digest, "sha256:") }

// JobKey is the bucket key of a run's job file.
func JobKey(runID int64) string { return fmt.Sprintf("jobs/%d.json", runID) }

// ManifestKey is the bucket key of a run's manifest.
func ManifestKey(runID int64) string { return fmt.Sprintf("results/%d/manifest.json", runID) }

// ResultKey is the bucket key of one image's result in a run.
func ResultKey(runID int64, digest string) string {
	return fmt.Sprintf("results/%d/%s.json", runID, trimSHA(digest))
}
