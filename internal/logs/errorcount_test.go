package logs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
		errorLine(at(16, 0), "ERROR again"),
		{Stream: "other/worker/task9", Timestamp: at(1, 30), Message: "ERROR in another stream"},
	})

	got, err := counter.Count(context.Background(), baseErrorRequest(at(2, 0), at(17, 0)))
	if err != nil {
		t.Fatal(err)
	}
	wantStarts := []time.Time{at(0, 0), at(5, 0), at(10, 0), at(15, 0)}
	wantCounts := []int{1, 2, 0, 1}
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
	// Buckets 11:55 (ended 12:00, 3 min before the first fetch) and 12:00 (open) were fresh for 60 s only.
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
	top := got.Buckets[2].TopMessage
	if n := len([]rune(top)); n != 200 || top != string([]rune(long)[:200]) {
		t.Errorf("long TopMessage has %d runes, want the first 200 intact", n)
	}
}
