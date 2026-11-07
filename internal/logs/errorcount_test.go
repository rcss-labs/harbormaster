package logs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

const errorStreamPrefix = "ecs/payments-api"

// testClock is an injected clock whose Sleep advances time instead of waiting.
type testClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *testClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *testClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func newTestErrorCounter(now time.Time) (*ErrorCounter, *fake.Fake, *testClock) {
	clock := &testClock{now: now}
	f := fake.New(fake.MockupEstate(clock.Now))
	counter := NewErrorCounter(f, clock.Now)
	counter.Sleep = clock.Sleep
	return counter, f, clock
}

func errorLine(at time.Time, message string) fake.LogLine {
	return fake.LogLine{Stream: errorStreamPrefix + "/payments-api/task1", Timestamp: at, Message: message}
}

func baseErrorRequest(from, to time.Time) ErrorCountRequest {
	return ErrorCountRequest{Target: testTarget, Group: paymentsGroup, Prefixes: []string{errorStreamPrefix}, From: from, To: to, Bucket: 5 * time.Minute}
}

func throttleError() error {
	return &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
}

func TestErrorCounter_CountsOnlyErrorLevelPerBucket(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(30 * time.Minute))
	at := func(minute, second int) time.Time { return time.Date(2026, 10, 31, 11, minute, second, 0, time.UTC) }
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{
		errorLine(at(1, 0), "ERROR x"),
		errorLine(at(5, 0), "ERROR on the boundary"), // belongs to the 11:05 bucket, not 11:00
		errorLine(at(6, 0), "level=fatal y"),
		errorLine(at(7, 0), "no error found"),
		errorLine(at(8, 0), "WARN z"),
		errorLine(at(12, 0), "level=Critical x"), // CloudWatch terms are case-sensitive; Level is not
		errorLine(at(16, 0), "ERROR again"),
		{Stream: "other/worker/task9", Timestamp: at(1, 30), Message: "ERROR in another stream"},
	})

	got, err := counter.Count(context.Background(), baseErrorRequest(at(2, 0), at(17, 0)))
	if err != nil {
		t.Fatal(err)
	}
	wantStarts := []time.Time{at(0, 0), at(5, 0), at(10, 0), at(15, 0)}
	wantCounts := []int{1, 2, 1, 1}
	if len(got.Buckets) != len(wantStarts) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got.Buckets), len(wantStarts), got.Buckets)
	}
	for i, b := range got.Buckets {
		if !b.Start.Equal(wantStarts[i]) || b.Count != wantCounts[i] || !b.Known || b.Capped {
			t.Errorf("bucket %d = %+v, want start %v count %d known, not capped", i, b, wantStarts[i], wantCounts[i])
		}
	}
	if got.Partial || got.Stale {
		t.Errorf("Partial=%v Stale=%v, want both false", got.Partial, got.Stale)
	}
}

func TestErrorCounter_CapsPagesAndMarksCapped(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(30 * time.Minute))
	f.LogsPageSize = 1
	var lines []fake.LogLine
	for i := 0; i < 10; i++ {
		lines = append(lines, errorLine(testNow.Add(time.Duration(i)*time.Second), fmt.Sprintf("ERROR boom %d", i)))
	}
	f.Estate.SetLogLines(paymentsGroup, lines)

	got, err := counter.Count(context.Background(), baseErrorRequest(testNow, testNow.Add(4*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 1 || got.Buckets[0].Count != 3 || !got.Buckets[0].Capped || !got.Buckets[0].Known {
		t.Fatalf("buckets = %+v, want one known capped bucket with count 3", got.Buckets)
	}
	if calls := f.Calls()["FilterLogEvents"]; calls != 3 {
		t.Errorf("FilterLogEvents calls = %d, want 3", calls)
	}
}

func TestErrorCounter_BudgetLeavesOldestUnknown(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(3 * time.Hour))
	var lines []fake.LogLine
	for i := 0; i < 15; i++ {
		lines = append(lines, errorLine(testNow.Add(time.Duration(i)*5*time.Minute+time.Minute), "ERROR tick"))
	}
	f.Estate.SetLogLines(paymentsGroup, lines)
	req := baseErrorRequest(testNow, testNow.Add(75*time.Minute))
	req.Prefixes = []string{errorStreamPrefix, "ecs/b", "ecs/c"}

	got, err := counter.Count(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls()["FilterLogEvents"]; calls > 40 || calls == 0 {
		t.Errorf("FilterLogEvents calls = %d, want 1..40", calls)
	}
	if len(got.Buckets) != 15 {
		t.Fatalf("got %d buckets, want 15", len(got.Buckets))
	}
	if !got.Partial {
		t.Error("Partial = false, want true")
	}
	known := 0
	for i, b := range got.Buckets {
		if b.Known {
			known++
			if b.Count != 1 {
				t.Errorf("known bucket %d count = %d, want 1", i, b.Count)
			}
		}
	}
	if !got.Buckets[14].Known || got.Buckets[0].Known {
		t.Errorf("newest known=%v oldest known=%v, want newest known and oldest unknown", got.Buckets[14].Known, got.Buckets[0].Known)
	}
	for i := 1; i < len(got.Buckets); i++ {
		if got.Buckets[i-1].Known && !got.Buckets[i].Known {
			t.Errorf("known buckets are not the newest run: bucket %d known but %d unknown", i-1, i)
		}
	}
	if known == 0 || known == 15 {
		t.Errorf("known buckets = %d, want some but not all", known)
	}
}

func TestErrorCounter_PacesCallsPerTarget(t *testing.T) {
	counter, f, clock := newTestErrorCounter(testNow.Add(3 * time.Hour))
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(testNow.Add(time.Minute), "ERROR x")})

	if _, err := counter.Count(context.Background(), baseErrorRequest(testNow, testNow.Add(14*time.Minute))); err != nil { // 3 buckets, 3 calls
		t.Fatal(err)
	}
	sleeps := clock.Sleeps()
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v, want 2 (the first call of a target is not delayed, the next two wait)", sleeps)
	}
	for i, d := range sleeps {
		if d < 500*time.Millisecond {
			t.Errorf("sleep %d = %v, want at least 500ms between calls of one target", i, d)
		}
	}

	otherRegion := baseErrorRequest(testNow, testNow.Add(time.Minute))
	otherRegion.Region = "eu-west-1"
	if _, err := counter.Count(context.Background(), otherRegion); err != nil {
		t.Fatal(err)
	}
	if got := clock.Sleeps(); len(got) != 2 {
		t.Errorf("sleeps after a call for another region = %v, want unchanged", got)
	}
}

func TestErrorCounter_ClosedBucketsAreCached(t *testing.T) {
	// Now is 12:03. Buckets 11:45 .. 12:00; the 11:55 bucket ended 8 minutes ago (closed), 12:00 is open.
	now := time.Date(2026, 10, 31, 12, 3, 0, 0, time.UTC)
	counter, f, clock := newTestErrorCounter(now)
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(now.Add(-time.Minute), "ERROR x")})
	req := baseErrorRequest(time.Date(2026, 10, 31, 11, 30, 0, 0, time.UTC), now) // 11:30 .. 12:00 = 7 buckets

	first, err := counter.Count(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	firstCalls := f.Calls()["FilterLogEvents"]
	if len(first.Buckets) != 7 || firstCalls != 7 {
		t.Fatalf("buckets=%d calls=%d, want 7 and 7", len(first.Buckets), firstCalls)
	}

	clock.Advance(2 * time.Minute)
	if _, err := counter.Count(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The 11:55 bucket ended at 12:00, 3 minutes before the first fetch, and 12:00 was open: both were fresh for 60 s only.
	if extra := f.Calls()["FilterLogEvents"] - firstCalls; extra != 2 {
		t.Errorf("second Count made %d calls, want 2 (only the buckets that ended under 5 minutes before the first fetch)", extra)
	}
}

func TestErrorCounter_ThrottleServesStaleThenErrors(t *testing.T) {
	counter, f, clock := newTestErrorCounter(testNow.Add(3 * time.Hour))
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(testNow.Add(time.Minute), "ERROR x"), errorLine(testNow.Add(7*time.Minute), "ERROR y")})
	req := baseErrorRequest(testNow, testNow.Add(9*time.Minute))

	warm, err := counter.Count(context.Background(), req)
	if err != nil || warm.Stale || warm.Partial {
		t.Fatalf("warm: err=%v stale=%v partial=%v", err, warm.Stale, warm.Partial)
	}
	clock.Advance(90 * time.Minute) // past the 1h fresh time, inside the 1h stale window
	f.FailAlways("FilterLogEvents", throttleError())

	stale, err := counter.Count(context.Background(), req)
	if err != nil {
		t.Fatalf("stale: err = %v, want stale data without error", err)
	}
	if !stale.Stale || !stale.Partial {
		t.Errorf("Stale=%v Partial=%v, want both true", stale.Stale, stale.Partial)
	}
	for i, b := range stale.Buckets {
		if !b.Known || b.Count != warm.Buckets[i].Count {
			t.Errorf("bucket %d = %+v, want the warm count %d, known", i, b, warm.Buckets[i].Count)
		}
	}

	cold := NewErrorCounter(f, clock.Now)
	cold.Sleep = clock.Sleep
	if _, err := cold.Count(context.Background(), req); err == nil || !awsx.IsThrottle(err) {
		t.Errorf("cold err = %v, want the throttle error", err)
	}
}

func TestErrorCounter_TopMessageGroupsNumbers(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(30 * time.Minute))
	long := "ERROR " + strings.Repeat("é", 494) // 500 runes
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{
		errorLine(testNow.Add(1*time.Second), "ERROR timeout after 5000ms"),
		errorLine(testNow.Add(2*time.Second), "ERROR timeout after 12ms"),
		errorLine(testNow.Add(3*time.Second), "ERROR timeout after 731ms"),
		errorLine(testNow.Add(4*time.Second), "ERROR disk full"),
		errorLine(testNow.Add(5*time.Minute+5*time.Second), "ERROR lost session 3f9a1c2e-abcd-4def-8abc-0123456789ab"),
		errorLine(testNow.Add(5*time.Minute+6*time.Second), "ERROR lost session 77b0cafe-dead-4bee-9fed-fedcba987654"),
		errorLine(testNow.Add(5*time.Minute+7*time.Second), "ERROR lost session aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"),
		errorLine(testNow.Add(5*time.Minute+8*time.Second), "ERROR lost session 0a1b2c3d-4e5f-4a6b-8c7d-9e8f7a6b5c4d"),
		errorLine(testNow.Add(5*time.Minute+9*time.Second), "ERROR cache miss"),
		errorLine(testNow.Add(5*time.Minute+10*time.Second), "ERROR cache miss"),
		errorLine(testNow.Add(10*time.Minute+time.Second), long),
	})

	got, err := counter.Count(context.Background(), baseErrorRequest(testNow, testNow.Add(11*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(got.Buckets))
	}
	if top := got.Buckets[0].TopMessage; !strings.HasPrefix(top, "ERROR timeout after ") || got.Buckets[0].Count != 4 {
		t.Errorf("TopMessage = %q count %d, want one timeout sample and count 4", top, got.Buckets[0].Count)
	}
	// Four lines differing only by UUID outnumber a message seen twice only if UUIDs group.
	if top := got.Buckets[1].TopMessage; !strings.HasPrefix(top, "ERROR lost session ") || got.Buckets[1].Count != 6 {
		t.Errorf("TopMessage = %q count %d, want a lost-session sample (UUIDs grouped) and count 6", top, got.Buckets[1].Count)
	}
	top := got.Buckets[2].TopMessage
	if n := len([]rune(top)); n != 200 || top != string([]rune(long)[:200]) {
		t.Errorf("long TopMessage has %d runes, want the first 200 intact", n)
	}
}

type optionRecordingClients struct {
	*fake.Fake
	mu          sync.Mutex
	maxAttempts []int
}

func (c *optionRecordingClients) Logs(ctx context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	api, err := c.Fake.Logs(ctx, t)
	return &optionRecordingLogs{LogsAPI: api, clients: c}, err
}

type optionRecordingLogs struct {
	awsx.LogsAPI
	clients *optionRecordingClients
}

func (l *optionRecordingLogs) FilterLogEvents(ctx context.Context, in *cloudwatchlogs.FilterLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	var applied cloudwatchlogs.Options
	for _, opt := range opts {
		opt(&applied)
	}
	l.clients.mu.Lock()
	l.clients.maxAttempts = append(l.clients.maxAttempts, applied.RetryMaxAttempts)
	l.clients.mu.Unlock()
	return l.LogsAPI.FilterLogEvents(ctx, in, opts...)
}

func TestErrorCounter_DisablesSDKRetries(t *testing.T) {
	counter, f, clock := newTestErrorCounter(testNow.Add(30 * time.Minute))
	recorder := &optionRecordingClients{Fake: f}
	counter.Clients = recorder
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(testNow.Add(time.Minute), "ERROR x")})
	_ = clock

	if _, err := counter.Count(context.Background(), baseErrorRequest(testNow, testNow.Add(9*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(recorder.maxAttempts) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(recorder.maxAttempts))
	}
	for i, attempts := range recorder.maxAttempts {
		if attempts != 1 {
			t.Errorf("call %d RetryMaxAttempts = %d, want 1 so retries cannot bypass the pacer and call budget", i, attempts)
		}
	}
}

func TestErrorCounter_SecondViewerSurvivesFirstViewerCancel(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(30 * time.Minute))
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(testNow.Add(time.Minute), "ERROR x"), errorLine(testNow.Add(7*time.Minute), "ERROR y")})
	held := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once
	counter.Sleep = func(ctx context.Context, _ time.Duration) error { // the scan's second call waits here
		holdOnce.Do(func() { close(held) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req := baseErrorRequest(testNow, testNow.Add(9*time.Minute)) // two buckets

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	type outcome struct {
		counts ErrorCounts
		err    error
	}
	firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() { c, err := counter.Count(firstCtx, req); firstDone <- outcome{c, err} }()
	<-held
	go func() { c, err := counter.Count(context.Background(), req); secondDone <- outcome{c, err} }()
	time.Sleep(50 * time.Millisecond) // let the second viewer join the running scan

	cancelFirst()
	if got := <-firstDone; !errors.Is(got.err, context.Canceled) {
		t.Errorf("first viewer err = %v, want context.Canceled", got.err)
	}
	close(release)
	second := <-secondDone
	if second.err != nil || len(second.counts.Buckets) != 2 || !second.counts.Buckets[0].Known || !second.counts.Buckets[1].Known {
		t.Fatalf("second viewer = %+v, err %v, want both buckets known", second.counts, second.err)
	}
	if calls := f.Calls()["FilterLogEvents"]; calls != 2 {
		t.Errorf("FilterLogEvents calls = %d, want 2 (one shared scan)", calls)
	}
}

func TestErrorCounter_EvictsExpiredEntriesBeforeFreshOnes(t *testing.T) {
	now := testNow
	counter, _, _ := newTestErrorCounter(now)
	entry := func(expiresAt time.Time) cachedBucket {
		return cachedBucket{bucket: ErrorBucket{Known: true}, fetchedAt: now, expiresAt: expiresAt}
	}
	for i := 0; i < maxCacheEntries/2; i++ {
		counter.cache[bucketKey{group: "expired", start: int64(i)}] = entry(now.Add(-10 * time.Minute))
		counter.cache[bucketKey{group: "fresh", start: int64(i)}] = entry(now.Add(10 * time.Minute))
	}

	counter.store(bucketKey{group: "new"}, ErrorBucket{Known: true}, now, 5*time.Minute)

	if len(counter.cache) > maxCacheEntries {
		t.Errorf("cache holds %d entries, want at most %d", len(counter.cache), maxCacheEntries)
	}
	fresh := 0
	for key := range counter.cache {
		if key.group == "fresh" {
			fresh++
		}
	}
	if fresh != maxCacheEntries/2 {
		t.Errorf("%d fresh entries survive, want all %d", fresh, maxCacheEntries/2)
	}
	if _, ok := counter.cache[bucketKey{group: "new"}]; !ok {
		t.Error("the newly stored entry was evicted")
	}
}

func TestErrorCounter_FetchTimeIsTakenBeforeTheBucketScan(t *testing.T) {
	// The 12:00 bucket ended at 12:05; at 12:09:58 it is still settling. Six prefixes make the scan take
	// 2.5 s of paced waiting, so the bucket would look settled if the fetch time were taken afterwards.
	now := time.Date(2026, 10, 31, 12, 9, 58, 0, time.UTC)
	counter, f, clock := newTestErrorCounter(now)
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{errorLine(time.Date(2026, 10, 31, 12, 1, 0, 0, time.UTC), "ERROR x")})
	req := baseErrorRequest(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 12, 4, 0, 0, time.UTC))
	req.Prefixes = []string{errorStreamPrefix, "p2", "p3", "p4", "p5", "p6"}

	if _, err := counter.Count(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	firstCalls := f.Calls()["FilterLogEvents"]
	clock.Advance(2 * time.Minute)
	if _, err := counter.Count(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if f.Calls()["FilterLogEvents"] == firstCalls {
		t.Error("second Count two minutes later made no calls, want a refetch: the bucket was settling when it was read")
	}
}

func TestErrorCounter_RejectsSubMillisecondBucket(t *testing.T) {
	counter, _, _ := newTestErrorCounter(testNow)
	req := baseErrorRequest(testNow, testNow.Add(time.Minute))
	req.Bucket = 500 * time.Microsecond
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Count panicked: %v; want an error", r)
		}
	}()
	if _, err := counter.Count(context.Background(), req); err == nil {
		t.Error("err = nil, want an error for a bucket under one millisecond")
	}
}

func TestErrorCounter_BudgetStopWithNothingKnownReturnsBudgetError(t *testing.T) {
	counter, f, _ := newTestErrorCounter(testNow.Add(time.Hour))
	req := baseErrorRequest(testNow, testNow.Add(4*time.Minute))
	for i := 0; i < maxCallsPerRequest+1; i++ { // one bucket needs more calls than a request may make
		req.Prefixes = append(req.Prefixes, fmt.Sprintf("ecs/p%d", i))
	}
	if _, err := counter.Count(context.Background(), req); !errors.Is(err, errCallBudget) {
		t.Errorf("err = %v, want the call budget error", err)
	}
	if calls := f.Calls()["FilterLogEvents"]; calls != 0 {
		t.Errorf("FilterLogEvents calls = %d, want 0", calls)
	}
}
