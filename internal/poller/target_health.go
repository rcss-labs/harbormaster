package poller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	Store   readStore
	Writer  *store.SliceWriter
	Clients awsx.Clients
	Every   time.Duration
	Now     func() time.Time
	// OnChange is called once per Run after the rows are written, with whether the stored health changed (the
	// snapshot cache's generation token follows it). Nil means nobody is told.
	OnChange func(ctx context.Context, changed bool)
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

	// What changed is decided here, from the stored rows, and not by the upsert's SQL: the rows keep being rewritten
	// every cycle, because the target-health endpoint reports their polled_at as the time they were read.
	stored, err := l.Store.TargetHealthByService(ctx, store.TargetFilter{AccountID: t.AccountID, Region: t.Region}, "")
	if err != nil {
		return fmt.Errorf("stored target health: %w", err)
	}
	changed := sliceTargetHealthChanged(stored, rows, attached)
	if err := l.Writer.UpsertTargetHealth(ctx, rows); err != nil {
		return err
	}
	if err := l.Writer.DeleteTargetHealthNotIn(ctx, t, attached); err != nil {
		if changed && l.OnChange != nil {
			l.OnChange(ctx, true) // the upsert above is committed
		}
		return err
	}
	if l.OnChange != nil {
		l.OnChange(ctx, changed)
	}
	return nil
}

// sliceTargetHealthChanged reports whether writing fresh and then deleting every group not in attached changes what is
// stored: a row that is new or differs, or a stored row whose group is no longer attached to any service.
func sliceTargetHealthChanged(stored map[model.ServiceKey][]model.TargetHealth, fresh []model.TargetHealth, attached []string) bool {
	keep := make(map[string]bool, len(attached))
	for _, arn := range attached {
		keep[arn] = true
	}
	for _, rows := range stored {
		for _, row := range rows {
			if !keep[row.TargetGroupARN] {
				return true
			}
		}
	}
	for _, row := range fresh {
		if targetHealthDiffers(stored[row.Key], []model.TargetHealth{row}) {
			return true
		}
	}
	return false
}

// RunService stores the health of the target groups attached to one service, and nothing else: no other service is
// read from AWS and no row is deleted. changed is true when a stored row differs from what AWS reported.
func (l *TargetHealthLoop) RunService(ctx context.Context, key model.ServiceKey) (changed bool, err error) {
	t := awsx.Target{AccountID: key.AccountID, Region: key.Region}
	filter := store.TargetFilter{AccountID: t.AccountID, Region: t.Region}
	services, err := l.Store.ListServices(ctx, filter, key.Cluster, store.Visibility{Admin: true})
	if err != nil {
		return false, fmt.Errorf("list services: %w", err)
	}
	var groups []string
	seen := map[string]bool{}
	for _, svc := range services {
		if svc.Key != key {
			continue
		}
		for _, lb := range svc.LoadBalancers {
			if lb.TargetGroupARN != "" && !seen[lb.TargetGroupARN] {
				seen[lb.TargetGroupARN] = true
				groups = append(groups, lb.TargetGroupARN)
			}
		}
	}
	if len(groups) == 0 {
		return false, nil
	}

	stored, err := l.Store.TargetHealthByService(ctx, filter, key.Cluster)
	if err != nil {
		return false, fmt.Errorf("stored target health: %w", err)
	}
	// A group already stored has its load balancer on record; only unknown groups need DescribeTargetGroups, which
	// keeps a focus cycle to the reads the design lists.
	loadBalancers := map[string]string{}
	for _, row := range stored[key] {
		if row.LoadBalancerARN != "" {
			loadBalancers[row.TargetGroupARN] = row.LoadBalancerARN
		}
	}
	var unknown []string
	for _, arn := range groups {
		if loadBalancers[arn] == "" {
			unknown = append(unknown, arn)
		}
	}
	api, err := l.Clients.ELB(ctx, t)
	if err != nil {
		return false, err
	}
	if len(unknown) > 0 {
		found, err := describeTargetGroupLoadBalancers(ctx, api, unknown)
		if err != nil {
			return false, err
		}
		for arn, lb := range found {
			loadBalancers[arn] = lb
		}
	}
	now := observeNow(l.Now)
	var rows []model.TargetHealth
	for _, arn := range groups {
		loadBalancerARN, exists := loadBalancers[arn]
		if !exists {
			continue
		}
		out, err := api.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(arn)})
		if errors.As(err, new(*elbtypes.TargetGroupNotFoundException)) {
			continue // the group is gone; the slice-wide tier owns deleting its row
		}
		if err != nil {
			return false, fmt.Errorf("describe target health: %w", err)
		}
		rows = append(rows, normalizeTargetHealth(key, arn, loadBalancerARN, out, now))
	}

	changed = targetHealthDiffers(stored[key], rows)
	if err := l.Writer.UpsertTargetHealth(ctx, rows); err != nil {
		return false, err
	}
	return changed, nil
}

// targetHealthDiffers compares what is stored with what AWS just reported, ignoring poll time and raw bodies.
func targetHealthDiffers(stored, fresh []model.TargetHealth) bool {
	byGroup := map[string]model.TargetHealth{}
	for _, row := range stored {
		byGroup[row.TargetGroupARN] = row
	}
	for _, row := range fresh {
		old, ok := byGroup[row.TargetGroupARN]
		if !ok || old.LoadBalancerARN != row.LoadBalancerARN || old.Healthy != row.Healthy ||
			old.Unhealthy != row.Unhealthy || !sameTargets(old.Targets, row.Targets) {
			return true
		}
	}
	return false
}

// sameTargets compares two target lists as sets: AWS does not promise an order, and a list that only came back in
// another order must not invalidate the cached models every cycle.
func sameTargets(a, b []model.Target) bool {
	if len(a) != len(b) {
		return false
	}
	order := func(x, y model.Target) int {
		return cmp.Or(cmp.Compare(x.ID, y.ID), cmp.Compare(x.Port, y.Port), cmp.Compare(x.AZ, y.AZ),
			cmp.Compare(x.State, y.State), cmp.Compare(x.Reason, y.Reason), cmp.Compare(x.Description, y.Description))
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, order)
	slices.SortFunc(b, order)
	return slices.Equal(a, b)
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
