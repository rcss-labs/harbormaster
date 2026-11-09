package api

import "time"

// securityNote is shown on the Security page; only requested images exist in the system.
const securityNote = "Only images requested from a service's Deploy tab are scanned."

// scanningOffHint tells an operator how to turn image scanning on.
const scanningOffHint = "Image scanning is off. Apply the harbormaster module with enable_image_scanning = true and the harbormaster-access module with scanner_task_role_arn."

// SecurityDTO is GET /api/security. With scanning off only Enabled and Hint are set.
type SecurityDTO struct {
	Enabled  bool               `json:"enabled"`
	Hint     string             `json:"hint"`
	DB       SecurityDBDTO      `json:"db"`
	Scanner  SecurityScannerDTO `json:"scanner"`
	Note     string             `json:"note"`
	Images   []SecurityImageDTO `json:"images"`
	PolledAt time.Time          `json:"polled_at"`
}

// SecurityDBDTO describes the vulnerability database. Refreshing is true only while a refresh marker is under 15 minutes old.
type SecurityDBDTO struct {
	BuiltAt          *time.Time `json:"built_at"`
	Freshness        string     `json:"freshness"`
	Refreshing       bool       `json:"refreshing"`
	LastRefreshError string     `json:"last_refresh_error"`
}

// SecurityScannerDTO summarises the scanner runs.
type SecurityScannerDTO struct {
	RunningRuns   int        `json:"running_runs"`
	RunningImages int        `json:"running_images"`
	Queued        int        `json:"queued"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     string     `json:"last_error"`
	FailingSince  *time.Time `json:"failing_since"`
}

// SeverityCountsDTO counts findings by severity.
type SeverityCountsDTO struct {
	Critical   int `json:"critical"`
	High       int `json:"high"`
	Medium     int `json:"medium"`
	Low        int `json:"low"`
	Negligible int `json:"negligible"`
	Unknown    int `json:"unknown"`
}

// SecurityImageDTO is one requested image. Requests lists only the services the caller can see.
type SecurityImageDTO struct {
	Digest            string               `json:"digest"`
	Image             string               `json:"image"`
	Repository        string               `json:"repository"`
	Tag               string               `json:"tag"`
	RegistryAccountID string               `json:"registry_account_id"`
	RegistryRegion    string               `json:"registry_region"`
	Requests          []SecurityRequestDTO `json:"requests"`
	Status            string               `json:"status"`
	FailureCode       string               `json:"failure_code"`
	FailureDetail     string               `json:"failure_detail"`
	FailureAction     string               `json:"failure_action"`
	Platform          string               `json:"platform"`
	ScannedAt         *time.Time           `json:"scanned_at"`
	MatchedDBBuiltAt  *time.Time           `json:"matched_db_built_at"`
	SyftVersion       string               `json:"syft_version"`
	Packages          *int                 `json:"packages"`
	Truncated         bool                 `json:"truncated"`
	Open              SeverityCountsDTO    `json:"open"`
	Accepted          int                  `json:"accepted"`
	LastRequestedAt   time.Time            `json:"last_requested_at"`
	BaseImage         BaseImageDTO         `json:"base_image"`
	Can               SecurityCanDTO       `json:"can"`
}

// Base image states of BaseImageDTO.
const (
	baseStateNone     = "none"
	baseStatePending  = "pending"
	baseStateMatched  = "matched"
	baseStateMismatch = "mismatch"
	baseStateError    = "error"
)

// BaseImageDTO is the base image set for the image's repository and how the last scan compared with it. ScannedRef is the
// base the last scan used. MatchedLayers and TotalLayers are set when the image's layers are known.
type BaseImageDTO struct {
	Ref           string `json:"ref"`
	ScannedRef    string `json:"scanned_ref"`
	State         string `json:"state"`
	MatchedLayers int    `json:"matched_layers"`
	TotalLayers   int    `json:"total_layers"`
	Message       string `json:"message"`
}

// SetBaseImageRequest is the body of PUT /api/security/images/{digest}/base-image. An empty ref clears the base.
type SetBaseImageRequest struct {
	BaseRef string `json:"base_ref"`
}

// SecurityRequestDTO is one service container that asked for the image to be scanned.
type SecurityRequestDTO struct {
	ServiceID     string    `json:"service_id"`
	Service       string    `json:"service"`
	Cluster       string    `json:"cluster"`
	Container     string    `json:"container"`
	Tag           string    `json:"tag"`
	Source        string    `json:"source"`
	By            string    `json:"by"`
	At            time.Time `json:"at"`
	PermissionTag TagDTO    `json:"permission_tag"`
	CanAccept     bool      `json:"can_accept"`
}

// SecurityCanDTO says what the caller may do with an image.
type SecurityCanDTO struct {
	Accept  bool `json:"accept"`
	Rescan  bool `json:"rescan"`
	SetBase bool `json:"set_base"`
}

// SecurityImageDetailDTO is GET /api/security/images/{digest}. With scanning off only Enabled and Hint are set.
type SecurityImageDetailDTO struct {
	Enabled bool   `json:"enabled"`
	Hint    string `json:"hint"`
	SecurityImageDTO
	Findings []SecurityFindingDTO `json:"findings"`
}

// SecurityFindingDTO is one vulnerability of an image. Acceptances lists the active ones of services the caller can see;
// AcceptedEverywhere is true when every one of those services has accepted it.
type SecurityFindingDTO struct {
	VulnID             string                  `json:"vuln_id"`
	Severity           string                  `json:"severity"`
	CVSS               *float64                `json:"cvss"`
	Package            string                  `json:"package"`
	Version            string                  `json:"version"`
	Type               string                  `json:"type"`
	Location           string                  `json:"location"`
	FixedVersion       string                  `json:"fixed_version"`
	FixState           string                  `json:"fix_state"`
	DataSource         string                  `json:"data_source"`
	URL                string                  `json:"url"`
	FirstSeenAt        time.Time               `json:"first_seen_at"`
	Acceptances        []SecurityAcceptanceDTO `json:"acceptances"`
	AcceptedEverywhere bool                    `json:"accepted_everywhere"`
	// Origin is "base" or "app" when the base image matched the image's layers, otherwise empty.
	Origin string `json:"origin"`
}

// SecurityAcceptanceDTO is an active risk acceptance of one finding for one service.
type SecurityAcceptanceDTO struct {
	ID        int64     `json:"id"`
	ServiceID string    `json:"service_id"`
	Service   string    `json:"service"`
	By        string    `json:"by"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	CanRevoke bool      `json:"can_revoke"`
}

// SecurityServiceDTO is GET /api/security/services/{serviceId}: the setting and every image the service requested,
// with Open and Accepted scoped to the service.
type SecurityServiceDTO struct {
	Enabled          bool               `json:"enabled"`
	PreDeployScan    bool               `json:"pre_deploy_scan"`
	CanChangeSetting bool               `json:"can_change_setting"`
	Images           []SecurityImageDTO `json:"images"`
}

// SecuritySettingRequest is the body of PUT /api/security/services/{serviceId}/setting.
type SecuritySettingRequest struct {
	PreDeployScan *bool `json:"pre_deploy_scan"`
}

// ScanRequestBody is the body of POST /api/security/services/{serviceId}/scans.
type ScanRequestBody struct {
	Container string `json:"container"`
	Tag       string `json:"tag"`
}

// Scan states of ScanStatusDTO.
const (
	scanStateNone          = "none"
	scanStateQueued        = "queued"
	scanStateScanning      = "scanning"
	scanStateScanned       = "scanned"
	scanStateFailed        = "failed"
	scanStateDBStale       = "db_stale"
	scanStateRefreshingDB  = "refreshing_db"
	scanStateUnsupported   = "unsupported"
	updatingVulnDataText   = "Updating vulnerability data"
	scanRetryAfterSeconds  = "30"
	preDeployScanOperation = "scan_setting"
)

// ScanStatusDTO is the state of the latest scan request for a container and tag, or for a digest.
type ScanStatusDTO struct {
	Enabled bool              `json:"enabled"`
	State   string            `json:"state"`
	Image   *SecurityImageDTO `json:"image"`
	Message string            `json:"message"`
}
