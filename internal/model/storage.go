package model

import (
	"encoding/json"
	"time"
)

// FileSystem is one EFS file system as cached by the storage poller.
// ProvisionedMiBps is nil unless ThroughputMode is "provisioned".
type FileSystem struct {
	AccountID        string
	Region           string
	FileSystemID     string
	ARN              string
	Name             string
	SizeBytes        int64
	ThroughputMode   string
	ProvisionedMiBps *float64
	PerformanceMode  string
	LifecycleState   string
	Encrypted        bool
	VPCID            string
	MountTargetAZs   []string
	Raw              json.RawMessage
	PolledAt         time.Time
}

// AccessPoint is one EFS access point of a file system.
// PosixUID and PosixGID are nil when the access point sets no POSIX user.
type AccessPoint struct {
	AccountID     string
	Region        string
	AccessPointID string
	FileSystemID  string
	Name          string
	RootPath      string
	PosixUID      *int64
	PosixGID      *int64
	Raw           json.RawMessage
	PolledAt      time.Time
}
