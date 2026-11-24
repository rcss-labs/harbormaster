package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentrun"
)

const (
	// maxPendingText is the most unsent text kept for one browser, far above any answer. Past it the rest of the
	// answer is not relayed (the saved answer is cut long before); the stream goes on.
	maxPendingText = 8 << 20
	// controlRoom is the most unsent bytes of tool and notice events; it is not shared with text.
	controlRoom = 256 << 10
	// maxTextFrame is the most text written at once. It is small so that a browser that reads slowly is seen to make
	// progress: each frame has the whole of the write deadline.
	maxTextFrame = 4 << 10
	// flushIdle is how long the last events wait for a browser that has stopped reading; drainFlush is how long they
	// get in all while the server shuts down.
	flushIdle  = 30 * time.Second
	drainFlush = 2 * time.Second
)

// browserWriter sends server-sent events to the browser from a goroutine of its own, so a slow browser never slows
// the reading of the agent's events (which carry the usage the budget depends on). Text that the browser has not
// been sent yet is merged, what waits is bounded in bytes, and the done and error events are always accepted: a
// browser that keeps reading, at any speed, loses nothing. Only a write that fails, or that does not finish before
// its deadline, breaks the stream, which broken tells. Every data line is JSON, which never holds a raw newline, so
// text from the model cannot end an event early or start a forged one. The writer also sends the comment heartbeat.
type browserWriter struct {
	w            http.ResponseWriter
	rc           *http.ResponseController
	writeTimeout time.Duration
	draining     <-chan struct{}

	broken chan struct{} // closed when the stream can no longer reach the browser
	done   chan struct{} // closed when the writer goroutine has stopped
	wake   chan struct{}

	mu           sync.Mutex // guards the queue
	items        []outItem
	textBytes    int
	controlBytes int
	textCut      bool
	closed       bool

	deadlineMu sync.Mutex // guards the write deadline's parts, and setting it
	writeStart time.Time
	flushing   bool
	hardStop   time.Time
}

// outItem is one unsent event: merged text, or a finished frame.
type outItem struct {
	text  []byte
	frame string
}

// startBrowserWriter starts the writer goroutine; finish must be called to stop it.
func startBrowserWriter(w http.ResponseWriter, writeTimeout, heartbeat time.Duration, draining <-chan struct{}) *browserWriter {
	b := &browserWriter{
		w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout, draining: draining,
		broken: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	go b.loop(heartbeat)
	return b
}

func (b *browserWriter) loop(heartbeat time.Duration) {
	defer close(b.done)
	ok := false
	defer func() {
		// A write that panics must not take the process down, or leave the turn waiting.
		if rec := recover(); rec != nil || !ok {
			close(b.broken)
		}
	}()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		frame, closed := b.next()
		if frame == "" && closed {
			ok = true
			return
		}
		if frame == "" {
			select {
			case <-b.wake:
				continue
			case <-ticker.C:
				frame = ": keep-alive\n\n"
			}
		}
		if b.write(frame) != nil {
			return
		}
	}
}

// next takes the next frame off the queue. An empty one means that nothing waits; closed, that nothing ever will.
func (b *browserWriter) next() (frame string, closed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return "", b.closed
	}
	it := &b.items[0]
	if it.text == nil {
		frame = it.frame
		b.controlBytes -= len(frame)
		b.pop()
		return frame, false
	}
	n := len(it.text)
	if n > maxTextFrame {
		n = maxTextFrame
		for n > maxTextFrame-utf8.UTFMax && !utf8.RuneStart(it.text[n]) {
			n--
		}
	}
	frame = sseFrameOf("text", map[string]string{"delta": string(it.text[:n])})
	b.textBytes -= n
	if it.text = it.text[n:]; len(it.text) == 0 {
		b.pop()
	}
	return frame, false
}

func (b *browserWriter) pop() {
	if b.items = b.items[1:]; len(b.items) == 0 {
		b.items = nil
	}
}

func sseFrameOf(name string, data any) string {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw)
}

func (b *browserWriter) write(frame string) error {
	b.setDeadline(func() { b.writeStart = time.Now() })
	if _, err := b.w.Write([]byte(frame)); err != nil {
		return err
	}
	if err := b.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// setDeadline applies change and sets the deadline of the write that is going on, or of the next: writeTimeout from
// its start, flushIdle once the turn is over, and never past hardStop.
func (b *browserWriter) setDeadline(change func()) {
	b.deadlineMu.Lock()
	defer b.deadlineMu.Unlock()
	change()
	deadline := b.writeStart.Add(b.writeTimeout)
	if b.flushing {
		deadline = b.writeStart.Add(max(b.writeTimeout, flushIdle))
	}
	if !b.hardStop.IsZero() && b.hardStop.Before(deadline) {
		deadline = b.hardStop
	}
	_ = b.rc.SetWriteDeadline(deadline)
}

// text queues a text delta, merged into the text that waits. It is refused once the stream is broken or closed, and
// dropped once too much waits.
func (b *browserWriter) text(delta string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuses() {
		return false
	}
	if b.textCut {
		return true
	}
	if b.textBytes+len(delta) > maxPendingText {
		b.textCut = true
		delta = agentrun.AnswerCutMarker
	}
	if n := len(b.items); n > 0 && b.items[n-1].text != nil {
		b.items[n-1].text = append(b.items[n-1].text, delta...)
	} else {
		b.items = append(b.items, outItem{text: []byte(delta)})
	}
	b.textBytes += len(delta)
	b.signal()
	return true
}

// event queues any other event. done and error are refused only once the stream is broken or closed, the rest also
// when too many wait.
func (b *browserWriter) event(name string, data any) bool {
	frame := sseFrameOf(name, data)
	b.mu.Lock()
	defer b.mu.Unlock()
	last := name == "done" || name == "error"
	if b.refuses() || (!last && b.controlBytes+len(frame) > controlRoom) {
		return false
	}
	b.items = append(b.items, outItem{frame: frame})
	b.controlBytes += len(frame)
	b.signal()
	return true
}

func (b *browserWriter) refuses() bool {
	select {
	case <-b.broken:
		return true
	default:
		return b.closed
	}
}

func (b *browserWriter) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// finish accepts no more events and returns once the writer has stopped, so nothing is written after the handler
// returns. A browser that keeps reading gets everything; one that stops is given flushIdle, and the shutdown gives
// either drainFlush in all.
func (b *browserWriter) finish() {
	b.mu.Lock()
	b.closed = true
	b.signal()
	b.mu.Unlock()
	b.setDeadline(func() { b.flushing = true })
	select {
	case <-b.done:
	case <-b.draining:
		b.setDeadline(func() { b.hardStop = time.Now().Add(drainFlush) })
		<-b.done
	}
	b.setDeadline(func() { b.writeStart, b.hardStop = time.Now(), time.Now().Add(drainFlush) }) // for the server's own last bytes
}
