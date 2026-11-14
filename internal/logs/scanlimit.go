package logs

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// BytesPerGiB is the unit Logs Insights bills in (GB = 2^30 bytes).
const BytesPerGiB = 1 << 30

// scanBudget tracks the bytes every batch of one search has scanned so far and says when the total
// passes the cap. A cap of 0 means no limit.
type scanBudget struct {
	max int64
	mu  sync.Mutex
	per []int64 // latest BytesScanned of each batch
}

func newScanBudget(max int64, batches int) *scanBudget {
	return &scanBudget{max: max, per: make([]int64, batches)}
}

// record stores the latest BytesScanned of batch i and reports whether the search as a whole is over the cap.
func (b *scanBudget) record(i int, scanned int64) bool {
	if b == nil || b.max <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.per[i] = scanned
	var total int64
	for _, n := range b.per {
		total += n
	}
	return total > b.max
}

// scanLimitMessage is what a target shows when its query was stopped at the cap.
func scanLimitMessage(max int64) string {
	return fmt.Sprintf("Stopped at the %s GB scan limit; the results are partial.", FormatGiB(max))
}

// FormatGiB renders a byte count as GiB without trailing zeros ("10", "0.5").
func FormatGiB(bytes int64) string {
	return strconv.FormatFloat(float64(bytes)/BytesPerGiB, 'f', -1, 64)
}

// Estimate is an upper bound on what a search may scan: the stored size of every log group it would query.
// StoredBytes covers the whole group, not the time range, so the real scan is usually far smaller.
type Estimate struct {
	StoredBytes   int64
	Groups        int // distinct log groups the search would query
	UnknownGroups int // groups whose size could not be read (no permission, missing, API error)
}

// Estimate reads the stored size of the log groups req would query, once per group. It starts no query and
// bills nothing. A target that cannot be read leaves its groups unknown; it is not an error.
func (s *Searcher) Estimate(ctx context.Context, req SearchRequest) (Estimate, error) {
	targets, batches, _, _ := planSearch(req.Services)
	groupsByTarget := map[awsx.Target][]string{}
	for _, b := range batches {
		groupsByTarget[b.target] = append(groupsByTarget[b.target], b.groups...)
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		est Estimate
	)
	for _, t := range targets {
		groups := groupsByTarget[t]
		wg.Add(1)
		go func() {
			defer wg.Done()
			bytes, unknown := s.storedBytes(ctx, t, groups)
			mu.Lock()
			defer mu.Unlock()
			est.StoredBytes += bytes
			est.Groups += len(groups)
			est.UnknownGroups += unknown
		}()
	}
	wg.Wait()
	return est, ctx.Err()
}

// storedBytes sums the stored size of groups in one target and counts the groups it could not read.
func (s *Searcher) storedBytes(ctx context.Context, t awsx.Target, groups []string) (total int64, unknown int) {
	api, err := s.Clients.Logs(ctx, t)
	if err != nil {
		slog.Warn("log search estimate: logs client", "account", t.AccountID, "region", t.Region, "err", err)
		return 0, len(groups)
	}
	for _, group := range groups {
		size, ok, err := describeGroupSize(ctx, api, group)
		if err != nil {
			slog.Warn("log search estimate: describe log groups", "account", t.AccountID, "region", t.Region, "err", err)
		}
		if !ok {
			unknown++
			continue
		}
		total += size
	}
	return total, unknown
}

// describeGroupSize finds group by exact name among the groups that share its prefix.
func describeGroupSize(ctx context.Context, api awsx.LogsAPI, group string) (int64, bool, error) {
	in := &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(group), Limit: aws.Int32(50)}
	for {
		out, err := api.DescribeLogGroups(ctx, in)
		if err != nil {
			return 0, false, err
		}
		for _, g := range out.LogGroups {
			if aws.ToString(g.LogGroupName) == group {
				return aws.ToInt64(g.StoredBytes), true, nil
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return 0, false, nil
		}
		in.NextToken = out.NextToken
	}
}
