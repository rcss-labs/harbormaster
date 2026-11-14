package fake

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

const (
	maxInsightsGroups     = 50
	defaultInsightsLimit  = 1000
	insightsTimeLayout    = "2006-01-02 15:04:05.000"
	insightsLineOverhead  = 26 // CloudWatch counts 26 bytes of metadata per event
	insightsQueryIDPrefix = "fakeq-"
)

// insightsState holds the fake's Logs Insights queries and the record of stopped ones.
type insightsState struct {
	mu      sync.Mutex
	next    int
	queries map[string]*insightsQuery
	stopped []string
}

// insightsQuery is a query evaluated at StartQuery time and revealed by polling.
type insightsQuery struct {
	polls     int
	cancelled bool
	done      bool
	rows      [][]logstypes.ResultField
	stats     logstypes.QueryStatistics
}

// StoppedQueries returns the ids passed to StopQuery, in call order.
func (f *Fake) StoppedQueries() []string {
	f.insights.mu.Lock()
	defer f.insights.mu.Unlock()
	return slices.Clone(f.insights.stopped)
}

func malformedQuery(format string, args ...any) error {
	return &logstypes.MalformedQueryException{Message: aws.String(fmt.Sprintf(format, args...))}
}

// StartQuery validates the request, evaluates the query against the groups' lines
// and returns an id. The query is evaluated once, here; polling only reveals it.
func (c *logsClient) StartQuery(_ context.Context, in *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "StartQuery", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	groups := slices.Clone(in.LogGroupNames)
	for _, id := range in.LogGroupIdentifiers {
		groups = append(groups, logGroupName(id))
	}
	invalid := func(msg string) error { return &logstypes.InvalidParameterException{Message: aws.String(msg)} }
	switch {
	case len(groups) == 0 || len(groups) > maxInsightsGroups:
		return nil, invalid(fmt.Sprintf("a query needs between 1 and %d log groups, got %d", maxInsightsGroups, len(groups)))
	case in.StartTime == nil || in.EndTime == nil || *in.EndTime <= *in.StartTime:
		return nil, invalid("EndTime must be after StartTime")
	case strings.TrimSpace(aws.ToString(in.QueryString)) == "":
		return nil, invalid("QueryString must not be empty")
	}
	plan, err := parseInsightsQuery(aws.ToString(in.QueryString))
	if err != nil {
		return nil, err
	}
	if plan.limit == 0 {
		plan.limit = defaultInsightsLimit
		if in.Limit != nil && *in.Limit > 0 {
			plan.limit = int(*in.Limit)
		}
	}

	start, end := time.Unix(*in.StartTime, 0), time.Unix(*in.EndTime, 0)
	e := c.f.Estate
	e.Mu.Lock()
	type hit struct {
		group string
		ev    logEvent
	}
	var scanned, matched []hit
	var bytes float64
	for _, group := range groups {
		events, evErr := c.eventsIn(group, start, end)
		if evErr != nil {
			e.Mu.Unlock()
			return nil, evErr
		}
		for _, ev := range events {
			scanned = append(scanned, hit{group, ev})
			bytes += float64(len(ev.message) + insightsLineOverhead)
			if plan.matches(c.target.AccountID+":"+group, ev) {
				matched = append(matched, hit{group, ev})
			}
		}
	}
	e.Mu.Unlock()

	// Newest first; the stable sort keeps the oldest-first order of equal timestamps reversed below.
	slices.Reverse(matched)
	slices.SortStableFunc(matched, func(a, b hit) int { return b.ev.at.Compare(a.ev.at) })
	q := &insightsQuery{stats: logstypes.QueryStatistics{
		RecordsMatched: float64(len(matched)),
		RecordsScanned: float64(len(scanned)),
		BytesScanned:   bytes,
	}}
	for i, h := range matched[:min(len(matched), plan.limit)] {
		q.rows = append(q.rows, []logstypes.ResultField{
			{Field: aws.String("@timestamp"), Value: aws.String(h.ev.at.UTC().Format(insightsTimeLayout))},
			{Field: aws.String("@message"), Value: aws.String(h.ev.message)},
			{Field: aws.String("@log"), Value: aws.String(c.target.AccountID + ":" + h.group)},
			{Field: aws.String("@logStream"), Value: aws.String(h.ev.stream)},
			{Field: aws.String("@ptr"), Value: aws.String(hexOf(hashSeed(h.group+h.ev.stream+h.ev.message, i)))},
		})
	}

	s := &c.f.insights
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queries == nil {
		s.queries = map[string]*insightsQuery{}
	}
	s.next++
	id := insightsQueryIDPrefix + strconv.Itoa(s.next)
	s.queries[id] = q
	return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String(id)}, nil
}

// GetQueryResults reports Running (no rows) on the first poll of a query and
// Complete with rows and statistics from the second, unless the query was stopped
// or Fake.InsightsAlwaysRunning is set.
func (c *logsClient) GetQueryResults(_ context.Context, in *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "GetQueryResults", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	s := &c.f.insights
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queries[aws.ToString(in.QueryId)]
	if !ok {
		return nil, &logstypes.ResourceNotFoundException{Message: aws.String("The specified query does not exist.")}
	}
	switch {
	case q.cancelled:
		return &cloudwatchlogs.GetQueryResultsOutput{Status: logstypes.QueryStatusCancelled, Statistics: &logstypes.QueryStatistics{}}, nil
	case c.f.InsightsAlwaysRunning:
		return &cloudwatchlogs.GetQueryResultsOutput{Status: logstypes.QueryStatusRunning, Statistics: &logstypes.QueryStatistics{}}, nil
	}
	q.polls++
	if q.polls < 2 {
		// A running query reports the bytes it has scanned so far (the scan cap relies on it); the fake
		// has done all of its scanning by the first poll.
		return &cloudwatchlogs.GetQueryResultsOutput{Status: logstypes.QueryStatusRunning, Statistics: &logstypes.QueryStatistics{BytesScanned: q.stats.BytesScanned}}, nil
	}
	q.done = true
	stats := q.stats
	return &cloudwatchlogs.GetQueryResultsOutput{Status: logstypes.QueryStatusComplete, Results: q.rows, Statistics: &stats}, nil
}

// StopQuery records the stop and cancels a query that has not completed.
func (c *logsClient) StopQuery(_ context.Context, in *cloudwatchlogs.StopQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "StopQuery", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	s := &c.f.insights
	s.mu.Lock()
	defer s.mu.Unlock()
	id := aws.ToString(in.QueryId)
	q, ok := s.queries[id]
	if !ok {
		return nil, &logstypes.ResourceNotFoundException{Message: aws.String("The specified query does not exist.")}
	}
	s.stopped = append(s.stopped, id)
	if q.done {
		return &cloudwatchlogs.StopQueryOutput{Success: false}, nil
	}
	q.cancelled = true
	return &cloudwatchlogs.StopQueryOutput{Success: true}, nil
}

// insightsPlan is the parsed subset of Logs Insights the fake evaluates.
type insightsPlan struct {
	scope   []scopeClause // OR-ed; empty means every line
	filters []messageFilter
	limit   int // 0 when the query has no limit stage
}

type scopeClause struct {
	log    string
	stream *regexp.Regexp
}

type messageFilter struct {
	negate bool
	re     *regexp.Regexp // set for /regex/ forms
	text   string         // substring for "text" forms
}

var (
	// One composed scope clause: (@log = "acct:group" and @logStream like /^(p1|p2)/).
	scopeClauseRe = regexp.MustCompile(`\(@log = "((?:[^"\\]|\\.)*)" and @logStream like /((?:[^/\\]|\\.)*)/\)`)
	scopeJoinRe   = regexp.MustCompile(`^\s*(or\s*)?$`)
	messageRe     = regexp.MustCompile(`^@message (not )?like (?:/((?:[^/\\]|\\.)*)/|"((?:[^"\\]|\\.)*)")$`)
	limitRe       = regexp.MustCompile(`^limit (\d+)$`)
)

// parseInsightsQuery reads one stage per line (lines after the first start with "| "). Anything the fake cannot read in a
// filter stage is a MalformedQueryException, so tests fail instead of over-matching.
func parseInsightsQuery(query string) (insightsPlan, error) {
	var plan insightsPlan
	for _, raw := range strings.Split(query, "\n") {
		// Stages are separated by a newline and "| "; the first has no pipe.
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "|"))
		if line == "" {
			continue
		}
		command, rest, _ := strings.Cut(line, " ")
		switch command {
		case "fields", "parse", "dedup", "sort":
		case "limit":
			m := limitRe.FindStringSubmatch(line)
			if m == nil {
				return plan, malformedQuery("cannot read limit stage %q", line)
			}
			plan.limit, _ = strconv.Atoi(m[1])
		case "filter":
			if err := plan.addFilter(strings.TrimSpace(rest)); err != nil {
				return plan, err
			}
		default:
			return plan, malformedQuery("unsupported command %q", command)
		}
	}
	return plan, nil
}

func (p *insightsPlan) addFilter(expr string) error {
	if strings.HasPrefix(expr, "(@log") {
		matches := scopeClauseRe.FindAllStringSubmatchIndex(expr, -1)
		prev := 0
		var clauses []scopeClause
		for _, m := range matches {
			if !scopeJoinRe.MatchString(expr[prev:m[0]]) {
				return malformedQuery("cannot read scope filter %q", expr)
			}
			prev = m[1]
			stream, err := regexp.Compile(expr[m[4]:m[5]])
			if err != nil {
				return malformedQuery("bad stream regex in %q: %v", expr, err)
			}
			clauses = append(clauses, scopeClause{log: expr[m[2]:m[3]], stream: stream})
		}
		if len(clauses) == 0 || strings.TrimSpace(expr[prev:]) != "" {
			return malformedQuery("cannot read scope filter %q", expr)
		}
		p.scope = append(p.scope, clauses...)
		return nil
	}
	m := messageRe.FindStringSubmatch(expr)
	if m == nil {
		return malformedQuery("cannot read filter %q", expr)
	}
	f := messageFilter{negate: m[1] != ""}
	if strings.HasPrefix(expr[strings.Index(expr, "like ")+5:], "/") {
		re, err := regexp.Compile(m[2])
		if err != nil {
			return malformedQuery("bad regex in %q: %v", expr, err)
		}
		f.re = re
	} else {
		f.text = m[3]
	}
	p.filters = append(p.filters, f)
	return nil
}

// matches reports whether a line (in the group whose @log value is log) passes every stage.
func (p insightsPlan) matches(log string, ev logEvent) bool {
	if len(p.scope) > 0 {
		inScope := false
		for _, clause := range p.scope {
			if clause.log == log && clause.stream.MatchString(ev.stream) {
				inScope = true
				break
			}
		}
		if !inScope {
			return false
		}
	}
	for _, f := range p.filters {
		var hit bool
		if f.re != nil {
			hit = f.re.MatchString(ev.message)
		} else {
			hit = strings.Contains(ev.message, f.text)
		}
		if hit == f.negate {
			return false
		}
	}
	return true
}
