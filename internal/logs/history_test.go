package logs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

var (
	testNow    = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	testTarget = awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
)

const paymentsGroup = "/ecs/core-services/payments-api"

func newTestClient() (*Client, *fake.Fake) {
	f := fake.New(fake.MockupEstate(func() time.Time { return testNow }))
	return &Client{Clients: f, Now: func() time.Time { return testNow }, MaxLookback: 7 * 24 * time.Hour}, f
}

func TestHistory_NewestFirstFiveHundredAndOlderCursor(t *testing.T) {
	c, f := newTestClient()
	var lines []fake.LogLine
	for i := 0; i < 2400; i++ { // 3 tasks, one line per 9 s each: a line every 3 s over 2 h
		lines = append(lines, fake.LogLine{
			Stream:    fmt.Sprintf("ecs/payments-api/task%d", i%3),
			Timestamp: testNow.Add(-time.Duration(i) * 3 * time.Second),
			Message:   fmt.Sprintf("line %d", i),
		})
	}
	f.Estate.SetLogLines(paymentsGroup, lines)

	page, err := c.History(context.Background(), testTarget, HistoryRequest{Group: paymentsGroup})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 500 || !page.Truncated || page.OlderBefore == nil {
		t.Fatalf("lines=%d truncated=%v older=%v; want 500, true, non-nil", len(page.Lines), page.Truncated, page.OlderBefore)
	}
	for i := 1; i < len(page.Lines); i++ {
		if page.Lines[i].T.Before(page.Lines[i-1].T) {
			t.Fatalf("lines not ascending at %d", i)
		}
	}
	if page.Lines[len(page.Lines)-1].Message != "line 0" {
		t.Errorf("newest line = %q, want line 0", page.Lines[len(page.Lines)-1].Message)
	}
	if want := page.Lines[0].T.Add(-time.Millisecond); !page.OlderBefore.Equal(want) {
		t.Errorf("OlderBefore = %v, want %v", page.OlderBefore, want)
	}
	first := page.Lines[0].T

	older, err := c.History(context.Background(), testTarget, HistoryRequest{Group: paymentsGroup, Before: *page.OlderBefore})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Lines) != 500 {
		t.Fatalf("second page has %d lines, want 500", len(older.Lines))
	}
	if !older.Lines[len(older.Lines)-1].T.Before(first) {
		t.Errorf("second page overlaps the first: newest %v, first page oldest %v", older.Lines[len(older.Lines)-1].T, first)
	}
	if older.Lines[0].Container != "payments-api" || older.Lines[0].TaskID == "" {
		t.Errorf("line not parsed: %+v", older.Lines[0])
	}
}

func TestHistory_WidensWindowsUntilEnough(t *testing.T) {
	c, f := newTestClient()
	var lines []fake.LogLine
	for i := 0; i < 20; i++ {
		lines = append(lines, fake.LogLine{
			Stream:    "ecs/payments-api/taskaaa",
			Timestamp: testNow.Add(-time.Duration(i) * 3 * time.Hour),
			Message:   fmt.Sprintf("line %d", i),
		})
	}
	f.Estate.SetLogLines(paymentsGroup, lines)

	page, err := c.History(context.Background(), testTarget, HistoryRequest{Group: paymentsGroup})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 20 || page.OlderBefore != nil || page.Truncated {
		t.Fatalf("lines=%d older=%v truncated=%v; want 20, nil, false", len(page.Lines), page.OlderBefore, page.Truncated)
	}
	if calls := f.Calls()["FilterLogEvents"]; calls < 5 {
		t.Errorf("FilterLogEvents called %d times, want >= 5 widening windows", calls)
	}
}

func TestHistory_PrefixPerContainer(t *testing.T) {
	c, _ := newTestClient()
	page, err := c.History(context.Background(), testTarget, HistoryRequest{
		Group: paymentsGroup, Prefixes: []string{"ecs/otel-collector/"}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) == 0 {
		t.Fatal("no sidecar lines returned")
	}
	for _, l := range page.Lines {
		if l.Container != "otel-collector" {
			t.Fatalf("line from container %q, want only otel-collector", l.Container)
		}
	}
}

func TestHistory_MultiplePrefixesMergeAscending(t *testing.T) {
	c, _ := newTestClient()
	page, err := c.History(context.Background(), testTarget, HistoryRequest{
		Group: paymentsGroup, Prefixes: []string{"ecs/otel-collector/", "ecs/payments-api/"}, Limit: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, l := range page.Lines {
		seen[l.Container] = true
		if i > 0 && l.T.Before(page.Lines[i-1].T) {
			t.Fatalf("not ascending at %d", i)
		}
	}
	if !seen["otel-collector"] || !seen["payments-api"] {
		t.Errorf("containers seen %v, want both", seen)
	}
}
