package api

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
)

// pacedWriter is a connection to a browser: it takes perSecond bytes a second, nothing until pausedUntil, and fails a
// write that is still going when its deadline passes, also one set while the write waits. Its tests run in a
// synctest bubble, so its clock is fake.
type pacedWriter struct {
	perSecond   int
	pausedUntil time.Time
	onWrite     func() error // when set, decides instead

	mu       sync.Mutex
	header   http.Header
	deadline time.Time
	got      strings.Builder
	largest  int
}

func (w *pacedWriter) Header() http.Header { return w.header }
func (w *pacedWriter) WriteHeader(int)     {}
func (w *pacedWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

func (w *pacedWriter) Write(p []byte) (int, error) {
	if w.onWrite != nil {
		if err := w.onWrite(); err != nil {
			return 0, err
		}
	}
	left := time.Duration(0)
	if w.perSecond > 0 {
		left = time.Duration(len(p)) * time.Second / time.Duration(w.perSecond)
	}
	for {
		w.mu.Lock()
		late := !w.deadline.IsZero() && !time.Now().Before(w.deadline)
		paused := time.Now().Before(w.pausedUntil)
		if !late && !paused && left <= 0 {
			w.got.Write(p)
			w.largest = max(w.largest, len(p))
			w.mu.Unlock()
			return len(p), nil
		}
		w.mu.Unlock()
		if late {
			return 0, os.ErrDeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
		if !paused {
			left -= 10 * time.Millisecond
		}
	}
}

// received is the text that reached the browser and the names of the other events, in order.
func (w *pacedWriter) received(t *testing.T) (text string, events []string) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var b strings.Builder
	for _, frame := range strings.Split(w.got.String(), "\n\n") {
		name, data, ok := strings.Cut(frame, "\ndata: ")
		if !ok {
			continue // a comment
		}
		name = strings.TrimPrefix(name, "event: ")
		if name != "text" {
			events = append(events, name)
			continue
		}
		var d struct{ Delta string }
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			t.Fatalf("data %.80q: %v", data, err)
		}
		b.WriteString(d.Delta)
	}
	return b.String(), events
}

const testWriteTimeout = 10 * time.Second

func startTestWriter(w *pacedWriter, draining <-chan struct{}) *browserWriter {
	w.header = http.Header{}
	return startBrowserWriter(w, testWriteTimeout, time.Hour, draining)
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func within(t *testing.T, what string, took, from, to time.Duration) {
	t.Helper()
	if took < from || took > to {
		t.Errorf("%s took %v, want %v to %v", what, took, from, to)
	}
}

// A reader at any speed loses nothing, however long the rest of the answer takes after the turn is over.
func TestBrowserWriter_SlowReaderGetsEverything(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{perSecond: 1 << 10}
		b := startTestWriter(w, nil)
		answer := strings.Repeat("a€<", 20_000)
		for chunk := range slices.Chunk([]byte(answer), 500) {
			b.text(string(chunk))
		}
		b.event("done", map[string]int{"message_id": 1})
		start := time.Now()
		b.finish()
		if isClosed(b.broken) {
			t.Fatal("a reader that kept reading was dropped")
		}
		text, events := w.received(t)
		// The chunks above split runes; only whole text is compared.
		if len(text) < 100_000 || !slices.Equal(events, []string{"done"}) {
			t.Errorf("received %d bytes and %v", len(text), events)
		}
		if took := time.Since(start); took < 90*time.Second {
			t.Errorf("took %v: the reader was not slow", took)
		}
	})
}

func TestBrowserWriter_FramesAreSmallAndNeverSplitARune(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{}
		b := startTestWriter(w, nil)
		answer := strings.Repeat("€", 30_000) // three bytes each, so frames end inside one
		b.text(answer)
		b.finish()
		text, _ := w.received(t)
		if text != answer || !utf8.ValidString(text) {
			t.Errorf("received %d bytes of %d", len(text), len(answer))
		}
		if w.largest > maxTextFrame+100 {
			t.Errorf("a frame of %d bytes", w.largest)
		}
	})
}

func TestBrowserWriter_AReaderThatStopsBreaksAfterTheWriteDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{pausedUntil: time.Now().Add(time.Hour)}
		b := startTestWriter(w, nil)
		start := time.Now()
		b.text("x")
		<-b.broken
		within(t, "the break", time.Since(start), testWriteTimeout, testWriteTimeout+time.Second)
		if b.text("y") || b.event("done", nil) {
			t.Error("an event was accepted on a broken stream")
		}
		b.finish() // must not wait for anything
		within(t, "finish", time.Since(start), 0, testWriteTimeout+time.Second)
	})
}

// At the end a browser may pause for up to thirty seconds, also in a write that began before the end; not longer.
func TestBrowserWriter_TheFinalFlushIsBoundedByTimeWithoutProgress(t *testing.T) {
	for name, tc := range map[string]struct {
		pause  time.Duration
		broken bool
	}{"a pause of 25 s": {25 * time.Second, false}, "a pause of 35 s": {35 * time.Second, true}} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				w := &pacedWriter{pausedUntil: start.Add(tc.pause)}
				b := startTestWriter(w, nil)
				b.text("x")
				time.Sleep(time.Second) // the write is waiting under the stream's shorter deadline
				b.event("done", nil)
				b.finish()
				if isClosed(b.broken) != tc.broken {
					t.Errorf("broken = %v after %v", isClosed(b.broken), time.Since(start))
				}
				within(t, "finish", time.Since(start), min(tc.pause, 30*time.Second), 31*time.Second)
			})
		})
	}
}

// While the server shuts down the last events get two seconds in all, also when that begins during the flush.
func TestBrowserWriter_DrainBoundsTheFinalFlushToTwoSeconds(t *testing.T) {
	for name, drainAfter := range map[string]time.Duration{"already draining": 0, "during the flush": 5 * time.Second} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				draining := make(chan struct{})
				w := &pacedWriter{perSecond: 1 << 10} // reads all the time, but would need minutes
				b := startTestWriter(w, draining)
				b.text(strings.Repeat("x", 500_000))
				start := time.Now()
				if drainAfter == 0 {
					close(draining)
				} else {
					time.AfterFunc(drainAfter, func() { close(draining) })
				}
				b.finish()
				within(t, "finish", time.Since(start), drainAfter+1900*time.Millisecond, drainAfter+2100*time.Millisecond)
			})
		})
	}
}

// However steadily a browser reads, the rest of an answer has ten minutes after the turn is over, not more.
func TestBrowserWriter_TheFinalFlushHasACeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{perSecond: 1 << 10}
		b := startTestWriter(w, nil)
		b.text(strings.Repeat("x", 2<<20)) // over half an hour at this pace
		time.Sleep(time.Minute)
		start := time.Now()
		b.finish()
		within(t, "finish", time.Since(start), 10*time.Minute-time.Second, 10*time.Minute+time.Second)
	})
}

func TestBrowserWriter_DrainDoesNotDisturbAStreamThatIsStillGoing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		draining := make(chan struct{})
		close(draining)
		w := &pacedWriter{pausedUntil: time.Now().Add(5 * time.Second)}
		b := startTestWriter(w, draining)
		b.text("x")
		time.Sleep(6 * time.Second)
		b.event("error", nil)
		b.finish()
		if text, events := w.received(t); text != "x" || !slices.Equal(events, []string{"error"}) {
			t.Errorf("received %q %v", text, events)
		}
	})
}

// What waits for a browser is bounded in bytes, and the frames that matter have room of their own.
func TestBrowserWriter_PendingIsMergedAndBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{pausedUntil: time.Now().Add(5 * time.Second)}
		b := startTestWriter(w, nil)
		b.event("notice", map[string]string{"kind": "context_trimmed"}) // the writer takes this one and waits on it
		time.Sleep(time.Second)
		for range 5000 {
			b.text("abcd")
		}
		if n := len(b.items); n != 1 {
			t.Errorf("%d items pending, want the deltas merged into one", n)
		}
		chunk := strings.Repeat("z", 64<<10)
		for range 2 * maxPendingText / len(chunk) {
			b.text(chunk)
		}
		for range 100_000 {
			b.event("tool_start", map[string]string{"name": "get_service"})
		}
		if b.textBytes > maxPendingText+len(chunk) || b.controlBytes > controlRoom {
			t.Errorf("%d bytes of text and %d of control frames pending", b.textBytes, b.controlBytes)
		}
		if !b.event("done", map[string]int{"message_id": 1}) {
			t.Error("done was refused")
		}
		if isClosed(b.broken) {
			t.Fatal("a full queue broke the stream")
		}
		b.finish()
		text, events := w.received(t)
		if len(text) < maxPendingText-len(chunk) || !strings.HasSuffix(text, "because it grew too long.]") || strings.Count(text, "grew too long") != 1 {
			t.Errorf("received %d bytes, ending %q", len(text), text[max(0, len(text)-60):])
		}
		if events[len(events)-1] != "done" || len(events) < 1000 {
			t.Errorf("%d events, last %s", len(events), events[len(events)-1])
		}
		if b.text("late") {
			t.Error("text was accepted after finish")
		}
	})
}

// The bounds are on what waits, not on what was sent: a browser that keeps up gets any amount.
func TestBrowserWriter_WhatWasSentNoLongerCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{}
		b := startTestWriter(w, nil)
		chunk := strings.Repeat("z", 1<<20)
		const chunks, tools = 12, 10_000
		for range chunks {
			b.text(chunk)
			synctest.Wait()
		}
		for range tools {
			b.event("tool_start", map[string]string{"name": "get_service"})
			synctest.Wait()
		}
		b.finish()
		text, events := w.received(t)
		if len(text) != chunks*len(chunk) || len(events) != tools {
			t.Errorf("received %d bytes of %d and %d events of %d", len(text), chunks*len(chunk), len(events), tools)
		}
	})
}

func TestBrowserWriter_AFailedOrPanickingWriteBreaksTheStream(t *testing.T) {
	for name, onWrite := range map[string]func() error{
		"fails":  func() error { return os.ErrClosed },
		"panics": func() error { panic("write") },
	} {
		synctest.Test(t, func(t *testing.T) {
			b := startTestWriter(&pacedWriter{onWrite: onWrite}, nil)
			b.text("x")
			synctest.Wait()
			if !isClosed(b.broken) {
				t.Errorf("%s: the break was not reported", name)
			}
			b.finish()
		})
	}
}

func TestBrowserWriter_SendsAHeartbeatWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &pacedWriter{header: http.Header{}}
		b := startBrowserWriter(w, testWriteTimeout, 15*time.Second, nil)
		time.Sleep(50 * time.Second)
		b.finish()
		if n := strings.Count(w.got.String(), ": keep-alive\n\n"); n != 3 {
			t.Errorf("%d heartbeats in 50 s", n)
		}
	})
}
