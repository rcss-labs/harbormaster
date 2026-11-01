package fake

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func efsFor(t *testing.T, f *Fake, target awsx.Target) awsx.EFSAPI {
	t.Helper()
	api, err := f.EFS(context.Background(), target)
	if err != nil {
		t.Fatalf("EFS(%v): %v", target, err)
	}
	return api
}

func TestFakeEFS_DescribeFileSystemsPaginates(t *testing.T) {
	f := newFake()
	api := efsFor(t, f, homelabUSEast1)
	var names []string
	var marker *string
	pages := 0
	for {
		out, err := api.DescribeFileSystems(context.Background(), &efs.DescribeFileSystemsInput{Marker: marker})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, fs := range out.FileSystems {
			names = append(names, aws.ToString(fs.Name))
		}
		if out.NextMarker == nil {
			break
		}
		marker = out.NextMarker
	}
	if len(names) != 3 || pages != 2 {
		t.Fatalf("file systems %v over %d pages, want 3 over 2", names, pages)
	}
}

func TestFakeEFS_DescribeMountTargetsByFileSystem(t *testing.T) {
	f := newFake()
	api := efsFor(t, f, homelabUSEast1)
	out, err := api.DescribeMountTargets(context.Background(), &efs.DescribeMountTargetsInput{FileSystemId: aws.String("fs-0c91a4e7")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.MountTargets) != 2 {
		t.Fatalf("mount targets = %d, want 2", len(out.MountTargets))
	}
	for _, mt := range out.MountTargets {
		if aws.ToString(mt.VpcId) != "vpc-07de19a4c011" {
			t.Fatalf("mount target vpc = %s", aws.ToString(mt.VpcId))
		}
	}
}

func TestFakeEFS_AccessPoints(t *testing.T) {
	f := newFake()
	api := efsFor(t, f, homelabUSEast1)
	out, err := api.DescribeAccessPoints(context.Background(), &efs.DescribeAccessPointsInput{FileSystemId: aws.String("fs-0c91a4e7")})
	if err != nil || len(out.AccessPoints) != 1 {
		t.Fatalf("aps %v err %v", out, err)
	}
	ap := out.AccessPoints[0]
	if aws.ToString(ap.AccessPointId) != "fsap-0a1b2c3d4e" || aws.ToString(ap.RootDirectory.Path) != "/jenkins" || aws.ToInt64(ap.PosixUser.Uid) != 1000 {
		t.Fatalf("access point = %+v", ap)
	}
	none, err := api.DescribeAccessPoints(context.Background(), &efs.DescribeAccessPointsInput{FileSystemId: aws.String("fs-07a2b9e1")})
	if err != nil || len(none.AccessPoints) != 0 {
		t.Fatalf("uploads-archive aps %v err %v", none, err)
	}
}

func TestFakeEFS_ThrottleKnob(t *testing.T) {
	f := newFake()
	f.ThrottleEFS = 1
	api := efsFor(t, f, homelabUSEast1)
	if _, err := api.DescribeFileSystems(context.Background(), &efs.DescribeFileSystemsInput{}); !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle", err)
	}
	if _, err := api.DescribeFileSystems(context.Background(), &efs.DescribeFileSystemsInput{}); err != nil {
		t.Fatal(err)
	}
}
