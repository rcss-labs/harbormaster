package logs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

const (
	homelabAccount = "482109375512"
	stagingAccount = "731022849906"
	sharedGroup    = "/ecs/test/shared"
)

// spyClients wraps the fake so tests can see StartQuery/StopQuery calls and fail one account.
type spyClients struct {
	awsx.Clients
	mu          sync.Mutex
	starts      []spyStart
	stopCtxErrs []error // ctx.Err() seen by each StopQuery call
	failStart   map[string]error
	failLogs    error
	// failLogsOnDoneCtx makes Logs fail when given a finished context, like awsx.Real
	// retrieving credentials with it.
	failLogsOnDoneCtx bool
	// scripted, when set, replaces the fake: StartQuery returns "scripted-q" and every
	// GetQueryResults returns this response.
	scripted *cloudwatchlogs.GetQueryResultsOutput
	// cancelDuringStart, when set, is called after AWS has created the query and before the
	// response reaches the caller, like a cancel landing mid-request. If the StartQuery context
	// is done by then, the response is lost the way an HTTP client loses it.
	cancelDuringStart func()
}

type spyStart struct {
	target awsx.Target
	groups []string
	query  string
}

func (s *spyClients) Logs(ctx context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	if s.failLogs != nil {
		return nil, s.failLogs
	}
	if s.failLogsOnDoneCtx && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	api, err := s.Clients.Logs(ctx, t)
	if err != nil {
		return nil, err
	}
	return &spyLogs{LogsAPI: api, s: s, target: t}, nil
}

type spyLogs struct {
	awsx.LogsAPI
	s      *spyClients
	target awsx.Target
}

func (l *spyLogs) StartQuery(ctx context.Context, in *cloudwatchlogs.StartQueryInput, o ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	l.s.mu.Lock()
	l.s.starts = append(l.s.starts, spyStart{l.target, slices.Clone(in.LogGroupNames), aws.ToString(in.QueryString)})
	err := l.s.failStart[l.target.AccountID]
	l.s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if l.s.scripted != nil {
		return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String("scripted-q")}, nil
	}
	if l.s.cancelDuringStart != nil {
		out, err := l.LogsAPI.StartQuery(context.WithoutCancel(ctx), in, o...)
		l.s.cancelDuringStart()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return out, err
	}
	return l.LogsAPI.StartQuery(ctx, in, o...)
}

func (l *spyLogs) StopQuery(ctx context.Context, in *cloudwatchlogs.StopQueryInput, o ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	l.s.mu.Lock()
	l.s.stopCtxErrs = append(l.s.stopCtxErrs, ctx.Err())
	l.s.mu.Unlock()
	if l.s.scripted != nil {
		return &cloudwatchlogs.StopQueryOutput{Success: true}, nil
	}
	return l.LogsAPI.StopQuery(ctx, in, o...)
}

func (l *spyLogs) GetQueryResults(ctx context.Context, in *cloudwatchlogs.GetQueryResultsInput, o ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	if l.s.scripted != nil {
		return l.s.scripted, nil
	}
	return l.LogsAPI.GetQueryResults(ctx, in, o...)
}

func resultRow(ts, msg, log, stream string) []logstypes.ResultField {
	return []logstypes.ResultField{
		{Field: aws.String("@timestamp"), Value: aws.String(ts)},
		{Field: aws.String("@message"), Value: aws.String(msg)},
		{Field: aws.String("@log"), Value: aws.String(log)},
		{Field: aws.String("@logStream"), Value: aws.String(stream)},
	}
}

func (s *spyClients) startsOn(t awsx.Target) []spyStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []spyStart
	for _, st := range s.starts {
		if st.target == t {
			out = append(out, st)
		}
	}
	return out
}

func newSearcher(f *fake.Fake) (*Searcher, *spyClients) {
	spy := &spyClients{Clients: f, failStart: map[string]error{}}
	return &Searcher{
		Clients:   spy,
		Now:       func() time.Time { return testNow },
		PollEvery: time.Millisecond,
	}, spy
}

func newSearchFake() *fake.Fake {
	return fake.New(fake.MockupEstate(func() time.Time { return testNow }))
}

func searchReq(services ...SearchService) SearchRequest {
	return SearchRequest{Services: services, From: testNow.Add(-time.Hour), To: testNow}
}

func svc(id, account, region, group string, prefixes ...string) SearchService {
	return SearchService{ID: id, AccountID: account, LogRegion: region, Group: group, Prefixes: prefixes}
}

func line(stream string, ago time.Duration, msg string) fake.LogLine {
	return fake.LogLine{Stream: stream, Timestamp: testNow.Add(-ago), Message: msg}
}

type apiError struct{ code string }

func (e apiError) Error() string                 { return e.code }
func (e apiError) ErrorCode() string             { return e.code }
func (e apiError) ErrorMessage() string          { return e.code }
func (e apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func targetState(t *testing.T, res SearchResult, target awsx.Target) TargetResult {
	t.Helper()
	for _, tr := range res.Targets {
		if tr.Target == target {
			return tr
		}
	}
	t.Fatalf("no result for target %v in %+v", target, res.Targets)
	return TargetResult{}
}

func TestSearcher_SharedGroupReturnsOnlySelectedStreams(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines(sharedGroup, []fake.LogLine{
		line("api/web/t1", time.Minute, "a1"),
		line("worker/job/t2", 2*time.Minute, "b1"),
		line("api/web/t1", 3*time.Minute, "a2"),
		line("other/x/t3", 4*time.Minute, "c1"),
	})
	s, _ := newSearcher(f)
	res, err := s.Search(context.Background(), searchReq(
		svc("A", homelabAccount, "us-east-1", sharedGroup, "api/web/")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v, want the 2 lines of A", res.Rows)
	}
	for _, r := range res.Rows {
		if r.ServiceID != "A" || r.Container != "web" || r.TaskID != "t1" || strings.HasPrefix(r.Message, "b") {
			t.Errorf("unexpected row %+v", r)
		}
	}
	if res.Rows[0].Message != "a1" {
		t.Errorf("newest first: got %q first", res.Rows[0].Message)
	}
}

func TestSearcher_MapsRowsToLongestMatchingPrefix(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines(sharedGroup, []fake.LogLine{line("app/api/t1", time.Minute, "x")})
	s, _ := newSearcher(f)
	res, err := s.Search(context.Background(), searchReq(
		svc("broad", homelabAccount, "us-east-1", sharedGroup, "app/"),
		svc("narrow", homelabAccount, "us-east-1", sharedGroup, "app/api/")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].ServiceID != "narrow" {
		t.Fatalf("rows = %+v, want one row for narrow", res.Rows)
	}
}

func TestSearcher_FansOutPerAccountAndLogRegion(t *testing.T) {
	f := newSearchFake()
	for _, g := range []string{"/g/a", "/g/b", "/g/c"} {
		f.Estate.SetLogLines(g, []fake.LogLine{line("p/c/t", time.Minute, "m "+g)})
	}
	s, spy := newSearcher(f)
	_, err := s.Search(context.Background(), searchReq(
		svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/"),
		svc("B", stagingAccount, "us-east-1", "/g/b", "p/c/"),
		svc("C", homelabAccount, "eu-west-1", "/g/c", "p/c/"), // awslogs-region differs from the service region
	))
	if err != nil {
		t.Fatal(err)
	}
	want := map[awsx.Target]string{
		{AccountID: homelabAccount, Region: "us-east-1"}: "/g/a",
		{AccountID: stagingAccount, Region: "us-east-1"}: "/g/b",
		{AccountID: homelabAccount, Region: "eu-west-1"}: "/g/c",
	}
	if len(spy.starts) != 3 {
		t.Fatalf("StartQuery calls = %d, want 3", len(spy.starts))
	}
	for target, group := range want {
		got := spy.startsOn(target)
		if len(got) != 1 || !slices.Equal(got[0].groups, []string{group}) {
			t.Errorf("target %v: starts = %+v, want one on %s", target, got, group)
		}
	}
}

func TestSearcher_ChunksAt50Groups(t *testing.T) {
	f := newSearchFake()
	var services []SearchService
	for i := 0; i < 60; i++ {
		g := fmt.Sprintf("/g/%02d", i)
		f.Estate.SetLogLines(g, []fake.LogLine{line("p/c/t", time.Minute, "m")})
		services = append(services, svc(fmt.Sprintf("s%02d", i), homelabAccount, "us-east-1", g, "p/c/"))
	}
	s, spy := newSearcher(f)
	res, err := s.Search(context.Background(), searchReq(services...))
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{}
	for _, st := range spy.starts {
		sizes = append(sizes, len(st.groups))
	}
	slices.Sort(sizes)
	if !slices.Equal(sizes, []int{10, 50}) {
		t.Fatalf("batch sizes = %v, want [10 50]", sizes)
	}
	if len(res.Targets) != 1 || res.Targets[0].State != StateOK || len(res.Rows) != 60 {
		t.Errorf("targets=%+v rows=%d, want one ok target and 60 rows", res.Targets, len(res.Rows))
	}
}

func TestSearcher_MergesNewestFirstAndCaps(t *testing.T) {
	f := newSearchFake()
	var l1, l2 []fake.LogLine
	for i := 0; i < 6; i++ {
		l1 = append(l1, line("p/c/t", time.Duration(2*i+1)*time.Minute, fmt.Sprintf("h%d", i)))
		l2 = append(l2, line("p/c/t", time.Duration(2*i+2)*time.Minute, fmt.Sprintf("s%d", i)))
	}
	f.Estate.SetLogLines("/g/home", l1)
	f.Estate.SetLogLines("/g/stag", l2)
	s, _ := newSearcher(f)
	req := searchReq(
		svc("H", homelabAccount, "us-east-1", "/g/home", "p/c/"),
		svc("S", stagingAccount, "us-east-1", "/g/stag", "p/c/"))
	req.MaxRows = 5
	res, err := s.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range res.Rows {
		got = append(got, r.Message)
	}
	if want := []string{"h0", "s0", "h1", "s1", "h2"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if res.RecordsMatched != 12 || !res.Capped {
		t.Errorf("RecordsMatched=%d Capped=%v, want 12, true", res.RecordsMatched, res.Capped)
	}
	if res.BytesScanned <= 0 {
		t.Errorf("BytesScanned = %d, want a sum over targets", res.BytesScanned)
	}
}

func TestSearcher_TruncatesLongMessagesOnRuneBoundary(t *testing.T) {
	f := newSearchFake()
	long := strings.Repeat("é", 3000) // 6000 bytes
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, long)})
	s, _ := newSearcher(f)
	res, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/")))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows[0]
	if !r.MessageTruncated || len(r.Message) != 4096 || strings.ContainsRune(r.Message, '�') {
		t.Errorf("truncated=%v len=%d, want true, 4096, valid UTF-8", r.MessageTruncated, len(r.Message))
	}
}

func TestSearcher_AccessDeniedIsPerTarget(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines("/g/home", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	f.Estate.SetLogLines("/g/stag", []fake.LogLine{line("p/c/t", time.Minute, "s")})
	s, spy := newSearcher(f)
	spy.failStart[stagingAccount] = apiError{"AccessDeniedException"}
	res, err := s.Search(context.Background(), searchReq(
		svc("H", homelabAccount, "us-east-1", "/g/home", "p/c/"),
		svc("S", stagingAccount, "us-east-1", "/g/stag", "p/c/")))
	if err != nil {
		t.Fatalf("a denied target must not fail the search: %v", err)
	}
	denied := targetState(t, res, awsx.Target{AccountID: stagingAccount, Region: "us-east-1"})
	if denied.State != StateNoPermission || !strings.Contains(denied.Message, stagingAccount) ||
		!strings.Contains(denied.Message, "cannot run Logs Insights queries") || !slices.Equal(denied.ServiceIDs, []string{"S"}) {
		t.Errorf("denied target = %+v", denied)
	}
	if ok := targetState(t, res, awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}); ok.State != StateOK {
		t.Errorf("other target = %+v, want ok", ok)
	}
	if len(res.Rows) != 1 || res.Rows[0].ServiceID != "H" {
		t.Errorf("rows = %+v, want the healthy account's line", res.Rows)
	}
}

func TestSearcher_ThrottleAndOtherErrorsArePerTarget(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines("/g/home", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, spy := newSearcher(f)
	spy.failStart[stagingAccount] = apiError{"LimitExceededException"}
	spy.failStart["000000000000"] = apiError{"InternalFailure"}
	f.Estate.Mu.Lock()
	f.Estate.Accounts = append(f.Estate.Accounts, fake.Account{ID: "000000000000", Name: "x", Regions: []string{"us-east-1"}})
	f.Estate.Mu.Unlock()
	res, err := s.Search(context.Background(), searchReq(
		svc("H", homelabAccount, "us-east-1", "/g/home", "p/c/"),
		svc("S", stagingAccount, "us-east-1", "/g/stag", "p/c/"),
		svc("X", "000000000000", "us-east-1", "/g/x", "p/c/")))
	if err != nil {
		t.Fatal(err)
	}
	if tr := targetState(t, res, awsx.Target{AccountID: stagingAccount, Region: "us-east-1"}); tr.State != StateThrottled ||
		tr.Message != "Too many Logs Insights queries are running in this account. Try again shortly." {
		t.Errorf("throttled target = %+v", tr)
	}
	if tr := targetState(t, res, awsx.Target{AccountID: "000000000000", Region: "us-east-1"}); tr.State != StateError ||
		tr.Message != "Logs Insights returned an error." {
		t.Errorf("error target = %+v", tr)
	}
	if len(res.Rows) != 1 {
		t.Errorf("rows = %d, want 1", len(res.Rows))
	}
}

func TestSearcher_AllClientsFailingIsAnError(t *testing.T) {
	f := newSearchFake()
	s, spy := newSearcher(f)
	spy.failLogs = errors.New("assume role failed")
	_, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/")))
	if err == nil {
		t.Fatal("want an error when no target could start")
	}
}

func TestSearcher_TimeoutStopsQueries(t *testing.T) {
	f := newSearchFake()
	f.InsightsAlwaysRunning = true
	f.Estate.SetLogLines("/g/home", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	f.Estate.SetLogLines("/g/stag", []fake.LogLine{line("p/c/t", time.Minute, "s")})
	s, _ := newSearcher(f)
	s.Timeout = 20 * time.Millisecond
	res, err := s.Search(context.Background(), searchReq(
		svc("H", homelabAccount, "us-east-1", "/g/home", "p/c/"),
		svc("S", stagingAccount, "us-east-1", "/g/stag", "p/c/")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range res.Targets {
		if tr.State != StateTimeout {
			t.Errorf("target %v state = %s, want timeout", tr.Target, tr.State)
		}
	}
	stopped := f.StoppedQueries()
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"fakeq-1", "fakeq-2"}) {
		t.Errorf("stopped = %v, want both queries", stopped)
	}
}

func TestSearcher_CancelStopsQueries(t *testing.T) {
	f := newSearchFake()
	f.InsightsAlwaysRunning = true
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, spy := newSearcher(f)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	res, err := s.Search(ctx, searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Targets[0].State != StateCancelled {
		t.Errorf("state = %s, want cancelled", res.Targets[0].State)
	}
	if got := f.StoppedQueries(); len(got) != 1 {
		t.Fatalf("stopped = %v, want 1", got)
	}
	if len(spy.stopCtxErrs) != 1 || spy.stopCtxErrs[0] != nil {
		t.Errorf("StopQuery context errors = %v, want a live context", spy.stopCtxErrs)
	}
}

func TestSearcher_CancelDuringStartQueryStillStopsTheQuery(t *testing.T) {
	f := newSearchFake()
	f.InsightsAlwaysRunning = true
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, spy := newSearcher(f)
	ctx, cancel := context.WithCancel(context.Background())
	spy.cancelDuringStart = cancel
	res, err := s.Search(ctx, searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Targets[0].State != StateCancelled {
		t.Errorf("state = %s, want cancelled", res.Targets[0].State)
	}
	if got := f.StoppedQueries(); len(got) != 1 {
		t.Errorf("stopped = %v, want the query that was just created stopped", got)
	}
}

func TestSearcher_MalformedQueryIsQueryError(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, _ := newSearcher(f)
	req := searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/"))
	req.Query = "filter @message like /(/"
	_, err := s.Search(context.Background(), req)
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want *QueryError", err)
	}
}

func TestSearcher_StopQueryDoesNotNeedALiveContextToBuildTheClient(t *testing.T) {
	f := newSearchFake()
	f.InsightsAlwaysRunning = true
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, spy := newSearcher(f)
	spy.failLogsOnDoneCtx = true
	s.Timeout = 20 * time.Millisecond
	if _, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/"))); err != nil {
		t.Fatal(err)
	}
	if got := f.StoppedQueries(); len(got) != 1 {
		t.Fatalf("stopped = %v, want the running query stopped", got)
	}
}

func TestSearcher_DropsRowsOutsideTheSelectedScope(t *testing.T) {
	f := newSearchFake()
	s, spy := newSearcher(f)
	const ts = "2026-10-31 11:59:00.000"
	spy.scripted = &cloudwatchlogs.GetQueryResultsOutput{
		Status: logstypes.QueryStatusComplete,
		Results: [][]logstypes.ResultField{
			resultRow(ts, "keep", homelabAccount+":/g/a", "api/web/t1"),
			resultRow(ts, "unselected stream", homelabAccount+":/g/a", "worker/job/t2"),
			resultRow(ts, "wrong group", homelabAccount+":/g/other", "api/web/t1"),
			resultRow(ts, "wrong account", stagingAccount+":/g/a", "api/web/t1"),
			resultRow(ts, "no log", "", "api/web/t1"),
		},
		Statistics: &logstypes.QueryStatistics{RecordsMatched: 5},
	}
	res, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "api/web/")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Message != "keep" {
		t.Fatalf("rows = %+v, want only the in-scope row", res.Rows)
	}
}

func TestSearcher_KeepsRowsSeenWhileRunningOnTimeoutAndCancel(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newSearchFake()
			s, spy := newSearcher(f)
			spy.scripted = &cloudwatchlogs.GetQueryResultsOutput{
				Status:     logstypes.QueryStatusRunning,
				Results:    [][]logstypes.ResultField{resultRow("2026-10-31 11:59:00.000", "partial", homelabAccount+":/g/a", "api/web/t1")},
				Statistics: &logstypes.QueryStatistics{RecordsMatched: 7},
			}
			ctx := context.Background()
			if mode == "timeout" {
				s.Timeout = 20 * time.Millisecond
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() { time.Sleep(20 * time.Millisecond); cancel() }()
			}
			res, err := s.Search(ctx, searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "api/web/")))
			if err != nil {
				t.Fatal(err)
			}
			want := StateTimeout
			if mode == "cancel" {
				want = StateCancelled
			}
			if res.Targets[0].State != want || len(res.Rows) != 1 || res.Rows[0].Message != "partial" || res.RecordsMatched != 7 {
				t.Errorf("state=%s rows=%+v matched=%d, want %s with the partial row and 7", res.Targets[0].State, res.Rows, res.RecordsMatched, want)
			}
		})
	}
}

func TestSearcher_ClampsMaxRowsToInsightsLimit(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, spy := newSearcher(f)
	req := searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/"))
	req.MaxRows = 50000
	if _, err := s.Search(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if q := spy.starts[0].query; !strings.HasSuffix(q, "| limit 10000") {
		t.Errorf("query ends %q, want | limit 10000", q[max(0, len(q)-30):])
	}
}

func TestSearcher_SkipsServicesWithoutPrefixes(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogLines("/g/a", []fake.LogLine{line("p/c/t", time.Minute, "h")})
	s, _ := newSearcher(f)
	res, err := s.Search(context.Background(), searchReq(
		svc("A", homelabAccount, "us-east-1", "/g/a", "p/c/"),
		svc("noprefix", homelabAccount, "us-east-1", "/g/b")))
	if err != nil {
		t.Fatalf("a service without prefixes must not fail the search: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].ServiceID != "A" {
		t.Errorf("rows = %+v", res.Rows)
	}
}

// staticRole is a one-account RoleSource for the integration test.
type staticRole struct{ account, arn string }

func (r staticRole) Role(_ context.Context, accountID string) (string, string, error) {
	if accountID != r.account {
		return "", "", awsx.ErrAccountUnknown
	}
	return r.arn, "", nil
}

// TestSearcherIT_RealInsights runs one composed query against a real log group.
// Requires HM_IT_AWS=1 plus HM_IT_ROLE_ARN, HM_IT_ACCOUNT, HM_IT_REGION, HM_IT_GROUP and HM_IT_PREFIX
// (ambient AWS credentials must be allowed to assume the role). Skipped by default.
func TestSearcherIT_RealInsights(t *testing.T) {
	if os.Getenv("HM_IT_AWS") != "1" {
		t.Skip("set HM_IT_AWS=1 to run against real AWS")
	}
	env := map[string]string{}
	for _, k := range []string{"HM_IT_ROLE_ARN", "HM_IT_ACCOUNT", "HM_IT_REGION", "HM_IT_GROUP", "HM_IT_PREFIX"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			t.Skipf("%s is required when HM_IT_AWS=1", k)
		}
	}
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(env["HM_IT_REGION"]))
	if err != nil {
		t.Fatal(err)
	}
	s := &Searcher{Clients: awsx.NewReal(cfg, staticRole{account: env["HM_IT_ACCOUNT"], arn: env["HM_IT_ROLE_ARN"]})}

	// Run through the raw API too so the @log and @logStream shape is asserted directly.
	target := awsx.Target{AccountID: env["HM_IT_ACCOUNT"], Region: env["HM_IT_REGION"]}
	query, err := ComposeQuery("", []StreamScope{{AccountID: target.AccountID, Group: env["HM_IT_GROUP"], Prefixes: []string{env["HM_IT_PREFIX"]}}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	api, err := s.Clients.Logs(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start, err := api.StartQuery(context.Background(), &cloudwatchlogs.StartQueryInput{
		LogGroupNames: []string{env["HM_IT_GROUP"]}, StartTime: aws.Int64(now.Add(-24 * time.Hour).Unix()),
		EndTime: aws.Int64(now.Unix()), QueryString: aws.String(query),
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]logstypes.ResultField
	for range 60 {
		out, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: start.QueryId})
		if err != nil {
			t.Fatal(err)
		}
		if out.Status == logstypes.QueryStatusComplete {
			rows = out.Results
			break
		}
		time.Sleep(time.Second)
	}
	if len(rows) == 0 {
		t.Fatal("no rows from the composed query")
	}
	for _, row := range rows {
		got := map[string]string{}
		for _, f := range row {
			got[aws.ToString(f.Field)] = aws.ToString(f.Value)
		}
		if got["@log"] != env["HM_IT_ACCOUNT"]+":"+env["HM_IT_GROUP"] || !strings.HasPrefix(got["@logStream"], env["HM_IT_PREFIX"]) {
			t.Errorf("@log=%q @logStream=%q, want %s:%s and prefix %s", got["@log"], got["@logStream"], env["HM_IT_ACCOUNT"], env["HM_IT_GROUP"], env["HM_IT_PREFIX"])
		}
	}

	res, err := s.Search(context.Background(), SearchRequest{
		Services: []SearchService{svc("it", target.AccountID, target.Region, env["HM_IT_GROUP"], env["HM_IT_PREFIX"])},
		From:     now.Add(-24 * time.Hour), To: now, MaxRows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 || res.Rows[0].ServiceID != "it" {
		t.Fatalf("rows = %+v targets = %+v", res.Rows, res.Targets)
	}
}
