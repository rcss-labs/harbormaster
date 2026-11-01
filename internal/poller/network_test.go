package poller

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// recordingNetworkStore serves a fixed service list and captures the last ReplaceNetwork call.
type recordingNetworkStore struct {
	services []store.ServiceRow
	replaces int
	vpcs     []model.VPC
	subnets  []model.Subnet
	edges    []model.VPCEdge
	sgs      []model.SecurityGroup
}

func (r *recordingNetworkStore) ListServices(context.Context, store.TargetFilter, string, store.Visibility) ([]store.ServiceRow, error) {
	return r.services, nil
}

func (r *recordingNetworkStore) ReplaceNetwork(_ context.Context, _, _ string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error {
	r.replaces++
	r.vpcs, r.subnets, r.edges, r.sgs = vpcs, subnets, edges, sgs
	return nil
}

func serviceWithGroups(groups ...string) store.ServiceRow {
	return store.ServiceRow{Service: model.Service{SecurityGroups: groups}}
}

var networkTarget = awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}

func TestNormalizeEdges_KeepsOnlyEcsEndpoints(t *testing.T) {
	var eps []ec2types.VpcEndpoint
	for _, svc := range []string{"ecr.api", "ecr.dkr", "logs", "ssmmessages", "s3"} {
		eps = append(eps, ec2types.VpcEndpoint{
			VpcEndpointId: aws.String("vpce-" + svc),
			VpcId:         aws.String("vpc-1"),
			ServiceName:   aws.String("com.amazonaws.us-east-1." + svc),
		})
	}
	edges := normalizeEdges(networkTarget, nil, nil, eps, time.Now())
	if len(edges) != 4 {
		t.Fatalf("edges = %d, want 4", len(edges))
	}
	var details []string
	for _, e := range edges {
		if e.Kind != model.VPCEdgeEndpoint {
			t.Errorf("kind = %q, want endpoint", e.Kind)
		}
		details = append(details, e.Detail)
	}
	sort.Strings(details)
	if want := []string{"ecr.api", "ecr.dkr", "logs", "ssmmessages"}; !reflect.DeepEqual(details, want) {
		t.Fatalf("details = %v, want %v", details, want)
	}
}

func TestEndpointShortName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"com.amazonaws.us-east-1.ecr.api", "ecr.api", true},
		{"com.amazonaws.us-east-1.ecr.dkr", "ecr.dkr", true},
		{"com.amazonaws.eu-west-1.logs", "logs", true},
		{"com.amazonaws.us-east-1.ssmmessages", "ssmmessages", true},
		{"com.amazonaws.us-east-1.s3", "", false},
		{"cn.com.amazonaws.cn-north-1.logs", "logs", true},
	}
	for _, tc := range tests {
		got, ok := endpointShortName(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("endpointShortName(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNameTag_FallsBackToID(t *testing.T) {
	tests := []struct {
		name string
		tags []ec2types.Tag
		want string
	}{
		{"no tags", nil, "vpc-1"},
		{"empty name", []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("")}}, "vpc-1"},
		{"named", []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("homelab-main")}}, "homelab-main"},
	}
	for _, tc := range tests {
		if got := nameTag(tc.tags, "vpc-1"); got != tc.want {
			t.Errorf("%s: nameTag = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeSubnets_PublicFromMapPublicIpOnLaunch(t *testing.T) {
	in := []ec2types.Subnet{
		{SubnetId: aws.String("subnet-a"), VpcId: aws.String("vpc-1"), MapPublicIpOnLaunch: aws.Bool(true), AvailableIpAddressCount: aws.Int32(40)},
		{SubnetId: aws.String("subnet-b"), VpcId: aws.String("vpc-1")},
	}
	got := normalizeSubnets(networkTarget, in, time.Now())
	if !got[0].Public || got[0].AvailableIPs != 40 {
		t.Errorf("subnet-a = public %v ips %d, want true 40", got[0].Public, got[0].AvailableIPs)
	}
	if got[1].Public || got[1].AvailableIPs != 0 {
		t.Errorf("subnet-b = public %v ips %d, want false 0", got[1].Public, got[1].AvailableIPs)
	}
}

func newNetworkEC2(t *testing.T) (*fake.Fake, awsx.EC2API) {
	t.Helper()
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	api, err := f.EC2(context.Background(), networkTarget)
	if err != nil {
		t.Fatal(err)
	}
	return f, api
}

func TestPollNetwork_WritesEstate(t *testing.T) {
	_, api := newNetworkEC2(t)
	st := &recordingNetworkStore{}
	if err := pollNetwork(context.Background(), networkTarget, api, st, loopNow); err != nil {
		t.Fatalf("pollNetwork: %v", err)
	}
	if len(st.vpcs) != 3 || len(st.subnets) != 6 {
		t.Fatalf("vpcs = %d subnets = %d, want 3 and 6", len(st.vpcs), len(st.subnets))
	}
	counts := map[model.VPCEdgeKind]int{}
	for _, e := range st.edges {
		counts[e.Kind]++
	}
	if counts[model.VPCEdgeIGW] != 2 || counts[model.VPCEdgeNAT] != 2 || counts[model.VPCEdgeEndpoint] != 4 {
		t.Fatalf("edge counts = %v, want igw 2 (the seeded default VPC has none) nat 2 endpoint 4", counts)
	}
	for _, v := range st.vpcs {
		if v.Name == "legacy-default" && !v.IsDefault {
			t.Error("legacy-default is not marked default")
		}
	}
}

func TestPollNetwork_SecurityGroupsOnlyFromServices(t *testing.T) {
	f, api := newNetworkEC2(t)
	// Discover two real group ids from the fake, then reference them (one twice) from services.
	all, err := api.DescribeSecurityGroups(context.Background(), nilSGInput())
	if err != nil || len(all.SecurityGroups) < 3 {
		t.Fatalf("seeded groups = %v err %v, want at least 3", len(all.SecurityGroups), err)
	}
	a, b := aws.ToString(all.SecurityGroups[0].GroupId), aws.ToString(all.SecurityGroups[1].GroupId)
	st := &recordingNetworkStore{services: []store.ServiceRow{serviceWithGroups(b, a), serviceWithGroups(a)}}
	if err := pollNetwork(context.Background(), networkTarget, api, st, loopNow); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, sg := range st.sgs {
		got = append(got, sg.GroupID)
	}
	sort.Strings(got)
	want := []string{a, b}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored groups = %v, want %v", got, want)
	}

	// No services: no DescribeSecurityGroups call at all.
	before := f.Calls()["DescribeSecurityGroups"]
	empty := &recordingNetworkStore{}
	if err := pollNetwork(context.Background(), networkTarget, api, empty, loopNow); err != nil {
		t.Fatal(err)
	}
	if after := f.Calls()["DescribeSecurityGroups"]; after != before {
		t.Fatalf("DescribeSecurityGroups calls %d -> %d, want none with no service groups", before, after)
	}
	if len(empty.sgs) != 0 {
		t.Fatalf("stored groups = %d, want 0", len(empty.sgs))
	}
}

func TestPollNetwork_ThrottleReturnsThrottleAndWritesNothing(t *testing.T) {
	f, api := newNetworkEC2(t)
	f.ThrottleEC2 = 1
	st := &recordingNetworkStore{}
	err := pollNetwork(context.Background(), networkTarget, api, st, loopNow)
	if !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle", err)
	}
	if st.replaces != 0 {
		t.Fatalf("ReplaceNetwork called %d times, want 0", st.replaces)
	}
}

func nilSGInput() *ec2.DescribeSecurityGroupsInput { return &ec2.DescribeSecurityGroupsInput{} }
