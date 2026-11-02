package fake

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	logLineInterval    = 9 * time.Second // one generated line per running task per interval
	sidecarLinePercent = 0.2             // share of generated lines written by the second container
	defaultLogPageSize = 100
	defaultLiveTailGap = 2 * time.Second
	logTokenPrefix     = "fakelog-"
	sidecarMessage     = "exporter: sent 512 spans to otlp endpoint"
	// healthLineEvery makes every n-th slot of a task the pool's health check line (when it has one), so a
	// log view always shows one within a few lines instead of depending on the pseudo-random pick.
	healthLineEvery = 4
	healthMarker    = "GET /health"
)

// logsClient is the fake CloudWatch Logs API scoped to one account and region.
//
// Groups follow the ECS convention "/ecs/<cluster>/<service>" and stream names
// are "<prefix>/<container>/<task id>" with the real ids of the service's
// running tasks. Estate.SetLogLines replaces the generated lines of a group.
type logsClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.LogsAPI = (*logsClient)(nil)

type logEvent struct {
	group   string
	stream  string
	at      time.Time
	message string
}

// logGroupName extracts the group name from a name or a log group ARN.
func logGroupName(identifier string) string {
	if _, after, ok := strings.Cut(identifier, ":log-group:"); ok {
		return strings.TrimSuffix(after, ":*")
	}
	return identifier
}

// FilterLogEvents serves the group's lines in [StartTime, EndTime], oldest first.
func (c *logsClient) FilterLogEvents(_ context.Context, in *cloudwatchlogs.FilterLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "FilterLogEvents", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	group := aws.ToString(in.LogGroupName)
	if group == "" {
		group = logGroupName(aws.ToString(in.LogGroupIdentifier))
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()

	end := e.now()
	if in.EndTime != nil {
		end = time.UnixMilli(*in.EndTime)
	}
	start := end.Add(-time.Hour)
	if in.StartTime != nil {
		start = time.UnixMilli(*in.StartTime)
	}
	events, err := c.eventsIn(group, start, end)
	if err != nil {
		return nil, err
	}
	events = filterEvents(events, in.LogStreamNames, aws.ToString(in.LogStreamNamePrefix), aws.ToString(in.FilterPattern))

	pageSize := c.f.LogsPageSize
	if pageSize <= 0 {
		pageSize = defaultLogPageSize
	}
	if in.Limit != nil && *in.Limit > 0 {
		pageSize = min(pageSize, int(*in.Limit))
	}
	from := 0
	if in.NextToken != nil && *in.NextToken != "" {
		n, convErr := strconv.Atoi(strings.TrimPrefix(*in.NextToken, logTokenPrefix))
		if convErr != nil || !strings.HasPrefix(*in.NextToken, logTokenPrefix) || n < 0 {
			return nil, &logstypes.InvalidParameterException{Message: aws.String("Invalid token")}
		}
		from = min(n, len(events))
	}
	to := min(from+pageSize, len(events))
	out := &cloudwatchlogs.FilterLogEventsOutput{}
	for i, ev := range events[from:to] {
		ms := ev.at.UnixMilli()
		out.Events = append(out.Events, logstypes.FilteredLogEvent{
			EventId:       aws.String(hexOf(hashSeed(ev.stream+ev.message, from+i))),
			IngestionTime: aws.Int64(ms),
			LogStreamName: aws.String(ev.stream),
			Message:       aws.String(ev.message),
			Timestamp:     aws.Int64(ms),
		})
	}
	if to < len(events) {
		out.NextToken = aws.String(logTokenPrefix + strconv.Itoa(to))
	}
	return out, nil
}

// eventsIn returns the group's events in [start, end] sorted oldest first
// (ties by stream). Caller holds Mu.
func (c *logsClient) eventsIn(group string, start, end time.Time) ([]logEvent, error) {
	e := c.f.Estate
	if lines, ok := e.LogLines[group]; ok {
		var events []logEvent
		for _, l := range lines {
			if !l.Timestamp.Before(start) && !l.Timestamp.After(end) {
				events = append(events, logEvent{stream: l.Stream, at: l.Timestamp, message: l.Message})
			}
		}
		sortEvents(events)
		return events, nil
	}
	src, ok := c.generatedSource(group)
	if !ok {
		return nil, &logstypes.ResourceNotFoundException{Message: aws.String("The specified log group does not exist.")}
	}
	var events []logEvent
	step := logLineInterval.Milliseconds()
	for _, task := range src.tasks {
		offset := int64(hashSeed(task.id, 1) % uint64(step))
		first := (start.UnixMilli() - offset + step - 1) / step
		for k := first; k*step+offset <= end.UnixMilli(); k++ {
			at := time.UnixMilli(k*step + offset)
			if at.Before(task.startedAt) || (!task.stoppedAt.IsZero() && at.After(task.stoppedAt)) {
				continue
			}
			events = append(events, src.lineFor(task, k, at))
		}
	}
	sortEvents(events)
	return events, nil
}

func sortEvents(events []logEvent) {
	// Insertion order is per task, so a stable sort by time then stream suffices.
	slices.SortStableFunc(events, func(a, b logEvent) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.stream, b.stream)
	})
}

type logTask struct {
	id                   string
	startedAt, stoppedAt time.Time
	containers           []string
}

type generatedSource struct {
	service string
	prefix  string
	pool    [][2]string
	tasks   []logTask
}

// generatedSource finds the service owning an "/ecs/<cluster>/<service>" group
// in this target and its running tasks. Caller holds Mu.
func (c *logsClient) generatedSource(group string) (generatedSource, bool) {
	e := c.f.Estate
	for _, svc := range e.Services {
		name := aws.ToString(svc.Service.ServiceName)
		if svc.AccountID != c.target.AccountID || svc.Region != c.target.Region || group != "/ecs/"+svc.ClusterName+"/"+name {
			continue
		}
		src := generatedSource{service: name, prefix: "ecs", pool: e.LogPools[name]}
		if src.pool == nil {
			src.pool = e.LogPools["default"]
		}
		for _, t := range e.Tasks {
			if t.AccountID != svc.AccountID || t.Region != svc.Region || t.ClusterName != svc.ClusterName ||
				aws.ToString(t.Task.Group) != "service:"+name || aws.ToString(t.Task.LastStatus) != "RUNNING" {
				continue
			}
			lt := logTask{id: lastSegment(aws.ToString(t.Task.TaskArn))}
			if t.Task.StartedAt != nil {
				lt.startedAt = *t.Task.StartedAt
			}
			for _, ct := range t.Task.Containers {
				lt.containers = append(lt.containers, aws.ToString(ct.Name))
			}
			src.tasks = append(src.tasks, lt)
		}
		return src, true
	}
	return generatedSource{}, false
}

// lineFor builds the deterministic line of task slot k. The second container
// (a sidecar, if the task has one) writes about a fifth of the lines.
func (g generatedSource) lineFor(task logTask, k int64, at time.Time) logEvent {
	seed := hashSeed(task.id, 2)
	container := g.service
	if len(task.containers) > 0 {
		container = task.containers[0]
	}
	message := ""
	if healthEntry, ok := g.healthEntry(); ok && k%healthLineEvery == 0 {
		message = levelPrefix(healthEntry[0]) + healthEntry[1]
	} else if len(task.containers) > 1 && unitNoise(seed, uint64(k)) < sidecarLinePercent {
		container = task.containers[1]
		message = sidecarMessage
	} else if len(g.pool) > 0 {
		entry := g.pool[int(unitNoise(seed^0xabcdef, uint64(k))*float64(len(g.pool)))]
		message = levelPrefix(entry[0]) + entry[1]
	}
	return logEvent{stream: g.prefix + "/" + container + "/" + task.id, at: at, message: message}
}

// healthEntry returns the pool's health check line, if the service has one.
func (g generatedSource) healthEntry() ([2]string, bool) {
	for _, entry := range g.pool {
		if strings.Contains(entry[1], healthMarker) {
			return entry, true
		}
	}
	return [2]string{}, false
}

// levelPrefix marks warn and error lines with a token the log level heuristic
// recognises; info lines stay as they are.
func levelPrefix(level string) string {
	switch level {
	case "warn":
		return "WARN "
	case "error":
		return "ERROR "
	}
	return ""
}

// filterEvents applies stream names, a stream prefix and a simple filter
// pattern (every whitespace-separated term, or one quoted phrase, must occur).
func filterEvents(events []logEvent, names []string, prefix, pattern string) []logEvent {
	terms := patternTerms(pattern)
	kept := events[:0:0]
	for _, ev := range events {
		if len(names) > 0 && !hasString(names, ev.stream) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(ev.stream, prefix) {
			continue
		}
		match := true
		for _, term := range terms {
			if !strings.Contains(ev.message, term) {
				match = false
				break
			}
		}
		if match {
			kept = append(kept, ev)
		}
	}
	return kept
}

func patternTerms(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if len(pattern) >= 2 && strings.HasPrefix(pattern, `"`) && strings.HasSuffix(pattern, `"`) {
		return []string{pattern[1 : len(pattern)-1]}
	}
	return strings.Fields(pattern)
}

// LiveTail starts a session that sends SessionStart, then a SessionUpdate with
// one to three lines every Fake.LiveTailInterval (default 2 s) until Close or
// ctx is cancelled. The input is recorded for Fake.LastLiveTailInput.
func (c *logsClient) LiveTail(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput) (awsx.LiveTailStream, error) {
	done, err := c.f.beginThrottled(c.target, "logs", "StartLiveTail", &c.f.ThrottleLogs)
	defer done()
	if err != nil {
		return nil, err
	}
	recorded := *in
	recorded.LogGroupIdentifiers = append([]string(nil), in.LogGroupIdentifiers...)
	recorded.LogStreamNames = append([]string(nil), in.LogStreamNames...)
	recorded.LogStreamNamePrefixes = append([]string(nil), in.LogStreamNamePrefixes...)
	c.f.liveTailMu.Lock()
	c.f.lastLiveTailInput = &recorded
	interval, failure, failAfter := c.f.LiveTailInterval, c.f.LiveTailError, c.f.LiveTailErrorAfter
	c.f.liveTailMu.Unlock()
	if interval <= 0 {
		interval = defaultLiveTailGap
	}

	e := c.f.Estate
	e.Mu.Lock()
	var candidates []logEvent
	now := e.now()
	for _, id := range in.LogGroupIdentifiers {
		group := logGroupName(id)
		events, err := c.eventsIn(group, now.Add(-logLineInterval*3), now)
		if err != nil {
			e.Mu.Unlock()
			return nil, err
		}
		for i := range events {
			events[i].group = id
		}
		candidates = append(candidates, events...)
	}
	e.Mu.Unlock()
	candidates = filterEvents(candidates, in.LogStreamNames, "", aws.ToString(in.LogEventFilterPattern))
	if len(in.LogStreamNamePrefixes) > 0 {
		var kept []logEvent
		for _, ev := range candidates {
			for _, p := range in.LogStreamNamePrefixes {
				if strings.HasPrefix(ev.stream, p) {
					kept = append(kept, ev)
					break
				}
			}
		}
		candidates = kept
	}

	s := &fakeLiveTail{clock: e.now, events: make(chan logstypes.StartLiveTailResponseStream, 16), done: make(chan struct{})}
	go s.run(ctx, in, candidates, interval, failure, failAfter)
	return s, nil
}

// fakeLiveTail is the fake's awsx.LiveTailStream.
type fakeLiveTail struct {
	clock  func() time.Time // the estate clock
	events chan logstypes.StartLiveTailResponseStream
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func (s *fakeLiveTail) Events() <-chan logstypes.StartLiveTailResponseStream { return s.events }

func (s *fakeLiveTail) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeLiveTail) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *fakeLiveTail) send(ctx context.Context, ev logstypes.StartLiveTailResponseStream) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *fakeLiveTail) run(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput, lines []logEvent, interval time.Duration, failure error, failAfter int) {
	defer close(s.events)
	start := logstypes.LiveTailSessionStart{
		LogGroupIdentifiers:   in.LogGroupIdentifiers,
		LogStreamNames:        in.LogStreamNames,
		LogStreamNamePrefixes: in.LogStreamNamePrefixes,
		LogEventFilterPattern: in.LogEventFilterPattern,
		RequestId:             aws.String("fake-request"),
		SessionId:             aws.String("fake-session"),
	}
	if !s.send(ctx, &logstypes.StartLiveTailResponseStreamMemberSessionStart{Value: start}) {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for updates := 0; ; updates++ {
		if failure != nil && updates >= failAfter {
			s.mu.Lock()
			s.err = failure
			s.mu.Unlock()
			return
		}
		select {
		case <-ticker.C:
		case <-s.done:
			return
		case <-ctx.Done():
			return
		}
		if len(lines) == 0 {
			continue
		}
		count := 1 + int(unitNoise(1, uint64(updates))*3)
		update := logstypes.LiveTailSessionUpdate{SessionMetadata: &logstypes.LiveTailSessionMetadata{}}
		now := s.clock().UnixMilli()
		for i := 0; i < count; i++ {
			ev := lines[(updates*3+i)%len(lines)]
			update.SessionResults = append(update.SessionResults, logstypes.LiveTailSessionLogEvent{
				LogGroupIdentifier: aws.String(ev.group),
				LogStreamName:      aws.String(ev.stream),
				Message:            aws.String(ev.message),
				Timestamp:          aws.Int64(now),
				IngestionTime:      aws.Int64(now),
			})
		}
		if !s.send(ctx, &logstypes.StartLiveTailResponseStreamMemberSessionUpdate{Value: update}) {
			return
		}
	}
}

// LastLiveTailInput returns a copy of the most recent StartLiveTailInput, or nil.
func (f *Fake) LastLiveTailInput() *cloudwatchlogs.StartLiveTailInput {
	f.liveTailMu.Lock()
	defer f.liveTailMu.Unlock()
	if f.lastLiveTailInput == nil {
		return nil
	}
	cp := *f.lastLiveTailInput
	return &cp
}
