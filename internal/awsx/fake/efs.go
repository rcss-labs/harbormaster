package fake

import (
	"context"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// EFS is the fake EFS API scoped to one account and region (plan 05). It
// answers from Estate.Network and pages two items at a time.
type EFS struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.EFSAPI = (*EFS)(nil)

const efsPageSize = 2

func (c *EFS) begin(op string) (func(), error) {
	done, err := c.f.beginThrottled(c.target, "efs", op, &c.f.ThrottleEFS)
	if err != nil {
		return done, err
	}
	return done, c.f.takeFailure(op)
}

func (c *EFS) inScope(account, region string) bool {
	return account == c.target.AccountID && region == c.target.Region
}

// efsPage slices items by Marker (an offset), efsPageSize at a time.
func efsPage[T any](items []T, marker *string) (page []T, next *string) {
	start := 0
	if marker != nil {
		start = parsePageToken(marker)
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + efsPageSize
	if end >= len(items) {
		return items[start:], nil
	}
	return items[start:end], aws.String(strconv.Itoa(end))
}

func optionalMatch(want *string, got string) bool { return want == nil || *want == got }

func (c *EFS) DescribeFileSystems(_ context.Context, in *efs.DescribeFileSystemsInput, _ ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error) {
	done, err := c.begin("DescribeFileSystems")
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []efstypes.FileSystemDescription
	for _, fs := range e.Network.FileSystems {
		if c.inScope(fs.AccountID, fs.Region) && optionalMatch(in.FileSystemId, aws.ToString(fs.FileSystem.FileSystemId)) {
			all = append(all, deepCopy(fs.FileSystem))
		}
	}
	page, next := efsPage(all, in.Marker)
	return &efs.DescribeFileSystemsOutput{FileSystems: page, NextMarker: next}, nil
}

func (c *EFS) DescribeAccessPoints(_ context.Context, in *efs.DescribeAccessPointsInput, _ ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error) {
	done, err := c.begin("DescribeAccessPoints")
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []efstypes.AccessPointDescription
	for _, ap := range e.Network.AccessPoints {
		if c.inScope(ap.AccountID, ap.Region) && optionalMatch(in.FileSystemId, aws.ToString(ap.AccessPoint.FileSystemId)) &&
			optionalMatch(in.AccessPointId, aws.ToString(ap.AccessPoint.AccessPointId)) {
			all = append(all, deepCopy(ap.AccessPoint))
		}
	}
	page, next := efsPage(all, in.NextToken)
	return &efs.DescribeAccessPointsOutput{AccessPoints: page, NextToken: next}, nil
}

func (c *EFS) DescribeMountTargets(_ context.Context, in *efs.DescribeMountTargetsInput, _ ...func(*efs.Options)) (*efs.DescribeMountTargetsOutput, error) {
	done, err := c.begin("DescribeMountTargets")
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []efstypes.MountTargetDescription
	for _, mt := range e.Network.MountTargets {
		if c.inScope(mt.AccountID, mt.Region) && optionalMatch(in.FileSystemId, aws.ToString(mt.MountTarget.FileSystemId)) &&
			optionalMatch(in.MountTargetId, aws.ToString(mt.MountTarget.MountTargetId)) {
			all = append(all, deepCopy(mt.MountTarget))
		}
	}
	page, next := efsPage(all, in.Marker)
	return &efs.DescribeMountTargetsOutput{MountTargets: page, NextMarker: next}, nil
}
