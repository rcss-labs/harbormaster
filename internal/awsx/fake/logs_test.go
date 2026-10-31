package fake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const paymentsLogGroup = "/ecs/core-services/payments-api"

func logsFor(t *testing.T, f *Fake, target awsx.Target) awsx.LogsAPI {
	t.Helper()
	api, err := f.Logs(context.Background(), target)
	if err != nil {
		t.Fatalf("Logs(%v): %v", target, err)
	}
	return api
}

func TestFakeFilterLogEvents_PagesNewestLast(t *testing.T) {
	api := logsFor(t, newFake(), homelabUSEast1)
	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        aws.String(paymentsLogGroup),
		LogStreamNamePrefix: aws.String("ecs/payments-api/"),
		StartTime:           aws.Int64(fixedNow.Add(-time.Hour).UnixMilli()),
		EndTime:             aws.Int64(fixedNow.UnixMilli()),
	}
	first, err := api.FilterLogEvents(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 100 || first.NextToken == nil {
		t.Fatalf("page 1: %d events, token %v; want 100 and a token", len(first.Events), first.NextToken)
	}
	total := 0
	var last int64
	for page, token := 1, first.NextToken; ; page++ {
		out := first
		if page > 1 {
			in.NextToken = token
			var err error
			if out, err = api.FilterLogEvents(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			token = out.NextToken
		}
		for _, ev := range out.Events {
			ts := aws.ToInt64(ev.Timestamp)
			if ts < last {
				t.Fatalf("events not ascending: %d after %d", ts, last)
			}
			last = ts
			if !strings.HasPrefix(aws.ToString(ev.LogStreamName), "ecs/payments-api/") {
				t.Fatalf("stream %q lacks the requested prefix", aws.ToString(ev.LogStreamName))
			}
			total++
		}
		if token == nil {
			break
		}
	}
	// 3 running tasks, one line per 9 s each, 80 % on the main container.
	if total < 800 || total > 1200 {
		t.Fatalf("total events = %d, want roughly 3 tasks x 400 x 0.8", total)
	}
}

func TestFakeFilterLogEvents_StreamNamesUseRealTaskIDs(t *testing.T) {
	f := newFake()
	api := logsFor(t, f, homelabUSEast1)
	out, err := api.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(paymentsLogGroup),
		StartTime:    aws.Int64(fixedNow.Add(-time.Hour).UnixMilli()),
		EndTime:      aws.Int64(fixedNow.UnixMilli()),
	})
	if err != nil {
		t.Fatal(err)
	}
	running := map[string]bool{}
	for _, task := range f.Estate.Tasks {
		if aws.ToString(task.Task.LastStatus) == "RUNNING" && aws.ToString(task.Task.Group) == "service:payments-api" {
			running[lastSegment(aws.ToString(task.Task.TaskArn))] = true
		}
	}
	for _, ev := range out.Events {
		parts := strings.Split(aws.ToString(ev.LogStreamName), "/")
		if len(parts) != 3 || parts[0] != "ecs" || !running[parts[2]] {
			t.Fatalf("stream %q is not ecs/<container>/<running task id>", aws.ToString(ev.LogStreamName))
		}
	}
}

func TestFakeFilterLogEvents_GeneratedLinesCarryWarnAndErrorTokens(t *testing.T) {
	api := logsFor(t, newFake(), homelabUSEast1)
	out, err := api.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(paymentsLogGroup),
		StartTime:    aws.Int64(fixedNow.Add(-time.Hour).UnixMilli()),
		EndTime:      aws.Int64(fixedNow.UnixMilli()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var warn, errs int
	for _, ev := range out.Events {
		switch {
		case strings.HasPrefix(aws.ToString(ev.Message), "WARN "):
			warn++
		case strings.HasPrefix(aws.ToString(ev.Message), "ERROR "):
			errs++
		}
	}
	if warn == 0 || errs == 0 {
		t.Fatalf("warn=%d error=%d in first page, want both > 0", warn, errs)
	}
}

func TestFakeFilterLogEvents_UnknownGroupIsNotFound(t *testing.T) {
	api := logsFor(t, newFake(), homelabUSEast1)
	_, err := api.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String("/ecs/nope/nope")})
	var notFound *logstypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want ResourceNotFoundException", err)
	}
}

func TestFakeFilterLogEvents_ExplicitLinesOverrideGeneratedPool(t *testing.T) {
	f := newFake()
	var lines []LogLine
	for i := 0; i < 20; i++ {
		lines = append(lines, LogLine{
			Stream:    "ecs/payments-api/taskaaa",
			Timestamp: fixedNow.Add(-time.Duration(i) * 3 * time.Hour),
			Message:   "line " + itoa(i),
		})
	}
	f.Estate.SetLogLines(paymentsLogGroup, lines)
	api := logsFor(t, f, homelabUSEast1)
	out, err := api.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(paymentsLogGroup),
		StartTime:    aws.Int64(fixedNow.Add(-72 * time.Hour).UnixMilli()),
		EndTime:      aws.Int64(fixedNow.UnixMilli()),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 3 h spacing: offsets 0..69 h fall inside 72 h, so 20 lines (0..57 h).
	if len(out.Events) != 20 || aws.ToString(out.Events[19].Message) != "line 0" {
		t.Fatalf("got %d events, last %q; want 20 ascending with line 0 last", len(out.Events), aws.ToString(out.Events[len(out.Events)-1].Message))
	}
	f.Estate.SetLogLines(paymentsLogGroup, nil)
	out, _ = api.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(paymentsLogGroup),
		StartTime:    aws.Int64(fixedNow.Add(-time.Hour).UnixMilli()),
		EndTime:      aws.Int64(fixedNow.UnixMilli()),
	})
	if len(out.Events) != 100 {
		t.Fatalf("after clearing the override got %d events, want a generated first page of 100", len(out.Events))
	}
}

func TestFakeLiveTail_EmitsAndCloses(t *testing.T) {
	f := newFake()
	f.LiveTailInterval = 20 * time.Millisecond
	api := logsFor(t, f, homelabUSEast1)
	in := &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: []string{paymentsLogGroup}}
	stream, err := api.LiveTail(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	gotUpdate := false
	for !gotUpdate {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				t.Fatal("stream closed before any update")
			}
			if u, isUpdate := ev.(*logstypes.StartLiveTailResponseStreamMemberSessionUpdate); isUpdate {
				if n := len(u.Value.SessionResults); n < 1 || n > 3 {
					t.Fatalf("update has %d lines, want 1-3", n)
				}
				gotUpdate = true
			}
		case <-deadline:
			t.Fatal("no SessionUpdate within 3 s")
		}
	}
	if got := f.LastLiveTailInput(); got == nil || len(got.LogGroupIdentifiers) != 1 || got.LogGroupIdentifiers[0] != paymentsLogGroup {
		t.Fatalf("recorded input = %+v", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	drainDeadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-stream.Events():
			if !ok {
				return
			}
		case <-drainDeadline:
			t.Fatal("Events() not closed after Close()")
		}
	}
}

func TestFakeLiveTail_InjectedStreamErrorSurfacesAfterClose(t *testing.T) {
	f := newFake()
	f.LiveTailInterval = 10 * time.Millisecond
	f.LiveTailError = &logstypes.SessionStreamingException{Message: aws.String("SessionStreamingException")}
	api := logsFor(t, f, homelabUSEast1)
	stream, err := api.LiveTail(context.Background(), &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: []string{paymentsLogGroup}})
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.After(3 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-stream.Events():
		case <-timeout:
			t.Fatal("stream did not end")
		}
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "SessionStreamingException") {
		t.Fatalf("Err() = %v, want SessionStreamingException", err)
	}
}
