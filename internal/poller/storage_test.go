package poller

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// recordingStorageStore captures the last ReplaceStorage call.
type recordingStorageStore struct {
	replaces     int
	fileSystems  []model.FileSystem
	accessPoints []model.AccessPoint
}

func (r *recordingStorageStore) ReplaceStorage(_ context.Context, _, _ string, fs []model.FileSystem, aps []model.AccessPoint) error {
	r.replaces++
	r.fileSystems, r.accessPoints = fs, aps
	return nil
}

var storageTarget = awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}

func TestThroughputModeLabel(t *testing.T) {
	tests := []struct {
		name        string
		mode        efstypes.ThroughputMode
		provisioned *float64
		want        string
	}{
		{"bursting", efstypes.ThroughputModeBursting, nil, "Bursting"},
		{"elastic", efstypes.ThroughputModeElastic, nil, "Elastic"},
		{"provisioned whole", efstypes.ThroughputModeProvisioned, aws.Float64(64), "Provisioned 64 MiB/s"},
		{"provisioned fractional", efstypes.ThroughputModeProvisioned, aws.Float64(12.5), "Provisioned 12.5 MiB/s"},
	}
	for _, tc := range tests {
		if got := throughputModeLabel(tc.mode, tc.provisioned); got != tc.want {
			t.Errorf("%s: throughputModeLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPerformanceModeLabel(t *testing.T) {
	if got := performanceModeLabel(efstypes.PerformanceModeGeneralPurpose); got != "General Purpose" {
		t.Errorf("generalPurpose = %q, want General Purpose", got)
	}
	if got := performanceModeLabel(efstypes.PerformanceModeMaxIo); got != "Max I/O" {
		t.Errorf("maxIO = %q, want Max I/O", got)
	}
}

func TestNormalizeFileSystem_VPCFromMountTargets(t *testing.T) {
	fsDesc := efstypes.FileSystemDescription{FileSystemId: aws.String("fs-1"), Name: aws.String("data")}
	mts := []efstypes.MountTargetDescription{
		{VpcId: aws.String("vpc-9"), AvailabilityZoneName: aws.String("us-east-1c")},
		{VpcId: aws.String("vpc-9"), AvailabilityZoneName: aws.String("us-east-1a")},
	}
	got := normalizeFileSystem(storageTarget, fsDesc, mts, time.Now())
	if got.VPCID != "vpc-9" {
		t.Errorf("VPCID = %q, want vpc-9", got.VPCID)
	}
	if want := []string{"us-east-1a", "us-east-1c"}; !reflect.DeepEqual(got.MountTargetAZs, want) {
		t.Errorf("MountTargetAZs = %v, want %v", got.MountTargetAZs, want)
	}

	noMounts := normalizeFileSystem(storageTarget, fsDesc, nil, time.Now())
	if noMounts.VPCID != "" {
		t.Errorf("VPCID without mount targets = %q, want empty", noMounts.VPCID)
	}
}

func TestNormalizeFileSystem_NameFallsBackToID(t *testing.T) {
	tests := []struct {
		name string
		desc efstypes.FileSystemDescription
		want string
	}{
		{"no name, no tags", efstypes.FileSystemDescription{FileSystemId: aws.String("fs-7")}, "fs-7"},
		{"name tag", efstypes.FileSystemDescription{FileSystemId: aws.String("fs-7"),
			Tags: []efstypes.Tag{{Key: aws.String("Name"), Value: aws.String("jenkins-home")}}}, "jenkins-home"},
	}
	for _, tc := range tests {
		if got := normalizeFileSystem(storageTarget, tc.desc, nil, time.Now()); got.Name != tc.want {
			t.Errorf("%s: Name = %q, want %q", tc.name, got.Name, tc.want)
		}
	}
}

func TestNormalizeAccessPoint_PosixUser(t *testing.T) {
	withUser := efstypes.AccessPointDescription{
		AccessPointId: aws.String("fsap-1"), FileSystemId: aws.String("fs-1"),
		RootDirectory: &efstypes.RootDirectory{Path: aws.String("/jenkins")},
		PosixUser:     &efstypes.PosixUser{Uid: aws.Int64(1000), Gid: aws.Int64(1001)},
	}
	got := normalizeAccessPoint(storageTarget, withUser, time.Now())
	if got.PosixUID == nil || *got.PosixUID != 1000 || got.PosixGID == nil || *got.PosixGID != 1001 {
		t.Errorf("posix uid/gid = %v/%v, want 1000/1001", got.PosixUID, got.PosixGID)
	}
	if got.RootPath != "/jenkins" {
		t.Errorf("RootPath = %q, want /jenkins", got.RootPath)
	}

	bare := efstypes.AccessPointDescription{AccessPointId: aws.String("fsap-2"), FileSystemId: aws.String("fs-1")}
	bareGot := normalizeAccessPoint(storageTarget, bare, time.Now())
	if bareGot.PosixUID != nil || bareGot.PosixGID != nil {
		t.Errorf("posix uid/gid without PosixUser = %v/%v, want nil/nil", bareGot.PosixUID, bareGot.PosixGID)
	}
	if bareGot.RootPath != "/" {
		t.Errorf("RootPath without RootDirectory = %q, want /", bareGot.RootPath)
	}
}

func TestPollStorage_WritesFileSystemsAndAccessPoints(t *testing.T) {
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	efsc, err := f.EFS(context.Background(), storageTarget)
	if err != nil {
		t.Fatal(err)
	}
	st := &recordingStorageStore{}
	if err := pollStorage(context.Background(), storageTarget, efsc, st, loopNow); err != nil {
		t.Fatalf("pollStorage: %v", err)
	}
	names := map[string]model.FileSystem{}
	for _, fs := range st.fileSystems {
		names[fs.Name] = fs
	}
	if len(st.fileSystems) != 3 {
		t.Fatalf("file systems = %d, want 3 (search-index is in eu-west-1)", len(st.fileSystems))
	}
	if jenkins, ok := names["jenkins-home"]; !ok || jenkins.VPCID != "vpc-07de19a4c011" {
		t.Errorf("jenkins-home = %+v, want present in vpc-07de19a4c011", jenkins)
	}
	if len(st.accessPoints) != 2 {
		t.Fatalf("access points = %d, want 2", len(st.accessPoints))
	}
}

func TestPollStorage_ThrottleReturnsThrottleAndWritesNothing(t *testing.T) {
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	f.ThrottleEFS = 1
	efsc, err := f.EFS(context.Background(), storageTarget)
	if err != nil {
		t.Fatal(err)
	}
	st := &recordingStorageStore{}
	err = pollStorage(context.Background(), storageTarget, efsc, st, loopNow)
	if !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle", err)
	}
	if st.replaces != 0 {
		t.Fatalf("ReplaceStorage called %d times, want 0", st.replaces)
	}
}
