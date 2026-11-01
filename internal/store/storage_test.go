package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestReplaceStorage_UpsertsFileSystemsAndAccessPoints(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	polledAt := time.Date(2026, 10, 31, 9, 30, 0, 0, time.UTC)
	provisioned := 128.5
	fileSystems := []model.FileSystem{
		{
			AccountID: "1", Region: "us-east-1", FileSystemID: "fs-1", ARN: "arn:fs-1", Name: "reports",
			SizeBytes: 4096, ThroughputMode: "provisioned", ProvisionedMiBps: &provisioned,
			PerformanceMode: "generalPurpose", LifecycleState: "available", Encrypted: true,
			VPCID: "vpc-1", MountTargetAZs: []string{"us-east-1a", "us-east-1b"},
			Raw: json.RawMessage(`{}`), PolledAt: polledAt,
		},
		{
			AccountID: "1", Region: "us-east-1", FileSystemID: "fs-2", ARN: "arn:fs-2", Name: "jenkins",
			ThroughputMode: "bursting", PerformanceMode: "generalPurpose", LifecycleState: "available",
			VPCID: "vpc-1", MountTargetAZs: []string{"us-east-1a"},
			Raw: json.RawMessage(`{}`), PolledAt: polledAt,
		},
	}
	accessPoints := []model.AccessPoint{
		{AccountID: "1", Region: "us-east-1", AccessPointID: "fsap-1", FileSystemID: "fs-1", Name: "reports-ap", RootPath: "/reports", Raw: json.RawMessage(`{}`), PolledAt: polledAt},
	}
	if err := s.ReplaceStorage(ctx, "1", "us-east-1", fileSystems, accessPoints); err != nil {
		t.Fatalf("first ReplaceStorage: %v", err)
	}

	gotFS, err := s.ListFileSystems(ctx, "1", "us-east-1")
	if err != nil {
		t.Fatalf("ListFileSystems: %v", err)
	}
	if len(gotFS) != 2 {
		t.Fatalf("file systems = %d, want 2", len(gotFS))
	}
	reports := gotFS[0]
	if len(reports.MountTargetAZs) != 2 || reports.MountTargetAZs[0] != "us-east-1a" || reports.MountTargetAZs[1] != "us-east-1b" {
		t.Errorf("MountTargetAZs = %#v, want [us-east-1a us-east-1b]", reports.MountTargetAZs)
	}
	if reports.ProvisionedMiBps == nil || *reports.ProvisionedMiBps != 128.5 {
		t.Errorf("ProvisionedMiBps = %v, want 128.5", reports.ProvisionedMiBps)
	}
	if gotFS[1].ProvisionedMiBps != nil {
		t.Errorf("bursting file system ProvisionedMiBps = %v, want nil", *gotFS[1].ProvisionedMiBps)
	}

	gotAPs, err := s.ListAccessPoints(ctx, "1", "us-east-1")
	if err != nil {
		t.Fatalf("ListAccessPoints: %v", err)
	}
	if len(gotAPs) != 1 || gotAPs[0].AccessPointID != "fsap-1" {
		t.Fatalf("access points = %+v, want fsap-1", gotAPs)
	}

	if err := s.ReplaceStorage(ctx, "1", "us-east-1", fileSystems, nil); err != nil {
		t.Fatalf("second ReplaceStorage: %v", err)
	}
	gotAPs, err = s.ListAccessPoints(ctx, "1", "us-east-1")
	if err != nil {
		t.Fatalf("ListAccessPoints after replace: %v", err)
	}
	if len(gotAPs) != 0 {
		t.Errorf("access points after replace without them = %+v, want none", gotAPs)
	}
}
