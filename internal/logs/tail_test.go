package logs

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

// closeSpyClients wraps the fake so tests can see whether the AWS stream was closed.
type closeSpyClients struct {
	awsx.Clients
	closed atomic.Int32
}

type closeSpyLogs struct {
	awsx.LogsAPI
	spy *closeSpyClients
}

type closeSpyStream struct {
	awsx.LiveTailStream
	spy *closeSpyClients
}

func (c *closeSpyClients) Logs(ctx context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	api, err := c.Clients.Logs(ctx, t)
	return closeSpyLogs{LogsAPI: api, spy: c}, err
}

func (l closeSpyLogs) LiveTail(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput) (awsx.LiveTailStream, error) {
	s, err := l.LogsAPI.LiveTail(ctx, in)
	if err != nil {
		return nil, err
	}
	return closeSpyStream{LiveTailStream: s, spy: l.spy}, nil
}

func (s closeSpyStream) Close() error {
	s.spy.closed.Add(1)
	return s.LiveTailStream.Close()
}

func tailClient(interval time.Duration) (*Client, *closeSpyClients) {
	c, f := newTestClient()
	f.LiveTailInterval = interval
	spy := &closeSpyClients{Clients: f}
	c.Clients = spy
	return c, spy
}

func waitDone(t *testing.T, tail *Tail, within time.Duration) {
	t.Helper()
	select {
	case <-tail.Done():
	case <-time.After(within):
		t.Fatal("tail not done in time")
	}
}

func TestStartTail_BuildsInputAndRelaysLines(t *testing.T) {
	c, spy := tailClient(20 * time.Millisecond)
	f := spy.Clients.(*fake.Fake)
	f.Estate.SetLogLines(paymentsGroup, []fake.LogLine{
		{Stream: "ecs/payments-api/taskaaa", Timestamp: testNow.Add(-time.Second), Message: "upstream timeout after 5s"},
		{Stream: "ecs/payments-api/taskbbb", Timestamp: testNow.Add(-time.Second), Message: "unrelated line"},
	})
	tail, err := c.StartTail(context.Background(), testTarget, TailRequest{
		AccountID: testTarget.AccountID, Group: paymentsGroup, Region: testTarget.Region,
		Prefixes: []string{"ecs/payments-api/"}, Query: "timeout",
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	select {
	case batch := <-tail.Lines():
		if len(batch) == 0 {
			t.Fatal("empty batch")
		}
		if batch[0].Container != "payments-api" || batch[0].TaskID != "taskaaa" || batch[0].T.IsZero() || batch[0].Message != "upstream timeout after 5s" {
			t.Errorf("line not parsed: %+v", batch[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no batch within 3 s")
	}

	in := f.LastLiveTailInput()
	wantGroup := "arn:aws:logs:us-east-1:482109375512:log-group:/ecs/core-services/payments-api"
	if !slices.Equal(in.LogGroupIdentifiers, []string{wantGroup}) {
		t.Errorf("LogGroupIdentifiers = %v, want [%s]", in.LogGroupIdentifiers, wantGroup)
	}
	if !slices.Equal(in.LogStreamNamePrefixes, []string{"ecs/payments-api/"}) {
		t.Errorf("prefixes = %v", in.LogStreamNamePrefixes)
	}
	if got := aws.ToString(in.LogEventFilterPattern); got != FilterPattern("timeout") {
		t.Errorf("filter pattern = %q", got)
	}
}

func TestTail_IdleStopsAfterDuration(t *testing.T) {
	c, spy := tailClient(time.Hour)
	tail, err := c.StartTail(context.Background(), testTarget, TailRequest{Group: paymentsGroup, Region: testTarget.Region, AccountID: testTarget.AccountID}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, tail, 2*time.Second)
	if !errors.Is(tail.Err(), ErrIdle) {
		t.Errorf("Err = %v, want ErrIdle", tail.Err())
	}
	if spy.closed.Load() == 0 {
		t.Error("AWS stream not closed")
	}
	if _, open := <-tail.Lines(); open {
		t.Error("Lines should be closed")
	}
}

func TestTail_TouchExtendsIdle(t *testing.T) {
	c, _ := tailClient(time.Hour)
	tail, err := c.StartTail(context.Background(), testTarget, TailRequest{Group: paymentsGroup, Region: testTarget.Region, AccountID: testTarget.AccountID}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	for end := time.Now().Add(200 * time.Millisecond); time.Now().Before(end); {
		tail.Touch()
		select {
		case <-tail.Done():
			t.Fatalf("tail stopped despite Touch: %v", tail.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestTail_CloseIsIdempotent(t *testing.T) {
	c, spy := tailClient(10 * time.Millisecond)
	tail, err := c.StartTail(context.Background(), testTarget, TailRequest{Group: paymentsGroup, Region: testTarget.Region, AccountID: testTarget.AccountID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tail.Close()
	tail.Close()
	waitDone(t, tail, time.Second)
	if tail.Err() != nil {
		t.Errorf("Err after Close = %v, want nil", tail.Err())
	}
	if n := spy.closed.Load(); n != 1 {
		t.Errorf("stream closed %d times, want 1", n)
	}
	tail.Touch() // must not block or panic after the end
}

func TestTail_StreamErrorPropagates(t *testing.T) {
	c, f := newTestClient()
	f.LiveTailInterval = 10 * time.Millisecond
	f.LiveTailError = &logstypes.SessionStreamingException{Message: aws.String("SessionStreamingException")}
	f.LiveTailErrorAfter = 1
	tail, err := c.StartTail(context.Background(), testTarget, TailRequest{Group: paymentsGroup, Region: testTarget.Region, AccountID: testTarget.AccountID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	for range tail.Lines() { // drains until closed
	}
	waitDone(t, tail, time.Second)
	var streamErr *logstypes.SessionStreamingException
	if !errors.As(tail.Err(), &streamErr) {
		t.Errorf("Err = %v, want SessionStreamingException", tail.Err())
	}
}

func TestTail_ContextCancelEndsTail(t *testing.T) {
	c, _ := tailClient(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	tail, err := c.StartTail(ctx, testTarget, TailRequest{Group: paymentsGroup, Region: testTarget.Region, AccountID: testTarget.AccountID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitDone(t, tail, time.Second)
	if !errors.Is(tail.Err(), context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", tail.Err())
	}
}

func TestTailLimiter_MaxTwoPerUser(t *testing.T) {
	l := NewTailLimiter(2)
	r1, ok1 := l.Acquire(7)
	_, ok2 := l.Acquire(7)
	_, ok3 := l.Acquire(7)
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("acquire results %v %v %v, want true true false", ok1, ok2, ok3)
	}
	if _, ok := l.Acquire(8); !ok {
		t.Error("other user should not be limited")
	}
	r1()
	if l.Count(7) != 1 {
		t.Errorf("Count(7) = %d, want 1", l.Count(7))
	}
	if _, ok := l.Acquire(7); !ok {
		t.Error("acquire after release should succeed")
	}
	r1() // second release of the same slot must not go negative
	if l.Count(7) != 2 {
		t.Errorf("Count(7) = %d after double release, want 2", l.Count(7))
	}
}
