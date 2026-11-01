package poller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// networkPageSize is the MaxResults sent to paginated EC2 describes (accepted by every call used here).
const networkPageSize = 100

// ecsEndpointNames are the VPC endpoint services an ECS-on-Fargate task needs; other endpoints are not cached.
var ecsEndpointNames = map[string]bool{"ecr.api": true, "ecr.dkr": true, "logs": true, "ssmmessages": true}

// networkStore is the part of the store the network tier uses.
type networkStore interface {
	ListServices(ctx context.Context, f store.TargetFilter, cluster string, vis store.Visibility) ([]store.ServiceRow, error)
	ReplaceNetwork(ctx context.Context, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error
}

// NetworkLoop is the 5 min tier that copies EC2 network descriptions into the cache tables.
type NetworkLoop struct {
	Store   *store.Store
	Clients awsx.Clients
	Every   time.Duration
	Now     func() time.Time
}

func (l *NetworkLoop) Name() string            { return tierNetwork }
func (l *NetworkLoop) Interval() time.Duration { return l.Every }

// Run polls the VPCs, subnets, gateways, endpoints and in-use security groups of the target.
func (l *NetworkLoop) Run(ctx context.Context, t awsx.Target) error {
	api, err := l.Clients.EC2(ctx, t)
	if err != nil {
		return err
	}
	return pollNetwork(ctx, t, api, l.Store, observeNow(l.Now))
}

// pollNetwork reads the target's network and replaces the cached copy in one write.
// Any error returns before anything is written; the loop's back-off handles throttles.
func pollNetwork(ctx context.Context, t awsx.Target, ec2c awsx.EC2API, st networkStore, now time.Time) error {
	var vpcs []ec2types.Vpc
	vpcPages := ec2.NewDescribeVpcsPaginator(ec2c, &ec2.DescribeVpcsInput{}, func(o *ec2.DescribeVpcsPaginatorOptions) { o.Limit = networkPageSize })
	for vpcPages.HasMorePages() {
		page, err := vpcPages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("describe vpcs: %w", err)
		}
		vpcs = append(vpcs, page.Vpcs...)
	}
	vpcIDs := make([]string, 0, len(vpcs))
	for _, v := range vpcs {
		vpcIDs = append(vpcIDs, aws.ToString(v.VpcId))
	}

	var subnets []ec2types.Subnet
	subnetPages := ec2.NewDescribeSubnetsPaginator(ec2c, &ec2.DescribeSubnetsInput{}, func(o *ec2.DescribeSubnetsPaginatorOptions) { o.Limit = networkPageSize })
	for subnetPages.HasMorePages() {
		page, err := subnetPages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("describe subnets: %w", err)
		}
		subnets = append(subnets, page.Subnets...)
	}

	var igws []ec2types.InternetGateway
	var nats []ec2types.NatGateway
	var endpoints []ec2types.VpcEndpoint
	if len(vpcIDs) > 0 { // an empty filter value list is rejected by EC2
		igwPages := ec2.NewDescribeInternetGatewaysPaginator(ec2c, &ec2.DescribeInternetGatewaysInput{
			Filters: []ec2types.Filter{{Name: aws.String("attachment.vpc-id"), Values: vpcIDs}},
		}, func(o *ec2.DescribeInternetGatewaysPaginatorOptions) { o.Limit = networkPageSize })
		for igwPages.HasMorePages() {
			page, err := igwPages.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("describe internet gateways: %w", err)
			}
			igws = append(igws, page.InternetGateways...)
		}
		natPages := ec2.NewDescribeNatGatewaysPaginator(ec2c, &ec2.DescribeNatGatewaysInput{
			Filter: []ec2types.Filter{
				{Name: aws.String("vpc-id"), Values: vpcIDs},
				{Name: aws.String("state"), Values: []string{"available", "pending"}},
			},
		}, func(o *ec2.DescribeNatGatewaysPaginatorOptions) { o.Limit = networkPageSize })
		for natPages.HasMorePages() {
			page, err := natPages.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("describe nat gateways: %w", err)
			}
			nats = append(nats, page.NatGateways...)
		}
		endpointPages := ec2.NewDescribeVpcEndpointsPaginator(ec2c, &ec2.DescribeVpcEndpointsInput{
			Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: vpcIDs}},
		}, func(o *ec2.DescribeVpcEndpointsPaginatorOptions) { o.Limit = networkPageSize })
		for endpointPages.HasMorePages() {
			page, err := endpointPages.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("describe vpc endpoints: %w", err)
			}
			endpoints = append(endpoints, page.VpcEndpoints...)
		}
	}

	// The clusters, service-list and services tiers run synchronously before the other tiers (syncStartTiers in loop.go), so on a fresh database their rows are already stored when the first network run reads the service security groups.
	groupIDs, err := serviceSecurityGroupIDs(ctx, t, st)
	if err != nil {
		return err
	}
	var groups []ec2types.SecurityGroup
	if len(groupIDs) > 0 {
		groupPages := ec2.NewDescribeSecurityGroupsPaginator(ec2c, &ec2.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: groupIDs}},
		}, func(o *ec2.DescribeSecurityGroupsPaginatorOptions) { o.Limit = networkPageSize })
		for groupPages.HasMorePages() {
			page, err := groupPages.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("describe security groups: %w", err)
			}
			groups = append(groups, page.SecurityGroups...)
		}
	}

	return st.ReplaceNetwork(ctx, t.AccountID, t.Region,
		normalizeVPCs(t, vpcs, now),
		normalizeSubnets(t, subnets, now),
		normalizeEdges(t, igws, nats, endpoints, now),
		normalizeSecurityGroups(t, groups, now))
}

// serviceSecurityGroupIDs returns the sorted, de-duplicated security groups used by the target's services.
// An empty cluster name lists the services of every cluster.
func serviceSecurityGroupIDs(ctx context.Context, t awsx.Target, st networkStore) ([]string, error) {
	services, err := st.ListServices(ctx, store.TargetFilter{AccountID: t.AccountID, Region: t.Region}, "", store.Visibility{Admin: true})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	seen := map[string]bool{}
	var ids []string
	for _, svc := range services {
		for _, id := range svc.SecurityGroups {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// nameTag returns the Name tag, or fallback when it is missing or empty.
func nameTag(tags []ec2types.Tag, fallback string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			return aws.ToString(tag.Value)
		}
	}
	return fallback
}

// endpointShortName maps a VPC endpoint service name to its short name, keeping only the endpoints ECS tasks use.
func endpointShortName(serviceName string) (string, bool) {
	_, after, found := strings.Cut(serviceName, "com.amazonaws.")
	if !found {
		return "", false
	}
	// after is "<region>.<short name>", e.g. "us-east-1.ecr.api".
	_, short, found := strings.Cut(after, ".")
	if !found || !ecsEndpointNames[short] {
		return "", false
	}
	return short, true
}

func normalizeVPCs(t awsx.Target, in []ec2types.Vpc, now time.Time) []model.VPC {
	out := make([]model.VPC, 0, len(in))
	for _, v := range in {
		id := aws.ToString(v.VpcId)
		out = append(out, model.VPC{
			AccountID: t.AccountID, Region: t.Region, VPCID: id,
			Name: nameTag(v.Tags, id), CIDR: aws.ToString(v.CidrBlock), IsDefault: aws.ToBool(v.IsDefault),
			Raw: rawJSON(v), PolledAt: now,
		})
	}
	return out
}

func normalizeSubnets(t awsx.Target, in []ec2types.Subnet, now time.Time) []model.Subnet {
	out := make([]model.Subnet, 0, len(in))
	for _, s := range in {
		id := aws.ToString(s.SubnetId)
		out = append(out, model.Subnet{
			AccountID: t.AccountID, Region: t.Region, SubnetID: id, VPCID: aws.ToString(s.VpcId),
			Name: nameTag(s.Tags, id), CIDR: aws.ToString(s.CidrBlock), AZ: aws.ToString(s.AvailabilityZone),
			AvailableIPs: int(aws.ToInt32(s.AvailableIpAddressCount)), Public: aws.ToBool(s.MapPublicIpOnLaunch),
			Raw: rawJSON(s), PolledAt: now,
		})
	}
	return out
}

func normalizeEdges(t awsx.Target, igws []ec2types.InternetGateway, nats []ec2types.NatGateway, eps []ec2types.VpcEndpoint, now time.Time) []model.VPCEdge {
	out := make([]model.VPCEdge, 0, len(igws)+len(nats)+len(eps))
	for _, g := range igws {
		for _, att := range g.Attachments {
			out = append(out, model.VPCEdge{
				AccountID: t.AccountID, Region: t.Region, VPCID: aws.ToString(att.VpcId),
				Kind: model.VPCEdgeIGW, ResourceID: aws.ToString(g.InternetGatewayId),
				Raw: rawJSON(g), PolledAt: now,
			})
		}
	}
	for _, n := range nats {
		out = append(out, model.VPCEdge{
			AccountID: t.AccountID, Region: t.Region, VPCID: aws.ToString(n.VpcId),
			Kind: model.VPCEdgeNAT, ResourceID: aws.ToString(n.NatGatewayId), SubnetID: aws.ToString(n.SubnetId),
			Detail: string(n.State), Raw: rawJSON(n), PolledAt: now,
		})
	}
	for _, e := range eps {
		if !liveEndpointState(e.State) {
			continue
		}
		short, ok := endpointShortName(aws.ToString(e.ServiceName))
		if !ok {
			continue
		}
		out = append(out, model.VPCEdge{
			AccountID: t.AccountID, Region: t.Region, VPCID: aws.ToString(e.VpcId),
			Kind: model.VPCEdgeEndpoint, ResourceID: aws.ToString(e.VpcEndpointId),
			Detail: short, Raw: rawJSON(e), PolledAt: now,
		})
	}
	return out
}

// liveEndpointState keeps endpoints that exist or are about to (available, pending, pendingAcceptance);
// deleting, deleted, rejected, failed and expired ones are not usable by tasks.
func liveEndpointState(state ec2types.State) bool {
	switch state {
	case ec2types.StateAvailable, ec2types.StatePending, ec2types.StatePendingAcceptance:
		return true
	}
	return false
}

func normalizeSecurityGroups(t awsx.Target, in []ec2types.SecurityGroup, now time.Time) []model.SecurityGroup {
	out := make([]model.SecurityGroup, 0, len(in))
	for _, g := range in {
		out = append(out, model.SecurityGroup{
			AccountID: t.AccountID, Region: t.Region, GroupID: aws.ToString(g.GroupId), VPCID: aws.ToString(g.VpcId),
			Name: aws.ToString(g.GroupName), Raw: rawJSON(g), PolledAt: now,
		})
	}
	return out
}
