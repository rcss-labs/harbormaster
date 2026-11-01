package fake

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// plan 05
var (
	_ awsx.EC2API = (*ec2.Client)(nil)
	_ awsx.EFSAPI = (*efs.Client)(nil)
	_ awsx.EC2API = (*EC2)(nil)
	_ awsx.EFSAPI = (*EFS)(nil)
)

const mainVpcID = "vpc-0a41c7e29b3d"

func ec2For(t *testing.T, f *Fake, target awsx.Target) awsx.EC2API {
	t.Helper()
	api, err := f.EC2(context.Background(), target)
	if err != nil {
		t.Fatalf("EC2(%v): %v", target, err)
	}
	return api
}

func TestFakeEC2_DescribeSubnetsFiltersByVpc(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	out, err := api.DescribeSubnets(context.Background(), &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{mainVpcID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Subnets) != 3 {
		t.Fatalf("subnets = %d, want 3", len(out.Subnets))
	}
}

func TestFakeEC2_DescribeVpcEndpointsIncludesS3Decoy(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	out, err := api.DescribeVpcEndpoints(context.Background(), &ec2.DescribeVpcEndpointsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{mainVpcID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.VpcEndpoints) != 5 {
		t.Fatalf("endpoints = %d, want 5", len(out.VpcEndpoints))
	}
	gateways := 0
	for _, ep := range out.VpcEndpoints {
		if ep.VpcEndpointType == ec2types.VpcEndpointTypeGateway {
			gateways++
		}
	}
	if gateways != 1 {
		t.Fatalf("gateway endpoints = %d, want 1 (the s3 decoy)", gateways)
	}
}

func TestFakeEC2_ThrottleKnob(t *testing.T) {
	f := newFake()
	f.ThrottleEC2 = 1
	api := ec2For(t, f, homelabUSEast1)
	if _, err := api.DescribeVpcs(context.Background(), &ec2.DescribeVpcsInput{}); !awsx.IsThrottle(err) {
		t.Fatalf("first call err = %v, want throttle", err)
	}
	if _, err := api.DescribeVpcs(context.Background(), &ec2.DescribeVpcsInput{}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if f.Calls()["DescribeVpcs"] != 2 {
		t.Fatalf("calls = %v", f.Calls())
	}
}

func TestFakeEC2_FailNextHonoured(t *testing.T) {
	f := newFake()
	boom := errBoom{}
	f.FailNext("DescribeNatGateways", boom)
	api := ec2For(t, f, homelabUSEast1)
	if _, err := api.DescribeNatGateways(context.Background(), &ec2.DescribeNatGatewaysInput{}); err != boom {
		t.Fatalf("err = %v, want injected", err)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func TestFakeEC2_AvailableIPsSubtractRunningTasks(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	out, err := api.DescribeSubnets(context.Background(), &ec2.DescribeSubnetsInput{SubnetIds: []string{"subnet-03f1a9c2"}})
	if err != nil || len(out.Subnets) != 1 {
		t.Fatalf("subnets %v err %v", out, err)
	}
	running := 0
	for _, tk := range f.Estate.Tasks {
		if aws.ToString(tk.Task.LastStatus) == "RUNNING" && taskSubnetID(tk) == "subnet-03f1a9c2" {
			running++
		}
	}
	if running == 0 {
		t.Fatal("expected running tasks in private-1a")
	}
	if got, want := int(aws.ToInt32(out.Subnets[0].AvailableIpAddressCount)), 231-running; got != want {
		t.Fatalf("available = %d, want %d", got, want)
	}
}

func TestFakeEC2_ScopedToAccountAndRegion(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, awsx.Target{AccountID: homelabID, Region: "eu-west-1"})
	out, err := api.DescribeVpcs(context.Background(), &ec2.DescribeVpcsInput{})
	if err != nil || len(out.Vpcs) != 1 || aws.ToString(out.Vpcs[0].VpcId) != "vpc-0f3392bb71e2" {
		t.Fatalf("vpcs %v err %v", out, err)
	}
}

func TestFakeEC2_PaginatesWithMaxResults(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	var ids []string
	var token *string
	pages := 0
	for {
		out, err := api.DescribeSubnets(context.Background(), &ec2.DescribeSubnetsInput{MaxResults: aws.Int32(10), NextToken: token})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, s := range out.Subnets {
			ids = append(ids, aws.ToString(s.SubnetId))
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	if len(ids) != 6 || pages != 3 {
		t.Fatalf("ids %d pages %d, want 6 subnets over 3 pages of 2", len(ids), pages)
	}
}

func TestFakeEC2_SecurityGroupsByGroupID(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	all, err := api.DescribeSecurityGroups(context.Background(), &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{mainVpcID}}}})
	if err != nil || len(all.SecurityGroups) < 2 {
		t.Fatalf("sgs %v err %v", all, err)
	}
	one, err := api.DescribeSecurityGroups(context.Background(), &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{aws.ToString(all.SecurityGroups[0].GroupId)}}}})
	if err != nil || len(one.SecurityGroups) != 1 {
		t.Fatalf("by group-id %v err %v", one, err)
	}
}

func TestFakeEC2_InternetGatewaysAndNatsByVpc(t *testing.T) {
	f := newFake()
	api := ec2For(t, f, homelabUSEast1)
	igw, err := api.DescribeInternetGateways(context.Background(), &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{{Name: aws.String("attachment.vpc-id"), Values: []string{mainVpcID}}}})
	if err != nil || len(igw.InternetGateways) != 1 {
		t.Fatalf("igw %v err %v", igw, err)
	}
	nat, err := api.DescribeNatGateways(context.Background(), &ec2.DescribeNatGatewaysInput{
		Filter: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{mainVpcID}}}})
	if err != nil || len(nat.NatGateways) != 1 || aws.ToString(nat.NatGateways[0].SubnetId) != "subnet-09c2d5ee" {
		t.Fatalf("nat %v err %v", nat, err)
	}
}
