package poller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// storageStore is the part of the store the storage tier uses.
type storageStore interface {
	ReplaceStorage(ctx context.Context, accountID, region string, fs []model.FileSystem, aps []model.AccessPoint) error
}

// StorageLoop is the 5 min tier that copies EFS file systems, mount targets and access points into the cache tables.
// plan 05
type StorageLoop struct {
	Store   *store.Store
	Clients awsx.Clients
	Every   time.Duration
	Now     func() time.Time
}

func (l *StorageLoop) Name() string            { return tierStorage }
func (l *StorageLoop) Interval() time.Duration { return l.Every }

// Run polls the EFS file systems, their mount targets and access points of the target.
func (l *StorageLoop) Run(ctx context.Context, t awsx.Target) error {
	api, err := l.Clients.EFS(ctx, t)
	if err != nil {
		return err
	}
	return pollStorage(ctx, t, api, l.Store, observeNow(l.Now))
}

// pollStorage reads the target's EFS estate and replaces the cached copy in one write.
// Any error returns before anything is written; the loop's back-off handles throttles.
func pollStorage(ctx context.Context, t awsx.Target, efsc awsx.EFSAPI, st storageStore, now time.Time) error {
	var fileSystems []normalizedFileSystem
	fsPages := efs.NewDescribeFileSystemsPaginator(efsc, &efs.DescribeFileSystemsInput{})
	for fsPages.HasMorePages() {
		page, err := fsPages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("describe file systems: %w", err)
		}
		for _, fs := range page.FileSystems {
			mts, err := describeMountTargets(ctx, efsc, aws.ToString(fs.FileSystemId))
			if err != nil {
				return err
			}
			fileSystems = append(fileSystems, normalizedFileSystem{desc: fs, mounts: mts})
		}
	}

	var accessPoints []efstypes.AccessPointDescription
	apPages := efs.NewDescribeAccessPointsPaginator(efsc, &efs.DescribeAccessPointsInput{})
	for apPages.HasMorePages() {
		page, err := apPages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("describe access points: %w", err)
		}
		accessPoints = append(accessPoints, page.AccessPoints...)
	}

	outFileSystems := make([]model.FileSystem, 0, len(fileSystems))
	for _, fs := range fileSystems {
		outFileSystems = append(outFileSystems, normalizeFileSystem(t, fs.desc, fs.mounts, now))
	}
	outAccessPoints := make([]model.AccessPoint, 0, len(accessPoints))
	for _, ap := range accessPoints {
		outAccessPoints = append(outAccessPoints, normalizeAccessPoint(t, ap, now))
	}
	return st.ReplaceStorage(ctx, t.AccountID, t.Region, outFileSystems, outAccessPoints)
}

// normalizedFileSystem pairs a described file system with its mount targets until normalization.
type normalizedFileSystem struct {
	desc   efstypes.FileSystemDescription
	mounts []efstypes.MountTargetDescription
}

// describeMountTargets returns every mount target of one file system.
func describeMountTargets(ctx context.Context, efsc awsx.EFSAPI, fileSystemID string) ([]efstypes.MountTargetDescription, error) {
	var mounts []efstypes.MountTargetDescription
	pages := efs.NewDescribeMountTargetsPaginator(efsc, &efs.DescribeMountTargetsInput{FileSystemId: aws.String(fileSystemID)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe mount targets of %s: %w", fileSystemID, err)
		}
		mounts = append(mounts, page.MountTargets...)
	}
	return mounts, nil
}

// normalizeFileSystem maps an EFS description and its mount targets to the cached row.
// The name is the file system name, else its Name tag, else its ID. The VPC is the first mount target's VPC.
func normalizeFileSystem(t awsx.Target, fs efstypes.FileSystemDescription, mts []efstypes.MountTargetDescription, now time.Time) model.FileSystem {
	id := aws.ToString(fs.FileSystemId)
	out := model.FileSystem{
		AccountID: t.AccountID, Region: t.Region, FileSystemID: id, ARN: aws.ToString(fs.FileSystemArn),
		Name:            nameTagEFS(fs.Tags, aws.ToString(fs.Name), id),
		ThroughputMode:  throughputModeLabel(fs.ThroughputMode, fs.ProvisionedThroughputInMibps),
		PerformanceMode: performanceModeLabel(fs.PerformanceMode),
		LifecycleState:  string(fs.LifeCycleState),
		Encrypted:       aws.ToBool(fs.Encrypted),
		Raw:             rawJSON(fs),
		PolledAt:        now,
	}
	if fs.SizeInBytes != nil {
		out.SizeBytes = fs.SizeInBytes.Value
	}
	if fs.ThroughputMode == efstypes.ThroughputModeProvisioned {
		out.ProvisionedMiBps = fs.ProvisionedThroughputInMibps
	}
	if len(mts) > 0 {
		out.VPCID = aws.ToString(mts[0].VpcId)
	}
	out.MountTargetAZs = mountTargetAZs(mts)
	return out
}

// nameTagEFS returns the file system name, else the Name tag, else the fallback.
func nameTagEFS(tags []efstypes.Tag, name, fallback string) string {
	if name != "" {
		return name
	}
	for _, tag := range tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			return aws.ToString(tag.Value)
		}
	}
	return fallback
}

// mountTargetAZs returns the sorted, de-duplicated availability zones of the mount targets.
func mountTargetAZs(mts []efstypes.MountTargetDescription) []string {
	seen := map[string]bool{}
	var zones []string
	for _, mt := range mts {
		zone := aws.ToString(mt.AvailabilityZoneName)
		if zone != "" && !seen[zone] {
			seen[zone] = true
			zones = append(zones, zone)
		}
	}
	sort.Strings(zones)
	return zones
}

// normalizeAccessPoint maps an EFS access point to the cached row. The root path defaults to "/";
// the POSIX ids are set only when the access point sets a POSIX user.
func normalizeAccessPoint(t awsx.Target, ap efstypes.AccessPointDescription, now time.Time) model.AccessPoint {
	out := model.AccessPoint{
		AccountID: t.AccountID, Region: t.Region,
		AccessPointID: aws.ToString(ap.AccessPointId), FileSystemID: aws.ToString(ap.FileSystemId),
		Name: aws.ToString(ap.Name), RootPath: "/",
		Raw: rawJSON(ap), PolledAt: now,
	}
	if ap.RootDirectory != nil && aws.ToString(ap.RootDirectory.Path) != "" {
		out.RootPath = aws.ToString(ap.RootDirectory.Path)
	}
	if ap.PosixUser != nil {
		out.PosixUID = aws.Int64(aws.ToInt64(ap.PosixUser.Uid))
		out.PosixGID = aws.Int64(aws.ToInt64(ap.PosixUser.Gid))
	}
	return out
}

// throughputModeLabel is the display form of an EFS throughput mode, e.g. "Elastic" or "Provisioned 64 MiB/s".
func throughputModeLabel(mode efstypes.ThroughputMode, provisionedMiBps *float64) string {
	switch mode {
	case efstypes.ThroughputModeBursting:
		return "Bursting"
	case efstypes.ThroughputModeElastic:
		return "Elastic"
	case efstypes.ThroughputModeProvisioned:
		if provisionedMiBps == nil {
			return "Provisioned"
		}
		return "Provisioned " + strconv.FormatFloat(*provisionedMiBps, 'f', -1, 64) + " MiB/s"
	}
	return string(mode)
}

// performanceModeLabel is the display form of an EFS performance mode.
func performanceModeLabel(mode efstypes.PerformanceMode) string {
	switch mode {
	case efstypes.PerformanceModeGeneralPurpose:
		return "General Purpose"
	case efstypes.PerformanceModeMaxIo:
		return "Max I/O"
	}
	return string(mode)
}
