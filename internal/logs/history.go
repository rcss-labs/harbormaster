package logs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	defaultHistoryLimit = 500
	firstWindow         = 15 * time.Minute
	windowGrowth        = 4
)

// Line is one log event.
type Line struct {
	T                                  time.Time
	Container, TaskID, Stream, Message string
}

// HistoryRequest asks for the newest lines at or before Before.
type HistoryRequest struct {
	Group, Region string
	Prefixes      []string  // stream name prefixes; empty means every stream
	Before        time.Time // zero means now
	Query         string    // free text, escaped by FilterPattern
	Limit         int       // default 500
}

// HistoryPage is one page of history, oldest line first.
type HistoryPage struct {
	Lines []Line
	// OlderBefore is the Before value that continues the history; nil when the
	// lookback floor was reached and nothing older remains.
	OlderBefore *time.Time
	// Truncated is true when more than Limit lines matched and the oldest were dropped.
	Truncated bool
}

// Client reads CloudWatch Logs history.
type Client struct {
	Clients     awsx.Clients
	Now         func() time.Time
	MaxLookback time.Duration // default 7 days
}

// History collects the newest req.Limit lines at or before req.Before by
// querying ever wider, non-overlapping time windows (15 min, then x4 each
// step, finally up to the lookback floor) until enough lines are found.
func (c *Client) History(ctx context.Context, t awsx.Target, req HistoryRequest) (HistoryPage, error) {
	api, err := c.Clients.Logs(ctx, t)
	if err != nil {
		return HistoryPage{}, err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	lookback := c.MaxLookback
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	before := req.Before
	if before.IsZero() {
		before = now
	}
	floor := now.Add(-lookback)
	pattern := FilterPattern(req.Query)
	prefixes := req.Prefixes
	if len(prefixes) == 0 {
		prefixes = []string{""}
	}

	var lines []Line
	windowEnd := before
	reach := firstWindow
	floorReached := !windowEnd.After(floor)
	for len(lines) < limit && !floorReached {
		windowStart := before.Add(-reach)
		if !windowStart.After(floor) {
			windowStart = floor
			floorReached = true
		}
		if windowStart.Before(windowEnd) || windowStart.Equal(windowEnd) {
			for _, prefix := range prefixes {
				found, err := filterWindow(ctx, api, req.Group, prefix, pattern, windowStart, windowEnd)
				if err != nil {
					return HistoryPage{}, err
				}
				lines = append(lines, found...)
			}
		}
		windowEnd = windowStart.Add(-time.Millisecond)
		reach *= windowGrowth
	}

	slices.SortStableFunc(lines, func(a, b Line) int {
		if cmp := a.T.Compare(b.T); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Stream, b.Stream)
	})
	page := HistoryPage{Lines: lines}
	if len(lines) > limit {
		page.Truncated = true
		page.Lines = lines[len(lines)-limit:]
	}
	if len(page.Lines) > 0 && (!floorReached || page.Truncated) {
		older := page.Lines[0].T.Add(-time.Millisecond)
		page.OlderBefore = &older
	}
	return page, nil
}

// filterWindow runs FilterLogEvents for one prefix over [start, end] through all pages.
func filterWindow(ctx context.Context, api awsx.LogsAPI, group, prefix, pattern string, start, end time.Time) ([]Line, error) {
	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(group),
		StartTime:    aws.Int64(start.UnixMilli()),
		EndTime:      aws.Int64(end.UnixMilli()),
	}
	if prefix != "" {
		in.LogStreamNamePrefix = aws.String(prefix)
	}
	if pattern != "" {
		in.FilterPattern = aws.String(pattern)
	}
	var lines []Line
	for {
		out, err := api.FilterLogEvents(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("filter log events in %s: %w", group, err)
		}
		for _, ev := range out.Events {
			stream := aws.ToString(ev.LogStreamName)
			container, taskID := ParseStream(stream)
			lines = append(lines, Line{
				T: time.UnixMilli(aws.ToInt64(ev.Timestamp)).UTC(), Container: container, TaskID: taskID,
				Stream: stream, Message: aws.ToString(ev.Message),
			})
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return lines, nil
		}
		in.NextToken = out.NextToken
	}
}
