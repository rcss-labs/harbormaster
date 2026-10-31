package logs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// ErrIdle is Tail.Err after the idle stop.
var ErrIdle = errors.New("live tail stopped after 30 minutes idle")

// TailRequest selects what a live tail follows. Prefixes are log stream name
// prefixes (see StreamPrefix); Query is free text turned into a filter pattern.
type TailRequest struct {
	AccountID     string
	Group, Region string
	Prefixes      []string
	Query         string
}

// Tail relays one CloudWatch Logs Live Tail session. A single goroutine owns
// the AWS stream; it ends on stream end or error, idle timeout, Close or
// cancellation of the context given to StartTail.
type Tail struct {
	lines     chan []Line
	touch     chan struct{}
	closeReq  chan struct{}
	closeOnce sync.Once
	done      chan struct{}
	err       error // written before done is closed
}

// StartTail opens a live tail. The tail stops after idle without a Touch.
func (c *Client) StartTail(ctx context.Context, t awsx.Target, req TailRequest, idle time.Duration) (*Tail, error) {
	api, err := c.Clients.Logs(ctx, t)
	if err != nil {
		return nil, err
	}
	in := &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers:   []string{fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s", req.Region, req.AccountID, req.Group)},
		LogEventFilterPattern: aws.String(FilterPattern(req.Query)),
	}
	if len(req.Prefixes) > 0 {
		in.LogStreamNamePrefixes = req.Prefixes
	}
	// The stream lives as long as the tail, not as long as the caller's request.
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := api.LiveTail(streamCtx, in)
	if err != nil {
		cancel()
		return nil, err
	}
	tail := &Tail{
		lines:    make(chan []Line, 16),
		touch:    make(chan struct{}, 1),
		closeReq: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go func() {
		defer cancel()
		tail.run(streamCtx, stream, idle)
	}()
	return tail, nil
}

func (t *Tail) run(ctx context.Context, stream awsx.LiveTailStream, idle time.Duration) {
	var result error
	defer func() {
		_ = stream.Close()
		t.err = result
		close(t.lines)
		close(t.done)
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()
	events := stream.Events()
	for {
		select {
		case <-t.closeReq:
			return
		case <-ctx.Done():
			result = ctx.Err()
			return
		case <-t.touch:
			timer.Reset(idle)
		case <-timer.C:
			result = ErrIdle
			return
		case ev, ok := <-events:
			if !ok {
				if err := stream.Err(); err != nil {
					result = fmt.Errorf("live tail: %w", err)
				} else if ctxErr := ctx.Err(); ctxErr != nil {
					result = ctxErr // the SDK stream closes itself when the context ends
				}
				return
			}
			update, isUpdate := ev.(*logstypes.StartLiveTailResponseStreamMemberSessionUpdate)
			if !isUpdate || len(update.Value.SessionResults) == 0 {
				continue // SessionStart and empty keepalive updates carry no lines
			}
			batch := make([]Line, 0, len(update.Value.SessionResults))
			for _, r := range update.Value.SessionResults {
				container, taskID := ParseStream(aws.ToString(r.LogStreamName))
				batch = append(batch, Line{
					T:         time.UnixMilli(aws.ToInt64(r.Timestamp)).UTC(),
					Container: container,
					TaskID:    taskID,
					Stream:    aws.ToString(r.LogStreamName),
					Message:   aws.ToString(r.Message),
				})
			}
			select {
			case t.lines <- batch:
			case <-t.closeReq:
				return
			case <-ctx.Done():
				result = ctx.Err()
				return
			}
		}
	}
}

// Lines delivers batches as CloudWatch sends them; it is closed when the tail ends.
func (t *Tail) Lines() <-chan []Line { return t.lines }

// Touch resets the idle timer. It never blocks and is safe after the tail ended.
func (t *Tail) Touch() {
	select {
	case t.touch <- struct{}{}:
	default: // a reset is already pending
	}
}

// Close stops the tail and waits for the relay goroutine to exit. Idempotent.
func (t *Tail) Close() {
	t.closeOnce.Do(func() { close(t.closeReq) })
	<-t.done
}

// Done is closed once the tail has ended and the AWS stream is closed.
func (t *Tail) Done() <-chan struct{} { return t.done }

// Err is ErrIdle after the idle stop, the stream error (wrapped), the context
// error on cancellation, or nil after Close. Read it after Done.
func (t *Tail) Err() error {
	select {
	case <-t.done:
		return t.err
	default:
		return nil
	}
}

// TailLimiter caps concurrent live tails per user within this process.
type TailLimiter struct {
	mu     sync.Mutex
	max    int
	counts map[int64]int
}

func NewTailLimiter(max int) *TailLimiter {
	return &TailLimiter{max: max, counts: map[int64]int{}}
}

// Acquire takes a slot for the user. release is idempotent.
func (l *TailLimiter) Acquire(userID int64) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[userID] >= l.max {
		return func() {}, false
	}
	l.counts[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.counts[userID]--; l.counts[userID] <= 0 {
				delete(l.counts, userID)
			}
		})
	}, true
}

// Count is the number of live tails the user holds.
func (l *TailLimiter) Count(userID int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[userID]
}
