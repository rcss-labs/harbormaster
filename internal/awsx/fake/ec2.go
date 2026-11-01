package fake

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// EC2 is the fake EC2 API scoped to one account and region (plan 05). It
// answers from Estate.Network.
type EC2 struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.EC2API = (*EC2)(nil)

const ec2PageSize = 2

// ec2MaxFilterValues is the number of values EC2 accepts in one filter; more is FilterLimitExceeded.
const ec2MaxFilterValues = 200

// checkFilterLimit rejects any filter with more than ec2MaxFilterValues values, like EC2.
func checkFilterLimit(filters []ec2types.Filter) error {
	for _, flt := range filters {
		if len(flt.Values) > ec2MaxFilterValues {
			return &smithy.GenericAPIError{Code: "FilterLimitExceeded",
				Message: fmt.Sprintf("filter %s has %d values, the limit is %d", aws.ToString(flt.Name), len(flt.Values), ec2MaxFilterValues)}
		}
	}
	return nil
}

// begin counts the call, applies FailNext and the ThrottleEC2 knob.
func (c *EC2) begin(op string) error {
	if err := c.f.beginThrottled(op, &c.f.ThrottleEC2); err != nil {
		return err
	}
	return c.f.takeFailure(op)
}

func (c *EC2) inScope(account, region string) bool {
	return account == c.target.AccountID && region == c.target.Region
}

// filterValues returns the values of the named filter and whether it is present.
func filterValues(filters []ec2types.Filter, name string) ([]string, bool) {
	for _, flt := range filters {
		if aws.ToString(flt.Name) == name {
			return flt.Values, true
		}
	}
	return nil, false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// matchesFilter is true when the filter is absent or one of its values equals got.
func matchesFilter(filters []ec2types.Filter, name, got string) bool {
	values, ok := filterValues(filters, name)
	return !ok || containsString(values, got)
}

// matchesIDs is true when ids is empty or contains got.
func matchesIDs(ids []string, got string) bool { return len(ids) == 0 || containsString(ids, got) }

// ec2Page slices items by NextToken (an offset) when maxResults is set.
func ec2Page[T any](items []T, maxResults *int32, token *string) (page []T, next *string) {
	if maxResults == nil {
		return items, nil
	}
	start := 0
	if token != nil {
		start = parsePageToken(token)
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + ec2PageSize
	if end >= len(items) {
		return items[start:], nil
	}
	return items[start:end], aws.String(strconv.Itoa(end))
}

func (c *EC2) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	if err := c.begin("DescribeVpcs"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filters); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []ec2types.Vpc
	for _, v := range e.Network.Vpcs {
		id := aws.ToString(v.Vpc.VpcId)
		if c.inScope(v.AccountID, v.Region) && matchesIDs(in.VpcIds, id) && matchesFilter(in.Filters, "vpc-id", id) {
			all = append(all, deepCopy(v.Vpc))
		}
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeVpcsOutput{Vpcs: page, NextToken: next}, nil
}

// runningTasksBySubnet counts RUNNING tasks per subnet id. Caller holds Mu.
func (e *Estate) runningTasksBySubnet(account, region string) map[string]int {
	counts := map[string]int{}
	for _, tk := range e.Tasks {
		if tk.AccountID != account || tk.Region != region || aws.ToString(tk.Task.LastStatus) != "RUNNING" {
			continue
		}
		for _, att := range tk.Task.Attachments {
			for _, d := range att.Details {
				if aws.ToString(d.Name) == "subnetId" {
					counts[aws.ToString(d.Value)]++
				}
			}
		}
	}
	return counts
}

func (c *EC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	if err := c.begin("DescribeSubnets"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filters); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	used := e.runningTasksBySubnet(c.target.AccountID, c.target.Region)
	var all []ec2types.Subnet
	for _, s := range e.Network.Subnets {
		id := aws.ToString(s.Subnet.SubnetId)
		if !c.inScope(s.AccountID, s.Region) || !matchesIDs(in.SubnetIds, id) ||
			!matchesFilter(in.Filters, "vpc-id", aws.ToString(s.Subnet.VpcId)) || !matchesFilter(in.Filters, "subnet-id", id) {
			continue
		}
		sub := deepCopy(s.Subnet)
		sub.AvailableIpAddressCount = aws.Int32(int32(s.Free - used[id]))
		all = append(all, sub)
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeSubnetsOutput{Subnets: page, NextToken: next}, nil
}

func (c *EC2) DescribeNatGateways(_ context.Context, in *ec2.DescribeNatGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	if err := c.begin("DescribeNatGateways"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filter); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []ec2types.NatGateway
	for _, n := range e.Network.NatGateways {
		if c.inScope(n.AccountID, n.Region) && matchesIDs(in.NatGatewayIds, aws.ToString(n.Nat.NatGatewayId)) &&
			matchesFilter(in.Filter, "vpc-id", aws.ToString(n.Nat.VpcId)) {
			all = append(all, deepCopy(n.Nat))
		}
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeNatGatewaysOutput{NatGateways: page, NextToken: next}, nil
}

func (c *EC2) DescribeInternetGateways(_ context.Context, in *ec2.DescribeInternetGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	if err := c.begin("DescribeInternetGateways"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filters); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []ec2types.InternetGateway
	for _, g := range e.Network.InternetGateways {
		if !c.inScope(g.AccountID, g.Region) || !matchesIDs(in.InternetGatewayIds, aws.ToString(g.Igw.InternetGatewayId)) {
			continue
		}
		if values, ok := filterValues(in.Filters, "attachment.vpc-id"); ok {
			attached := false
			for _, att := range g.Igw.Attachments {
				attached = attached || containsString(values, aws.ToString(att.VpcId))
			}
			if !attached {
				continue
			}
		}
		all = append(all, deepCopy(g.Igw))
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeInternetGatewaysOutput{InternetGateways: page, NextToken: next}, nil
}

func (c *EC2) DescribeVpcEndpoints(_ context.Context, in *ec2.DescribeVpcEndpointsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error) {
	if err := c.begin("DescribeVpcEndpoints"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filters); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []ec2types.VpcEndpoint
	for _, ep := range e.Network.Endpoints {
		if c.inScope(ep.AccountID, ep.Region) && matchesIDs(in.VpcEndpointIds, aws.ToString(ep.Endpoint.VpcEndpointId)) &&
			matchesFilter(in.Filters, "vpc-id", aws.ToString(ep.Endpoint.VpcId)) {
			all = append(all, deepCopy(ep.Endpoint))
		}
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeVpcEndpointsOutput{VpcEndpoints: page, NextToken: next}, nil
}

func (c *EC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	if err := c.begin("DescribeSecurityGroups"); err != nil {
		return nil, err
	}
	if err := checkFilterLimit(in.Filters); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var all []ec2types.SecurityGroup
	for _, g := range e.Network.SecurityGroups {
		id := aws.ToString(g.Group.GroupId)
		if c.inScope(g.AccountID, g.Region) && matchesIDs(in.GroupIds, id) &&
			matchesFilter(in.Filters, "group-id", id) && matchesFilter(in.Filters, "vpc-id", aws.ToString(g.Group.VpcId)) {
			all = append(all, deepCopy(g.Group))
		}
	}
	page, next := ec2Page(all, in.MaxResults, in.NextToken)
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: page, NextToken: next}, nil
}

// parsePageToken reads an offset token; garbage or negative values restart at 0.
func parsePageToken(token *string) int {
	n, err := strconv.Atoi(*token)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
