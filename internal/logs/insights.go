package logs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	// maxInsightsGroups is the most log groups one Logs Insights query accepts.
	maxInsightsGroups = 50
	// maxInFlightBatches bounds the queries one Search runs at once.
	maxInFlightBatches = 4
	// maxMessageBytes is the longest message returned per row.
	maxMessageBytes = 4 << 10
	// stopQueryTimeout bounds the cleanup StopQuery calls after a deadline or cancel.
	stopQueryTimeout = 5 * time.Second

	defaultMaxRows   = 10000
	defaultTimeout   = 60 * time.Second
	defaultPollEvery = time.Second

	insightsTimestampLayout = "2006-01-02 15:04:05.000"
)

// SearchService is one selected service and where its containers log.
type SearchService struct {
	ID                          string // model.ServiceKey.ID()
	AccountID, LogRegion, Group string
	Prefixes                    []string // StreamPrefix of every awslogs container in Group
}

type SearchRequest struct {
	Services []SearchService
	Query    string
	From, To time.Time
	MaxRows  int // default 10000
}

type SearchRow struct {
	T                                     time.Time
	ServiceID, Container, TaskID, Message string
	MessageTruncated                      bool
}

type TargetState string

const (
	StateOK           TargetState = "ok"
	StateNoPermission TargetState = "no_permission"
	StateThrottled    TargetState = "throttled"
	StateTimeout      TargetState = "timeout"
	StateCancelled    TargetState = "cancelled"
	StateError        TargetState = "error"
)

// TargetResult is the outcome for one account and log region.
type TargetResult struct {
	Target                                       awsx.Target
	ServiceIDs                                   []string
	State                                        TargetState
	Message                                      string
	RecordsMatched, RecordsScanned, BytesScanned int64
}

type SearchResult struct {
	Rows           []SearchRow
	RecordsMatched int64
	BytesScanned   int64
	Capped         bool
	Targets        []TargetResult
	Duration       time.Duration
}

// Searcher runs one Logs Insights search across accounts and log regions.
type Searcher struct {
	Clients   awsx.Clients
	Now       func() time.Time
	Timeout   time.Duration                              // default 60 s
	PollEvery time.Duration                              // default 1 s
	Sleep     func(context.Context, time.Duration) error // injectable; default waits on a timer or ctx
}

// batch is one StartQuery: at most 50 groups of one target.
type batch struct {
	target awsx.Target
	groups []string
	scopes []StreamScope
}

// batchOutcome is what one batch contributes to its target.
type batchOutcome struct {
	state          TargetState
	message        string
	rows           []map[string]string
	matched        int64
	scanned, bytes int64
	startErr       error // set when the query could not be started because of Logs access to the target
}

// targetIndex maps (group, stream) back to the selected service with the longest matching prefix.
type targetIndex map[string][]prefixOwner

type prefixOwner struct{ prefix, serviceID string }

func (s *Searcher) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	timeout, pollEvery, sleep := s.Timeout, s.PollEvery, s.Sleep
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if pollEvery <= 0 {
		pollEvery = defaultPollEvery
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	maxRows := req.MaxRows
	if maxRows <= 0 || maxRows > defaultMaxRows { // Insights returns at most 10,000 rows
		maxRows = defaultMaxRows
	}
	started := now()

	targets, batches, indexes, serviceIDs := planSearch(req.Services)
	var result SearchResult
	if len(batches) == 0 {
		return result, nil
	}
	// Compose every batch up front so a bad query fails the whole search before any AWS call.
	queries := make([]string, len(batches))
	for i, b := range batches {
		q, err := ComposeQuery(req.Query, b.scopes, maxRows)
		if err != nil {
			return result, err
		}
		queries[i] = q
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outcomes := make([]batchOutcome, len(batches))
	var (
		wg       sync.WaitGroup
		queryErr error
		errMu    sync.Mutex
		slots    = make(chan struct{}, maxInFlightBatches)
	)
	for i := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-runCtx.Done():
			}
			run := &batchRun{
				s: s, parent: ctx, ctx: runCtx, b: batches[i], query: queries[i],
				from: req.From, to: req.To, maxRows: maxRows, pollEvery: pollEvery, sleep: sleep,
			}
			outcomes[i] = run.do()
			var qe *QueryError
			if errors.As(outcomes[i].startErr, &qe) {
				errMu.Lock()
				if queryErr == nil {
					queryErr = qe
				}
				errMu.Unlock()
				cancel() // the whole search is invalid; stop the other batches
			}
		}()
	}
	wg.Wait()
	if queryErr != nil {
		return result, queryErr
	}

	byTarget := map[awsx.Target]*TargetResult{}
	for _, t := range targets {
		byTarget[t] = &TargetResult{Target: t, ServiceIDs: serviceIDs[t], State: StateOK}
	}
	clientFailures := 0
	var clientErr error
	for i, out := range outcomes {
		tr := byTarget[batches[i].target]
		if tr.State == StateOK && out.state != StateOK {
			tr.State, tr.Message = out.state, out.message
		}
		tr.RecordsMatched += out.matched
		tr.RecordsScanned += out.scanned
		tr.BytesScanned += out.bytes
		if out.startErr != nil {
			clientFailures++
			clientErr = errors.Join(clientErr, out.startErr)
		}
		for _, raw := range out.rows {
			if row, ok := mapRow(raw, batches[i].target.AccountID, indexes[batches[i].target]); ok {
				result.Rows = append(result.Rows, row)
			}
		}
	}
	if clientFailures == len(batches) {
		return result, fmt.Errorf("log search: no target could start: %w", clientErr)
	}
	for _, t := range targets {
		tr := *byTarget[t]
		result.Targets = append(result.Targets, tr)
		result.RecordsMatched += tr.RecordsMatched
		result.BytesScanned += tr.BytesScanned
	}
	slices.SortStableFunc(result.Rows, func(a, b SearchRow) int {
		if c := b.T.Compare(a.T); c != 0 {
			return c
		}
		if c := strings.Compare(a.ServiceID, b.ServiceID); c != 0 {
			return c
		}
		return strings.Compare(a.Message, b.Message)
	})
	if len(result.Rows) > maxRows {
		result.Rows = result.Rows[:maxRows]
	}
	result.Capped = result.RecordsMatched > int64(len(result.Rows))
	result.Duration = now().Sub(started)
	return result, nil
}

// planSearch groups services by target, dedupes groups, and chunks them into batches of at most 50 groups.
func planSearch(services []SearchService) ([]awsx.Target, []batch, map[awsx.Target]targetIndex, map[awsx.Target][]string) {
	byTarget := map[awsx.Target][]SearchService{}
	for _, svc := range services {
		t := awsx.Target{AccountID: svc.AccountID, Region: svc.LogRegion}
		byTarget[t] = append(byTarget[t], svc)
	}
	targets := make([]awsx.Target, 0, len(byTarget))
	for t := range byTarget {
		targets = append(targets, t)
	}
	slices.SortFunc(targets, func(a, b awsx.Target) int {
		return strings.Compare(a.AccountID+"/"+a.Region, b.AccountID+"/"+b.Region)
	})

	var batches []batch
	indexes := map[awsx.Target]targetIndex{}
	serviceIDs := map[awsx.Target][]string{}
	for _, t := range targets {
		svcs := byTarget[t]
		slices.SortStableFunc(svcs, func(a, b SearchService) int { return strings.Compare(a.ID, b.ID) })
		idx := targetIndex{}
		prefixesByGroup := map[string][]string{}
		var groups []string
		for _, svc := range svcs {
			if len(svc.Prefixes) == 0 {
				// Fail closed: no prefixes would mean "every stream" of the group. The API layer
				// guarantees non-empty prefixes (every selected service has an awslogs container).
				continue
			}
			serviceIDs[t] = append(serviceIDs[t], svc.ID)
			if _, seen := prefixesByGroup[svc.Group]; !seen {
				groups = append(groups, svc.Group)
				prefixesByGroup[svc.Group] = nil
			}
			for _, p := range svc.Prefixes {
				if !slices.Contains(prefixesByGroup[svc.Group], p) {
					prefixesByGroup[svc.Group] = append(prefixesByGroup[svc.Group], p)
				}
				idx[svc.Group] = append(idx[svc.Group], prefixOwner{prefix: p, serviceID: svc.ID})
			}
		}
		indexes[t] = idx
		for chunk := range slices.Chunk(groups, maxInsightsGroups) {
			b := batch{target: t, groups: chunk}
			for _, g := range chunk {
				b.scopes = append(b.scopes, StreamScope{AccountID: t.AccountID, Group: g, Prefixes: prefixesByGroup[g]})
			}
			batches = append(batches, b)
		}
	}
	return targets, batches, indexes, serviceIDs
}

// batchRun executes one batch: start, poll, and stop on deadline or cancel.
type batchRun struct {
	s         *Searcher
	parent    context.Context // the caller's context; StopQuery detaches from it
	ctx       context.Context // parent plus the search deadline
	b         batch
	query     string
	from, to  time.Time
	maxRows   int
	pollEvery time.Duration
	sleep     func(context.Context, time.Duration) error
}

func (r *batchRun) do() batchOutcome {
	if r.ctx.Err() != nil { // the deadline or a cancel arrived while waiting for a slot
		return r.interrupted(batchOutcome{}, nil)
	}
	api, err := r.s.Clients.Logs(r.ctx, r.b.target)
	if err != nil {
		slog.Warn("log search: logs client", "account", r.b.target.AccountID, "region", r.b.target.Region, "err", err)
		out := classifyFailure(err, r.b.target)
		out.startErr = err
		return out
	}
	start, err := api.StartQuery(r.ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: r.b.groups,
		StartTime:     aws.Int64(r.from.Unix()),
		EndTime:       aws.Int64(ceilUnix(r.to)),
		QueryString:   aws.String(r.query),
		Limit:         aws.Int32(int32(r.maxRows)),
	})
	if err != nil {
		if r.ctx.Err() != nil {
			return r.interrupted(batchOutcome{}, nil)
		}
		return r.failure(err)
	}
	queryID := aws.ToString(start.QueryId)

	var last batchOutcome
	for {
		if r.ctx.Err() != nil {
			return r.interrupted(last, api, queryID)
		}
		res, err := api.GetQueryResults(r.ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(queryID)})
		if err != nil {
			if r.ctx.Err() != nil {
				return r.interrupted(last, api, queryID)
			}
			r.stop(api, queryID)
			out := r.failure(err)
			return out
		}
		last = outcomeOf(res)
		switch res.Status {
		case logstypes.QueryStatusComplete:
			last.state = StateOK
			return last
		case logstypes.QueryStatusFailed:
			last.state, last.message = StateError, "Logs Insights returned an error."
			slog.Warn("log search: query failed", "account", r.b.target.AccountID, "region", r.b.target.Region, "query_id", queryID)
			return last
		case logstypes.QueryStatusCancelled:
			last.state, last.message = StateCancelled, "The query was cancelled."
			return last
		case logstypes.QueryStatusTimeout:
			last.state, last.message = StateTimeout, "The query timed out in Logs Insights."
			return last
		}
		if err := r.sleep(r.ctx, r.pollEvery); err != nil {
			return r.interrupted(last, api, queryID)
		}
	}
}

// interrupted stops the unfinished queries and reports timeout or cancelled, keeping rows already seen.
// It reuses the client do() already holds: building a new one with the finished context can fail
// (credential retrieval), which would silently leave the query running and billing.
func (r *batchRun) interrupted(last batchOutcome, api awsx.LogsAPI, queryIDs ...string) batchOutcome {
	if api != nil {
		for _, id := range queryIDs {
			r.stop(api, id)
		}
	}
	if r.parent.Err() != nil || errors.Is(r.ctx.Err(), context.Canceled) {
		last.state, last.message = StateCancelled, "The search was cancelled."
	} else {
		last.state, last.message = StateTimeout, "The search timed out before this account finished."
	}
	return last
}

// stop cancels a running query on a context detached from the caller's, which is already done on
// cancel or deadline; a done context would turn StopQuery into a no-op and leave the query billing.
func (r *batchRun) stop(api awsx.LogsAPI, queryID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.parent), stopQueryTimeout)
	defer cancel()
	if _, err := api.StopQuery(ctx, &cloudwatchlogs.StopQueryInput{QueryId: aws.String(queryID)}); err != nil {
		slog.Warn("log search: stop query", "query_id", queryID, "err", err)
	}
}

// failure turns an AWS error into a per-target state; a malformed query is the caller's mistake.
func (r *batchRun) failure(err error) batchOutcome {
	var malformed *logstypes.MalformedQueryException
	if errors.As(err, &malformed) {
		return batchOutcome{state: StateError, startErr: &QueryError{Message: aws.ToString(malformed.Message)}}
	}
	slog.Warn("log search: query error", "account", r.b.target.AccountID, "region", r.b.target.Region, "err", err)
	return classifyFailure(err, r.b.target)
}

func classifyFailure(err error, t awsx.Target) batchOutcome {
	var apiErr smithy.APIError
	switch {
	case awsx.IsAccessDenied(err):
		return batchOutcome{state: StateNoPermission, message: fmt.Sprintf(
			"The harbormaster-access role in %s cannot run Logs Insights queries. Update the access module to the latest version.", t.AccountID)}
	case awsx.IsThrottle(err), errors.As(err, &apiErr) && apiErr.ErrorCode() == "LimitExceededException":
		return batchOutcome{state: StateThrottled, message: "Too many Logs Insights queries are running in this account. Try again shortly."}
	}
	return batchOutcome{state: StateError, message: "Logs Insights returned an error."}
}

func outcomeOf(res *cloudwatchlogs.GetQueryResultsOutput) batchOutcome {
	out := batchOutcome{state: StateOK}
	if st := res.Statistics; st != nil {
		out.matched, out.scanned, out.bytes = int64(st.RecordsMatched), int64(st.RecordsScanned), int64(st.BytesScanned)
	}
	for _, fields := range res.Results {
		row := make(map[string]string, len(fields))
		for _, f := range fields {
			row[aws.ToString(f.Field)] = aws.ToString(f.Value)
		}
		out.rows = append(out.rows, row)
	}
	return out
}

// mapRow attributes a result row to a selected service, or drops it. Dropping rows that map to no
// selected service is defence in depth: the composed query already filters by stream prefix.
func mapRow(raw map[string]string, accountID string, idx targetIndex) (SearchRow, bool) {
	account, group, ok := strings.Cut(raw["@log"], ":")
	if !ok || account != accountID {
		return SearchRow{}, false
	}
	stream := raw["@logStream"]
	best, bestLen := "", -1
	for _, owner := range idx[group] {
		if strings.HasPrefix(stream, owner.prefix) && len(owner.prefix) > bestLen {
			best, bestLen = owner.serviceID, len(owner.prefix)
		}
	}
	if bestLen < 0 {
		return SearchRow{}, false
	}
	at, err := time.ParseInLocation(insightsTimestampLayout, raw["@timestamp"], time.UTC)
	if err != nil {
		return SearchRow{}, false
	}
	container, taskID := ParseStream(stream)
	message, truncated := truncateUTF8(raw["@message"], maxMessageBytes)
	return SearchRow{T: at, ServiceID: best, Container: container, TaskID: taskID, Message: message, MessageTruncated: truncated}, true
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func ceilUnix(t time.Time) int64 {
	sec := t.Unix()
	if t.Nanosecond() > 0 {
		sec++
	}
	return sec
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
