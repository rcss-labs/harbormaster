package logs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"golang.org/x/sync/singleflight"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// ErrorTermsPattern is a CloudWatch OR filter that matches a superset of the
// error-level lines; matches are re-checked with Level.
const ErrorTermsPattern = `?ERROR ?FATAL ?PANIC ?CRITICAL ?error ?fatal ?panic ?critical ?Error ?Fatal`

const (
	maxPagesPerChain   = 3
	maxCallsPerRequest = 40
	scanDeadline       = 10 * time.Second
	// callInterval paces FilterLogEvents to 2 calls/s per target: 40% of the lowest regional quota
	// (5/s in eu-central-1, not adjustable), leaving room for the Logs tab and other server tasks.
	callInterval     = 500 * time.Millisecond
	settleWindow     = 5 * time.Minute
	openBucketTTL    = 60 * time.Second
	closedBucketTTL  = time.Hour
	staleServeWindow = time.Hour
	maxCacheEntries  = 20000
	topMessageRunes  = 200
)

var errCallBudget = errors.New("error count call budget used up")

// variableRuns are the parts of a line that differ between occurrences of the same message.
var variableRuns = regexp.MustCompile(`0[xX][0-9a-fA-F]+|\b[0-9a-fA-F]{8,}\b|\d+`)

type ErrorCountRequest struct {
	Target        awsx.Target
	Group, Region string
	Prefixes      []string
	From, To      time.Time
	Bucket        time.Duration
}

// ErrorBucket is the error count of [Start, Start+Bucket). Count is meaningful only when Known.
// Capped means the page limit stopped the scan, so Count is a lower bound.
type ErrorBucket struct {
	Start         time.Time
	Count         int
	Capped, Known bool
	TopMessage    string
}

// ErrorCounts lists buckets oldest first. Partial means some bucket is unknown or was served after
// a failure; Stale means an expired cache entry was served. FetchedAt is the oldest fetch time of
// the known buckets.
type ErrorCounts struct {
	Buckets        []ErrorBucket
	Partial, Stale bool
	FetchedAt      time.Time
}

// ErrorCounter counts error lines per time bucket with FilterLogEvents. Counts are cached in
// process memory only: log text is never written to the database.
type ErrorCounter struct {
	Clients awsx.Clients
	Now     func() time.Time
	// Sleep waits for d or until ctx ends; nil waits on a real timer.
	Sleep func(ctx context.Context, d time.Duration) error

	scans singleflight.Group

	mu       sync.Mutex
	cache    map[bucketKey]cachedBucket
	nextSlot map[awsx.Target]time.Time
}

type bucketKey struct {
	target   awsx.Target
	group    string
	prefixes string
	start    int64 // unix ms
	size     time.Duration
}

type cachedBucket struct {
	bucket    ErrorBucket
	fetchedAt time.Time
	expiresAt time.Time
}

func NewErrorCounter(clients awsx.Clients, now func() time.Time) *ErrorCounter {
	if now == nil {
		now = time.Now
	}
	return &ErrorCounter{Clients: clients, Now: now, cache: map[bucketKey]cachedBucket{}, nextSlot: map[awsx.Target]time.Time{}}
}

// Count returns the error count of every bucket covering [From, To]. Concurrent identical requests
// share one scan. It fails only when no bucket is known.
func (c *ErrorCounter) Count(ctx context.Context, req ErrorCountRequest) (ErrorCounts, error) {
	if req.Region != "" {
		req.Target.Region = req.Region
	}
	if req.Bucket <= 0 {
		return ErrorCounts{}, errors.New("error count bucket must be positive")
	}
	prefixes := slices.Clone(req.Prefixes)
	if len(prefixes) == 0 {
		prefixes = []string{""}
	}
	slices.Sort(prefixes)
	starts := bucketStarts(req.From, req.To, req.Bucket)
	flightKey := fmt.Sprintf("%v|%s|%q|%d|%d|%d", req.Target, req.Group, prefixes, starts[0].UnixMilli(), starts[len(starts)-1].UnixMilli(), req.Bucket)

	// The scan runs detached so one viewer leaving does not fail the others sharing it.
	scanCtx := context.WithoutCancel(ctx)
	resultCh := c.scans.DoChan(flightKey, func() (any, error) {
		scanCtx, cancel := context.WithTimeout(scanCtx, scanDeadline)
		defer cancel()
		return c.scan(scanCtx, req.Target, req.Group, prefixes, starts, req.Bucket)
	})
	select {
	case <-ctx.Done():
		return ErrorCounts{}, ctx.Err()
	case res := <-resultCh:
		if res.Err != nil {
			return ErrorCounts{}, res.Err
		}
		counts := res.Val.(ErrorCounts)
		counts.Buckets = slices.Clone(counts.Buckets)
		return counts, nil
	}
}

// bucketStarts returns the starts of the buckets covering [from, to], aligned to multiples of size
// from the epoch, oldest first. A bucket starting exactly at to is not included.
func bucketStarts(from, to time.Time, size time.Duration) []time.Time {
	align := func(t time.Time) time.Time {
		ms := t.UnixMilli()
		step := size.Milliseconds()
		return time.UnixMilli(ms - ((ms%step)+step)%step).UTC()
	}
	first := align(from)
	last := first
	if to.After(from) {
		last = align(to.Add(-time.Millisecond))
	}
	var starts []time.Time
	for s := first; !s.After(last); s = s.Add(size) {
		starts = append(starts, s)
	}
	return starts
}

func (c *ErrorCounter) scan(ctx context.Context, target awsx.Target, group string, prefixes []string, starts []time.Time, size time.Duration) (ErrorCounts, error) {
	now := c.Now()
	prefixKey := strings.Join(prefixes, "\x00")
	buckets := make([]ErrorBucket, len(starts))
	fetchedAt := make([]time.Time, len(starts))
	for i, start := range starts {
		buckets[i] = ErrorBucket{Start: start}
	}

	// Fresh cache hits first; everything else needs a scan, newest first.
	var pending []int
	for i := len(starts) - 1; i >= 0; i-- {
		key := bucketKey{target, group, prefixKey, starts[i].UnixMilli(), size}
		if entry, ok := c.lookup(key, now); ok && now.Before(entry.expiresAt) {
			buckets[i], fetchedAt[i] = entry.bucket, entry.fetchedAt
			continue
		}
		pending = append(pending, i)
	}

	var scanErr error
	scanCut := false // budget or deadline ran out, as opposed to an AWS error
	if len(pending) > 0 {
		var api awsx.LogsAPI
		api, scanErr = c.Clients.Logs(ctx, target)
		calls := 0
		for _, i := range pending {
			if scanErr != nil {
				break
			}
			if calls+len(prefixes) > maxCallsPerRequest {
				scanCut = true
				break
			}
			bucket, err := c.scanBucket(ctx, api, target, group, prefixes, starts[i], size, &calls)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, errCallBudget) {
					scanCut = true
				} else {
					scanErr = err
				}
				break
			}
			fetched := c.Now()
			buckets[i], fetchedAt[i] = bucket, fetched
			c.store(bucketKey{target, group, prefixKey, starts[i].UnixMilli(), size}, bucket, fetched, size)
		}
	}

	result := ErrorCounts{Buckets: buckets}
	if scanErr != nil {
		// An AWS failure: serve expired entries instead of nothing.
		for _, i := range pending {
			if buckets[i].Known {
				continue
			}
			key := bucketKey{target, group, prefixKey, starts[i].UnixMilli(), size}
			if entry, ok := c.lookup(key, now); ok {
				buckets[i], fetchedAt[i] = entry.bucket, entry.fetchedAt
				result.Stale = true
			}
		}
		result.Partial = true
	}
	known := 0
	for i := range buckets {
		if !buckets[i].Known {
			result.Partial = true
			continue
		}
		known++
		if result.FetchedAt.IsZero() || fetchedAt[i].Before(result.FetchedAt) {
			result.FetchedAt = fetchedAt[i]
		}
	}
	if known == 0 {
		switch {
		case scanErr != nil:
			return ErrorCounts{}, scanErr
		case scanCut:
			return ErrorCounts{}, context.DeadlineExceeded
		}
	}
	return result, nil
}

// scanBucket counts one bucket across all prefixes. It fails if any call fails or the call budget
// or deadline runs out, so a half-counted bucket is never reported as known.
func (c *ErrorCounter) scanBucket(ctx context.Context, api awsx.LogsAPI, target awsx.Target, group string, prefixes []string, start time.Time, size time.Duration, calls *int) (ErrorBucket, error) {
	bucket := ErrorBucket{Start: start, Known: true}
	messageCounts := map[string]int{}
	samples := map[string]string{}
	for _, prefix := range prefixes {
		in := &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:  aws.String(group),
			StartTime:     aws.Int64(start.UnixMilli()),
			EndTime:       aws.Int64(start.Add(size).UnixMilli() - 1),
			FilterPattern: aws.String(ErrorTermsPattern),
		}
		if prefix != "" {
			in.LogStreamNamePrefix = aws.String(prefix)
		}
		for page := 1; ; page++ {
			if *calls >= maxCallsPerRequest {
				return ErrorBucket{}, errCallBudget
			}
			if err := c.pace(ctx, target); err != nil {
				return ErrorBucket{}, err
			}
			*calls++
			out, err := api.FilterLogEvents(ctx, in)
			if err != nil {
				return ErrorBucket{}, err
			}
			for _, ev := range out.Events {
				message := aws.ToString(ev.Message)
				if Level(message) != "error" {
					continue
				}
				bucket.Count++
				shape := variableRuns.ReplaceAllString(message, "#")
				if messageCounts[shape] == 0 {
					samples[shape] = message
				}
				messageCounts[shape]++
			}
			if out.NextToken == nil || *out.NextToken == "" {
				break
			}
			if page == maxPagesPerChain {
				bucket.Capped = true
				break
			}
			in.NextToken = out.NextToken
		}
	}
	bucket.TopMessage = truncateRunes(samples[mostFrequent(messageCounts)], topMessageRunes)
	return bucket, nil
}

// mostFrequent returns the key with the highest count; ties go to the smaller key so results are stable.
func mostFrequent(counts map[string]int) string {
	best, bestCount := "", 0
	for key, n := range counts {
		if n > bestCount || (n == bestCount && key < best) {
			best, bestCount = key, n
		}
	}
	return best
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// pace reserves the next call slot of the target, 500 ms after the previous one, and waits for it.
func (c *ErrorCounter) pace(ctx context.Context, target awsx.Target) error {
	c.mu.Lock()
	now := c.Now()
	slot := now
	if next := c.nextSlot[target]; next.After(now) {
		slot = next
	}
	c.nextSlot[target] = slot.Add(callInterval)
	c.mu.Unlock()
	wait := slot.Sub(now)
	if wait <= 0 {
		return ctx.Err()
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lookup returns the entry if it is fresh or within the stale window.
func (c *ErrorCounter) lookup(key bucketKey, now time.Time) (cachedBucket, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || !now.Before(entry.expiresAt.Add(staleServeWindow)) {
		return cachedBucket{}, false
	}
	return entry, true
}

func (c *ErrorCounter) store(key bucketKey, bucket ErrorBucket, fetchedAt time.Time, size time.Duration) {
	ttl := closedBucketTTL
	if fetchedAt.Sub(bucket.Start.Add(size)) < settleWindow {
		ttl = openBucketTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = cachedBucket{bucket: bucket, fetchedAt: fetchedAt, expiresAt: fetchedAt.Add(ttl)}
	if len(c.cache) > maxCacheEntries {
		c.evict(fetchedAt)
	}
}

// evict drops entries until a tenth of the capacity is free: unusable ones first, then expired ones,
// then the soonest to expire. Caller holds mu.
func (c *ErrorCounter) evict(now time.Time) {
	type candidate struct {
		key  bucketKey
		rank int // 0 beyond the stale window, 1 expired, 2 fresh
		at   time.Time
	}
	candidates := make([]candidate, 0, len(c.cache))
	for key, entry := range c.cache {
		rank := 2
		switch {
		case !now.Before(entry.expiresAt.Add(staleServeWindow)):
			rank = 0
		case !now.Before(entry.expiresAt):
			rank = 1
		}
		candidates = append(candidates, candidate{key, rank, entry.expiresAt})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return a.at.Compare(b.at)
	})
	for _, victim := range candidates[:len(c.cache)-maxCacheEntries*9/10] {
		delete(c.cache, victim.key)
	}
}
