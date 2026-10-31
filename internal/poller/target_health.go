package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// targetGroupDescribeLimit is the most target group ARNs one DescribeTargetGroups call accepts.
const targetGroupDescribeLimit = 20

// TargetHealthLoop is the 15 s tier that stores each attached target group's target health.
type TargetHealthLoop struct {
	Store   *store.Store
	Clients awsx.Clients
	Every   time.Duration
	Now     func() time.Time
}

func (l *TargetHealthLoop) Name() string            { return tierTargetHealth }
func (l *TargetHealthLoop) Interval() time.Duration { return l.Every }

// Run stores the health of every target group attached to a service of the target. Target
// groups that no longer exist are skipped; their stored rows stay until the service detaches them.
func (l *TargetHealthLoop) Run(ctx context.Context, t awsx.Target) error {
	services, err := l.Store.ListServices(ctx, store.TargetFilter{AccountID: t.AccountID, Region: t.Region}, "", store.Visibility{Admin: true})
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}

	type attachment struct {
		key      model.ServiceKey
		groupARN string
	}
	var attachments []attachment
	var attached []string
	seen := map[string]bool{}
	for _, svc := range services {
		for _, lb := range svc.LoadBalancers {
			if lb.TargetGroupARN == "" {
				continue
			}
			attachments = append(attachments, attachment{key: svc.Key, groupARN: lb.TargetGroupARN})
			if !seen[lb.TargetGroupARN] {
				seen[lb.TargetGroupARN] = true
				attached = append(attached, lb.TargetGroupARN)
			}
		}
	}

	api, err := l.Clients.ELB(ctx, t)
	if err != nil {
		return err
	}
	loadBalancers, err := describeTargetGroupLoadBalancers(ctx, api, attached)
	if err != nil {
		return err
	}

	now := observeNow(l.Now)
	var rows []model.TargetHealth
	for _, a := range attachments {
		loadBalancerARN, exists := loadBalancers[a.groupARN]
		if !exists {
			continue
		}
		out, err := api.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(a.groupARN)})
		if err != nil {
			return fmt.Errorf("describe target health: %w", err)
		}
		rows = append(rows, normalizeTargetHealth(a.key, a.groupARN, loadBalancerARN, out, now))
	}

	if err := l.Store.UpsertTargetHealth(ctx, rows); err != nil {
		return err
	}
	return l.Store.DeleteTargetHealthNotIn(ctx, t, attached)
}

// describeTargetGroupLoadBalancers maps each existing target group ARN to its load balancer ARN.
// A batch that names a missing group fails as a whole, so that batch is retried one ARN at a time.
func describeTargetGroupLoadBalancers(ctx context.Context, api awsx.ELBAPI, arns []string) (map[string]string, error) {
	loadBalancers := map[string]string{}
	for start := 0; start < len(arns); start += targetGroupDescribeLimit {
		batch := arns[start:min(start+targetGroupDescribeLimit, len(arns))]
		out, err := api.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: batch})
		if errors.As(err, new(*elbtypes.TargetGroupNotFoundException)) {
			for _, arn := range batch {
				found, err := describeOneTargetGroup(ctx, api, arn)
				if err != nil {
					return nil, err
				}
				if found == "" {
					continue
				}
				loadBalancers[arn] = found
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("describe target groups: %w", err)
		}
		for _, group := range out.TargetGroups {
			if len(group.LoadBalancerArns) > 0 {
				loadBalancers[aws.ToString(group.TargetGroupArn)] = group.LoadBalancerArns[0]
			}
		}
	}
	return loadBalancers, nil
}

// describeOneTargetGroup returns the group's load balancer ARN, or "" when the group no longer exists.
func describeOneTargetGroup(ctx context.Context, api awsx.ELBAPI, arn string) (string, error) {
	out, err := api.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: []string{arn}})
	if errors.As(err, new(*elbtypes.TargetGroupNotFoundException)) {
		slog.Debug("poller: target group not found, skipping", "arn", arn)
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("describe target group: %w", err)
	}
	for _, group := range out.TargetGroups {
		if len(group.LoadBalancerArns) > 0 {
			return group.LoadBalancerArns[0], nil
		}
	}
	return "", nil
}

func normalizeTargetHealth(key model.ServiceKey, groupARN, loadBalancerARN string, out *elasticloadbalancingv2.DescribeTargetHealthOutput, now time.Time) model.TargetHealth {
	row := model.TargetHealth{Key: key, TargetGroupARN: groupARN, LoadBalancerARN: loadBalancerARN, PolledAt: now, Raw: rawJSON(out)}
	for _, description := range out.TargetHealthDescriptions {
		target := model.Target{}
		if description.Target != nil {
			target.ID = aws.ToString(description.Target.Id)
			target.Port = int(aws.ToInt32(description.Target.Port))
			target.AZ = aws.ToString(description.Target.AvailabilityZone)
		}
		if description.TargetHealth != nil {
			target.State = strings.ToLower(string(description.TargetHealth.State))
			target.Reason = string(description.TargetHealth.Reason)
			target.Description = aws.ToString(description.TargetHealth.Description)
		}
		switch target.State {
		case "healthy":
			row.Healthy++
		case "unhealthy":
			row.Unhealthy++
		}
		row.Targets = append(row.Targets, target)
	}
	return row
}

// observeNow returns the configured clock's time, or the current UTC time when no clock is set.
func observeNow(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now().UTC()
}
