package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const insightsGroup = "/ecs/test/shared"

// seedInsights writes lines for two services into one group: api/ and worker/ streams.
func seedInsights(f *Fake) []LogLine {
	var lines []LogLine
	msgs := []string{"GET /health 200", "request timeout after 30s", "upstream time-out", "GET /orders 200", "job done", "connection reset"}
	for i, m := range msgs {
		lines = append(lines,
			LogLine{Stream: "api/web/t1", Timestamp: fixedNow.Add(-time.Duration(i+1) * time.Minute), Message: m},
			LogLine{Stream: "worker/job/t2", Timestamp: fixedNow.Add(-time.Duration(i+1)*time.Minute - time.Second), Message: m},
		)
	}
	f.Estate.SetLogLines(insightsGroup, lines)
	return lines
}

func startInsights(t *testing.T, api awsx.LogsAPI, query string) (string, error) {
	t.Helper()
	out, err := api.StartQuery(context.Background(), &cloudwatchlogs.StartQueryInput{
		LogGroupNames: []string{insightsGroup},
		StartTime:     aws.Int64(fixedNow.Add(-time.Hour).Unix()),
		EndTime:       aws.Int64(fixedNow.Unix()),
		QueryString:   aws.String(query),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.QueryId), nil
}

func completeInsights(t *testing.T, api awsx.LogsAPI, id string) *cloudwatchlogs.GetQueryResultsOutput {
	t.Helper()
	for range 3 {
		out, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(id)})
		if err != nil {
			t.Fatal(err)
		}
		if out.Status == logstypes.QueryStatusComplete {
			return out
		}
	}
	t.Fatal("query never completed")
	return nil
}

func rowField(row []logstypes.ResultField, name string) string {
	for _, f := range row {
		if aws.ToString(f.Field) == name {
			return aws.ToString(f.Value)
		}
	}
	return ""
}

func TestStartQuery_RunsThenCompletesWithStats(t *testing.T) {
	f := newFake()
	lines := seedInsights(f)
	api := logsFor(t, f, homelabUSEast1)
	id, err := startInsights(t, api, "filter @message like \"GET\"\n| fields @timestamp, @message, @log, @logStream\n| sort @timestamp desc\n| limit 100")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "fakeq-") {
		t.Errorf("query id %q, want fakeq- prefix", id)
	}
	first, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(id)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != logstypes.QueryStatusRunning || len(first.Results) != 0 {
		t.Fatalf("first poll: status %s, %d rows; want Running and none", first.Status, len(first.Results))
	}
	second, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(id)})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != logstypes.QueryStatusComplete {
		t.Fatalf("second poll status %s, want Complete", second.Status)
	}
	if got := second.Statistics.RecordsMatched; got != 4 {
		t.Errorf("RecordsMatched = %v, want 4 (two GET lines in two streams)", got)
	}
	if got := second.Statistics.RecordsScanned; got != float64(len(lines)) {
		t.Errorf("RecordsScanned = %v, want %d", got, len(lines))
	}
	var wantBytes float64
	for _, l := range lines {
		wantBytes += float64(len(l.Message) + 26)
	}
	if got := second.Statistics.BytesScanned; got != wantBytes {
		t.Errorf("BytesScanned = %v, want %v", got, wantBytes)
	}
	if len(second.Results) != 4 {
		t.Fatalf("%d rows, want 4", len(second.Results))
	}
	row := second.Results[0]
	if rowField(row, "@timestamp") != "2026-10-31 11:59:00.000" || rowField(row, "@log") != homelabID+":"+insightsGroup ||
		rowField(row, "@logStream") != "api/web/t1" || rowField(row, "@ptr") == "" {
		t.Errorf("newest row = %v", row)
	}
}

func scopeQuery(account string, prefixes ...string) string {
	return fmt.Sprintf("filter (@log = %q and @logStream like /^(%s)/)\n| fields @timestamp, @message, @log, @logStream\n| sort @timestamp desc\n| limit 100",
		account+":"+insightsGroup, strings.Join(prefixes, "|"))
}

func TestStartQuery_ScopeStageSelectsStreams(t *testing.T) {
	f := newFake()
	seedInsights(f)
	api := logsFor(t, f, homelabUSEast1)

	id, err := startInsights(t, api, scopeQuery(homelabID, `worker\/`))
	if err != nil {
		t.Fatal(err)
	}
	out := completeInsights(t, api, id)
	if len(out.Results) != 6 {
		t.Fatalf("%d rows, want the 6 worker lines", len(out.Results))
	}
	for _, row := range out.Results {
		if !strings.HasPrefix(rowField(row, "@logStream"), "worker/") {
			t.Errorf("stream %q leaked through the scope", rowField(row, "@logStream"))
		}
	}

	// Two prefixes, a second clause for another group (matches nothing here), and a user stage after the scope.
	q := fmt.Sprintf("filter (@log = %q and @logStream like /^(worker\\/|api\\/)/) or (@log = %q and @logStream like /^(x)/)\n| filter @message like \"job\"\n| fields @timestamp, @message, @log, @logStream\n| sort @timestamp desc\n| limit 100",
		homelabID+":"+insightsGroup, homelabID+":/other")
	id, err = startInsights(t, api, q)
	if err != nil {
		t.Fatal(err)
	}
	if out = completeInsights(t, api, id); len(out.Results) != 2 {
		t.Errorf("%d rows, want 2 (job done in both streams)", len(out.Results))
	}

	// A scope naming another account matches nothing.
	id, err = startInsights(t, api, scopeQuery("999999999999", `worker\/`))
	if err != nil {
		t.Fatal(err)
	}
	if out = completeInsights(t, api, id); len(out.Results) != 0 {
		t.Errorf("%d rows for a foreign account, want 0", len(out.Results))
	}
}

func TestStartQuery_MessageFilterAndLimit(t *testing.T) {
	f := newFake()
	seedInsights(f)
	api := logsFor(t, f, homelabUSEast1)
	cases := []struct {
		name, stage string
		want        int
	}{
		{"regex", `filter @message like /time.?out/`, 4},
		{"text", `filter @message like "GET /health"`, 2},
		{"not like text", `filter @message not like "GET"`, 8},
		{"not like regex", `filter @message not like /time.?out/`, 8},
	}
	for _, tc := range cases {
		id, err := startInsights(t, api, tc.stage+"\n| fields @timestamp, @message\n| sort @timestamp desc\n| limit 100")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := len(completeInsights(t, api, id).Results); got != tc.want {
			t.Errorf("%s: %d rows, want %d", tc.name, got, tc.want)
		}
	}

	id, err := startInsights(t, api, "filter @message like /time.?out/\n| fields @timestamp\n| sort @timestamp desc\n| limit 3")
	if err != nil {
		t.Fatal(err)
	}
	out := completeInsights(t, api, id)
	if len(out.Results) != 3 || out.Statistics.RecordsMatched != 4 {
		t.Errorf("limit 3: %d rows, matched %v; want 3 rows and 4 matched", len(out.Results), out.Statistics.RecordsMatched)
	}
	if rowField(out.Results[0], "@timestamp") < rowField(out.Results[2], "@timestamp") {
		t.Error("rows are not newest first")
	}

	// A filter the fake cannot read fails loudly instead of matching everything.
	_, err = startInsights(t, api, "filter @message =~ \"x\"\n| limit 10")
	var malformed *logstypes.MalformedQueryException
	if !errors.As(err, &malformed) {
		t.Errorf("unreadable filter: err = %v, want MalformedQueryException", err)
	}
}

func TestStartQuery_Validation(t *testing.T) {
	f := newFake()
	seedInsights(f)
	api := logsFor(t, f, homelabUSEast1)
	base := func() *cloudwatchlogs.StartQueryInput {
		return &cloudwatchlogs.StartQueryInput{LogGroupNames: []string{insightsGroup}, StartTime: aws.Int64(100), EndTime: aws.Int64(200), QueryString: aws.String("limit 1")}
	}
	bad := map[string]func(*cloudwatchlogs.StartQueryInput){
		"no groups":   func(in *cloudwatchlogs.StartQueryInput) { in.LogGroupNames = nil },
		"end<=start":  func(in *cloudwatchlogs.StartQueryInput) { in.EndTime = aws.Int64(100) },
		"empty query": func(in *cloudwatchlogs.StartQueryInput) { in.QueryString = aws.String(" ") },
	}
	for name, mutate := range bad {
		in := base()
		mutate(in)
		var invalid *logstypes.InvalidParameterException
		if _, err := api.StartQuery(context.Background(), in); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want InvalidParameterException", name, err)
		}
	}
}

func TestStartQuery_RejectsTooManyGroups(t *testing.T) {
	f := newFake()
	api := logsFor(t, f, homelabUSEast1)
	var groups []string
	for i := range 51 {
		groups = append(groups, fmt.Sprintf("/ecs/g/%d", i))
	}
	_, err := api.StartQuery(context.Background(), &cloudwatchlogs.StartQueryInput{
		LogGroupNames: groups, StartTime: aws.Int64(100), EndTime: aws.Int64(200), QueryString: aws.String("limit 1"),
	})
	var invalid *logstypes.InvalidParameterException
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want InvalidParameterException", err)
	}
}

func TestStartQuery_FailureKnobsAndCalls(t *testing.T) {
	f := newFake()
	seedInsights(f)
	api := logsFor(t, f, homelabUSEast1)
	boom := errors.New("boom")
	f.FailNext("StartQuery", boom)
	if _, err := startInsights(t, api, "limit 1"); !errors.Is(err, boom) {
		t.Fatalf("FailNext: err = %v", err)
	}
	f.ThrottleLogs = 1
	if _, err := startInsights(t, api, "limit 1"); err == nil || !strings.Contains(err.Error(), "Rate exceeded") {
		t.Fatalf("throttle knob: err = %v", err)
	}
	if _, err := startInsights(t, api, "limit 1"); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()["StartQuery"]; got != 3 {
		t.Errorf("Calls()[StartQuery] = %d, want 3", got)
	}
	_, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String("nope")})
	var notFound *logstypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		t.Errorf("unknown id: err = %v, want ResourceNotFoundException", err)
	}
}

func TestStartQuery_AlwaysRunning(t *testing.T) {
	f := newFake()
	seedInsights(f)
	f.InsightsAlwaysRunning = true
	api := logsFor(t, f, homelabUSEast1)
	id, _ := startInsights(t, api, "limit 1")
	for range 4 {
		out, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(id)})
		if err != nil || out.Status != logstypes.QueryStatusRunning {
			t.Fatalf("status %v err %v, want Running", out, err)
		}
	}
}

func TestStopQuery_CancelsAndIsRecorded(t *testing.T) {
	f := newFake()
	seedInsights(f)
	f.InsightsAlwaysRunning = true
	api := logsFor(t, f, homelabUSEast1)
	id, err := startInsights(t, api, "limit 1")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := api.StopQuery(context.Background(), &cloudwatchlogs.StopQueryInput{QueryId: aws.String(id)})
	if err != nil || !stop.Success {
		t.Fatalf("StopQuery: %v, %v", stop, err)
	}
	out, err := api.GetQueryResults(context.Background(), &cloudwatchlogs.GetQueryResultsInput{QueryId: aws.String(id)})
	if err != nil || out.Status != logstypes.QueryStatusCancelled {
		t.Fatalf("after stop: %v, %v; want Cancelled", out, err)
	}
	if got := f.StoppedQueries(); len(got) != 1 || got[0] != id {
		t.Errorf("StoppedQueries() = %v, want [%s]", got, id)
	}
	var notFound *logstypes.ResourceNotFoundException
	if _, err := api.StopQuery(context.Background(), &cloudwatchlogs.StopQueryInput{QueryId: aws.String("nope")}); !errors.As(err, &notFound) {
		t.Errorf("unknown id: err = %v, want ResourceNotFoundException", err)
	}
}
