package fake

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// storedBytesWindow is how far back the fake sums events to report a group's storedBytes.
const storedBytesWindow = 30 * 24 * time.Hour

// DescribeLogGroups lists the target's known log groups (explicit lines, stored-bytes overrides and the
// generated "/ecs/<cluster>/<service>" groups) whose name starts with LogGroupNamePrefix, in one page.
func (c *logsClient) DescribeLogGroups(_ context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "DescribeLogGroups", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	names := map[string]bool{}
	for name := range e.LogLines {
		names[name] = true
	}
	for name := range e.LogGroupBytes {
		names[name] = true
	}
	for _, svc := range e.Services {
		if svc.AccountID == c.target.AccountID && svc.Region == c.target.Region {
			names["/ecs/"+svc.ClusterName+"/"+aws.ToString(svc.Service.ServiceName)] = true
		}
	}
	prefix := aws.ToString(in.LogGroupNamePrefix)
	var sorted []string
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			sorted = append(sorted, name)
		}
	}
	slices.Sort(sorted)
	out := &cloudwatchlogs.DescribeLogGroupsOutput{}
	for _, name := range sorted {
		stored, ok := e.LogGroupBytes[name]
		if !ok {
			events, evErr := c.eventsIn(name, e.now().Add(-storedBytesWindow), e.now())
			if evErr != nil {
				continue
			}
			for _, ev := range events {
				stored += int64(len(ev.message) + insightsLineOverhead)
			}
		}
		out.LogGroups = append(out.LogGroups, logstypes.LogGroup{LogGroupName: aws.String(name), StoredBytes: aws.Int64(stored)})
	}
	return out, nil
}
