package fake

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// elbClient is the fake ELBv2 API scoped to one account and region. It
// answers from Estate.TargetGroups and Estate.ExtraTargetGroups.
type elbClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.ELBAPI = (*elbClient)(nil)

func targetGroupName(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return arn
}

func (c *elbClient) DescribeTargetGroups(_ context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	done, err := c.f.beginThrottled(c.target, "elasticloadbalancing", "DescribeTargetGroups", &c.f.ThrottleELB)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var inScope []targetGroupEntry
	for _, entry := range e.allTargetGroups() {
		if entry.key.AccountID == c.target.AccountID && entry.key.Region == c.target.Region {
			inScope = append(inScope, entry)
		}
	}
	out := &elasticloadbalancingv2.DescribeTargetGroupsOutput{}
	describe := func(entry targetGroupEntry) elbtypes.TargetGroup {
		port := int32(8080)
		for _, svc := range e.Services {
			if svc.AccountID == entry.key.AccountID && svc.Region == entry.key.Region && svc.ClusterName == entry.key.Cluster &&
				aws.ToString(svc.Service.ServiceName) == entry.key.Service {
				for _, lb := range svc.Service.LoadBalancers {
					if aws.ToString(lb.TargetGroupArn) == entry.group.ARN {
						port = aws.ToInt32(lb.ContainerPort)
					}
				}
			}
		}
		return elbtypes.TargetGroup{
			TargetGroupArn:   aws.String(entry.group.ARN),
			TargetGroupName:  aws.String(targetGroupName(entry.group.ARN)),
			LoadBalancerArns: []string{entry.group.LoadBalancerARN},
			Protocol:         elbtypes.ProtocolEnumHttp,
			Port:             aws.Int32(port),
			TargetType:       elbtypes.TargetTypeEnumIp,
			HealthCheckPath:  aws.String("/health"),
		}
	}
	switch {
	case len(in.TargetGroupArns) > 0:
		for _, arn := range in.TargetGroupArns {
			found := false
			for _, entry := range inScope {
				if entry.group.ARN == arn {
					out.TargetGroups = append(out.TargetGroups, describe(entry))
					found = true
				}
			}
			if !found {
				return nil, &elbtypes.TargetGroupNotFoundException{Message: aws.String("One or more target groups not found")}
			}
		}
	case len(in.Names) > 0:
		for _, name := range in.Names {
			found := false
			for _, entry := range inScope {
				if targetGroupName(entry.group.ARN) == name {
					out.TargetGroups = append(out.TargetGroups, describe(entry))
					found = true
				}
			}
			if !found {
				return nil, &elbtypes.TargetGroupNotFoundException{Message: aws.String("One or more target groups not found")}
			}
		}
	default:
		for _, entry := range inScope {
			if in.LoadBalancerArn == nil || entry.group.LoadBalancerARN == *in.LoadBalancerArn {
				out.TargetGroups = append(out.TargetGroups, describe(entry))
			}
		}
	}
	return out, nil
}

// DescribeTargetHealth lists one target per running task of the group's
// service (by private IP); the first Unhealthy targets fail their health check.
func (c *elbClient) DescribeTargetHealth(_ context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	done, err := c.f.beginThrottled(c.target, "elasticloadbalancing", "DescribeTargetHealth", &c.f.ThrottleELB)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var found *targetGroupEntry
	for _, entry := range e.allTargetGroups() {
		if entry.key.AccountID == c.target.AccountID && entry.key.Region == c.target.Region && entry.group.ARN == aws.ToString(in.TargetGroupArn) {
			entry := entry
			found = &entry
		}
	}
	if found == nil {
		return nil, &elbtypes.TargetGroupNotFoundException{Message: aws.String("Target group '" + aws.ToString(in.TargetGroupArn) + "' not found")}
	}

	port := int32(8080)
	for _, svc := range e.Services {
		if svc.AccountID == found.key.AccountID && svc.Region == found.key.Region && svc.ClusterName == found.key.Cluster &&
			aws.ToString(svc.Service.ServiceName) == found.key.Service {
			for _, lb := range svc.Service.LoadBalancers {
				if aws.ToString(lb.TargetGroupArn) == found.group.ARN {
					port = aws.ToInt32(lb.ContainerPort)
				}
			}
		}
	}
	var ips []string
	for _, t := range e.Tasks {
		if t.AccountID == found.key.AccountID && t.Region == found.key.Region && t.ClusterName == found.key.Cluster &&
			aws.ToString(t.Task.Group) == "service:"+found.key.Service && aws.ToString(t.Task.LastStatus) == "RUNNING" {
			ips = append(ips, taskPrivateIP(t.Task.Attachments))
		}
	}
	total := found.group.Healthy + found.group.Unhealthy
	for len(ips) < total { // counts set by tests may exceed the running tasks
		ips = append(ips, "10.99.0."+itoa(len(ips)+1))
	}
	ips = ips[:total]

	out := &elasticloadbalancingv2.DescribeTargetHealthOutput{}
	for i, ip := range ips {
		health := &elbtypes.TargetHealth{State: elbtypes.TargetHealthStateEnumHealthy}
		if i < found.group.Unhealthy {
			health = &elbtypes.TargetHealth{
				State:       elbtypes.TargetHealthStateEnumUnhealthy,
				Reason:      elbtypes.TargetHealthReasonEnumResponseCodeMismatch,
				Description: aws.String("Health checks failed with these codes: [503]"),
			}
		}
		if len(in.Targets) > 0 && !targetRequested(in.Targets, ip) {
			continue
		}
		out.TargetHealthDescriptions = append(out.TargetHealthDescriptions, elbtypes.TargetHealthDescription{
			Target:          &elbtypes.TargetDescription{Id: aws.String(ip), Port: aws.Int32(port)},
			HealthCheckPort: aws.String(itoa(int(port))),
			TargetHealth:    health,
		})
	}
	return out, nil
}

func targetRequested(targets []elbtypes.TargetDescription, id string) bool {
	for _, t := range targets {
		if aws.ToString(t.Id) == id {
			return true
		}
	}
	return false
}
