package poller

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// maxFocusInFlight bounds the focus cycles one slice runs at once.
	maxFocusInFlight = 4
	// pollNowCoalesce is how recent a cycle must be for a poll-now signal to be absorbed.
	pollNowCoalesce = time.Second
	// maxOneOffQueue is how many one-off cycles may wait for a free slot; further requests are dropped.
	maxOneOffQueue = 4
	// focusSchedulerTick is how often a slice looks for due focus cycles.
	focusSchedulerTick = 50 * time.Millisecond
	// focusWarnEvery limits the log line for a failing object to once a minute.
	focusWarnEvery = time.Minute
	// recentCycles is how many cycles an object's average cost covers.
	recentCycles = 3
	// registryReadTimeout bounds one read of the focus registry.
	registryReadTimeout = 2 * time.Second
)

// focusEntry is one focused object of a slice.
type focusEntry struct {
	obj       focus.Object
	cycles    int // cycles started
	lastStart time.Time
	running   bool
	triggered bool
	trigAt    time.Time
	// recent holds the calls of the last few cycles by category; their mean is what one cycle costs, which the
	// estimate of the round time uses. Three cycles cover one period of the every-third-cycle extra calls.
	recent   []map[focus.Category]int
	lastWarn time.Time
}

// registerLoop makes the loop reachable by the focus manager while its slice is held; the returned function removes it.
func (p *Poller) registerLoop(l *Loop) func() {
	p.loopsMu.Lock()
	p.loops[l.target] = l
	p.loopsMu.Unlock()
	return func() {
		p.loopsMu.Lock()
		if p.loops[l.target] == l {
			delete(p.loops, l.target)
		}
		p.loopsMu.Unlock()
	}
}

func (p *Poller) loopFor(t awsx.Target) *Loop {
	p.loopsMu.Lock()
	defer p.loopsMu.Unlock()
	return p.loops[t]
}

func (p *Poller) eachLoop(fn func(*Loop)) {
	p.loopsMu.Lock()
	loops := make([]*Loop, 0, len(p.loops))
	for _, l := range p.loops {
		loops = append(loops, l)
	}
	p.loopsMu.Unlock()
	for _, l := range loops {
		fn(l)
	}
}

// runFocusManager feeds the slice loops this process holds with the objects that are open. It subscribes to poll-now
// signals (speed) and reads the registry every focusReadEvery (repair: a lost signal, a slice just taken over). Both are
// filtered against the loops this process runs; a signal for another slice is dropped and counted, never forwarded.
// When the registry cannot be read for one TTL every focus entry is dropped, and focus resumes with the next good read.
// It returns when ctx ends.
func (p *Poller) runFocusManager(ctx context.Context) {
	reg := p.cfg.Registry
	if reg == nil {
		return
	}
	tel := p.cfg.Telemetry
	readNow := make(chan struct{}, 1)
	if p.cfg.Bus != nil {
		go func() {
			err := p.cfg.Bus.Subscribe(ctx, signal.PollNow, p.handlePollNow, func() {
				select {
				case readNow <- struct{}{}:
				default:
				}
			})
			if err != nil && ctx.Err() == nil {
				p.log.Warn("poller: poll-now subscription ended; the registry read still repairs focus", "err", err)
			}
		}()
	}

	lastOK := time.Now()
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, registryReadTimeout)
		objs, err := reg.List(rctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && time.Since(lastOK) >= p.cfg.Focus.TTL {
				p.eachLoop(func(l *Loop) { l.setFocused(nil) })
				p.reportFocusGauges(tel)
			}
			return
		}
		lastOK = time.Now()
		byTarget := map[awsx.Target][]focus.Object{}
		for _, o := range objs {
			byTarget[o.Target()] = append(byTarget[o.Target()], o)
		}
		p.eachLoop(func(l *Loop) { l.setFocused(byTarget[l.target]) })
		p.reportFocusGauges(tel)
	}

	ticker := time.NewTicker(p.focusReadEvery)
	defer ticker.Stop()
	read()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			read()
		case <-readNow:
			read()
		}
	}
}

// reportFocusGauges publishes the number of running focus loops by kind and each slice's effective interval.
func (p *Poller) reportFocusGauges(tel interface {
	SetFocusObjects(map[string]int)
	SetFocusInterval(account, region string, d time.Duration)
}) {
	kinds := map[string]int{string(focus.KindCluster): 0, string(focus.KindService): 0}
	p.eachLoop(func(l *Loop) {
		for kind, n := range l.focusKindCounts() {
			kinds[kind] += n
		}
		if l.focusEntries() > 0 {
			interval, _ := l.focusInterval()
			tel.SetFocusInterval(l.target.AccountID, l.target.Region, interval)
		}
	})
	tel.SetFocusObjects(kinds)
}

// handlePollNow routes one poll-now signal to the loop of the object's slice, if this process holds it.
func (p *Poller) handlePollNow(payload string) {
	received := time.Now()
	obj, err := focus.ParseMember(payload)
	if err != nil {
		p.cfg.Telemetry.CountPollNow("not_owner")
		return
	}
	l := p.loopFor(obj.Target())
	if l == nil {
		p.cfg.Telemetry.CountPollNow("not_owner")
		return
	}
	p.cfg.Telemetry.CountPollNow(l.pollNow(obj, received))
}

// setFocused makes objs the loop's focused objects: new ones start at once, ones no longer listed end.
func (l *Loop) setFocused(objs []focus.Object) {
	l.focusMu.Lock()
	want := map[string]focus.Object{}
	for _, o := range objs {
		want[o.Member()] = o
	}
	for member := range l.focusSet {
		if _, ok := want[member]; !ok {
			delete(l.focusSet, member)
		}
	}
	added := false
	now := time.Now()
	for member, o := range want {
		if _, ok := l.focusSet[member]; !ok {
			e := &focusEntry{obj: o}
			// A page that was opened first read once as a one-off (poll-now beat the registry read): that read counts as the
			// object's first cycle, so opening a page costs one cycle, not two.
			if at, ok := l.focusOnce[member]; ok && now.Sub(at) < time.Minute {
				e.lastStart, e.cycles = at, 1
			}
			l.focusSet[member] = e
			added = true
		}
	}
	l.updateFocusRoundLocked()
	l.focusMu.Unlock()
	if added {
		l.wakeFocus()
	}
}

func (l *Loop) wakeFocus() {
	select {
	case l.focusWake <- struct{}{}:
	default:
	}
}

func (l *Loop) focusEntries() int {
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	return len(l.focusSet)
}

func (l *Loop) focusCycles(o focus.Object) int {
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	if e := l.focusSet[o.Member()]; e != nil {
		return e.cycles
	}
	return 0
}

func (l *Loop) focusKindCounts() map[string]int {
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	out := map[string]int{}
	for _, e := range l.focusSet {
		out[string(e.obj.Kind)]++
	}
	return out
}

// pollNow answers a poll-now signal for an object of this slice. A cycle that started less than a second ago (or is
// running, or already requested) absorbs it: result "coalesced". Otherwise the object's next cycle runs now. An object
// that is not registered (its page was over a cap) gets exactly one cycle and no loop.
func (l *Loop) pollNow(o focus.Object, received time.Time) string {
	member := o.Member()
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	if e := l.focusSet[member]; e != nil {
		if e.running || e.triggered || (!e.lastStart.IsZero() && received.Sub(e.lastStart) < pollNowCoalesce) {
			return "coalesced"
		}
		e.triggered, e.trigAt = true, received
		l.wakeFocus()
		return "handled"
	}
	if last, ok := l.focusOnce[member]; ok && received.Sub(last) < pollNowCoalesce {
		return "coalesced"
	}
	// A one-off cycle counts against the same in-flight cap as the loops, and only a few may wait for a slot: a burst of
	// signals must not take the slice's call budget from pages that are really open.
	once := &onceRequest{obj: o, requestedAt: received}
	switch {
	case l.inFlightLocked() < maxFocusInFlight:
		l.startOnceLocked(once)
	case len(l.onceQueue) < maxOneOffQueue:
		l.onceQueue = append(l.onceQueue, once)
	default:
		return "dropped"
	}
	l.focusOnce[member] = received
	for m, at := range l.focusOnce { // forget old requests
		if received.Sub(at) > time.Minute {
			delete(l.focusOnce, m)
		}
	}
	return "handled"
}

// onceRequest is a poll-now for an object that is not registered: one cycle, no loop.
type onceRequest struct {
	obj         focus.Object
	requestedAt time.Time
}

// inFlightLocked is the number of focus cycles running in this slice, loops and one-offs alike. The caller holds focusMu.
func (l *Loop) inFlightLocked() int {
	n := l.onceRunning
	for _, e := range l.focusSet {
		if e.running {
			n++
		}
	}
	return n
}

// startOnceLocked runs a one-off cycle under the loop's context, so it ends with the loop. The caller holds focusMu.
func (l *Loop) startOnceLocked(r *onceRequest) {
	ctx := l.focusCtx
	if ctx == nil {
		ctx = context.Background()
	}
	l.onceRunning++
	l.focusRunning.Add(1)
	go func() {
		defer l.focusRunning.Done()
		l.runFocusCycle(ctx, &focusEntry{obj: r.obj}, 0, r.requestedAt, true)
		l.focusMu.Lock()
		l.onceRunning--
		l.focusMu.Unlock()
		l.wakeFocus() // a queued one-off may take the slot
	}()
}

// runFocus starts the due focus cycles of the slice, most overdue first and at most maxFocusInFlight at once, until
// ctx ends. An object is due one effective interval after its last cycle started, or at once when a poll-now asked.
func (l *Loop) runFocus(ctx context.Context) {
	l.focusMu.Lock()
	l.focusCtx = ctx
	l.focusMu.Unlock()
	ticker := time.NewTicker(focusSchedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.focusRunning.Wait()
			return
		case <-ticker.C:
		case <-l.focusWake:
		}
		l.startDueFocusCycles(ctx)
	}
}

func (l *Loop) startDueFocusCycles(ctx context.Context) {
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	if len(l.focusSet) == 0 && len(l.onceQueue) == 0 {
		return
	}
	interval := l.focusIntervalExact()
	now := time.Now()
	inFlight := l.onceRunning
	var due []*focusEntry
	for _, e := range l.focusSet {
		switch {
		case e.running:
			inFlight++
		case e.triggered || e.lastStart.IsZero() || now.Sub(e.lastStart) >= interval:
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].triggered != due[j].triggered {
			return due[i].triggered
		}
		return due[i].lastStart.Before(due[j].lastStart)
	})
	for _, e := range due {
		if inFlight >= maxFocusInFlight {
			break
		}
		inFlight++
		cycle, trigAt, wasTriggered := e.cycles, e.trigAt, e.triggered
		e.running, e.triggered, e.lastStart = true, false, now
		e.cycles++
		l.focusRunning.Add(1)
		go func() {
			defer l.focusRunning.Done()
			l.runFocusCycle(ctx, e, cycle, trigAt, wasTriggered)
		}()
	}
	// One-off cycles wait behind the loops: they are for pages that are not registered.
	for inFlight < maxFocusInFlight && len(l.onceQueue) > 0 {
		next := l.onceQueue[0]
		l.onceQueue = l.onceQueue[1:]
		l.startOnceLocked(next)
		inFlight++
	}
}

// runFocusCycle reads one object, records what it cost and, on success, leaves the evidence the page shows.
func (l *Loop) runFocusCycle(ctx context.Context, e *focusEntry, cycle int, requestedAt time.Time, byPollNow bool) {
	tel := l.p.cfg.Telemetry
	started := time.Now()
	var res focusResult
	var err error
	switch e.obj.Kind {
	case focus.KindCluster:
		res, err = l.focusCluster(ctx, e.obj.Cluster, cycle)
	default:
		res, err = l.focusService(ctx, e.obj.ServiceKey(), cycle)
	}
	finished := time.Now()

	l.focusMu.Lock()
	e.running = false
	if err == nil {
		e.recent = append(e.recent, res.byCategory)
		if len(e.recent) > recentCycles {
			e.recent = e.recent[1:]
		}
		l.updateFocusRoundLocked()
	}
	l.focusMu.Unlock()

	tel.ObserveFocusCycle(string(e.obj.Kind), finished.Sub(started))
	if byPollNow {
		tel.ObservePollNowLatency(finished.Sub(requestedAt))
	}
	if err != nil {
		l.warnFocusFailure(ctx, e, err)
		return
	}
	// A cycle with no rows to write skips the fence, so check it before saying the object was read.
	if err := l.w.CheckLease(ctx); err != nil {
		l.warnFocusFailure(ctx, e, err)
		return
	}
	interval, throttled := l.focusInterval()
	state := "live"
	if throttled {
		state = "throttled"
	}
	if reg := l.p.cfg.Registry; reg != nil {
		if setErr := reg.SetFresh(ctx, e.obj, focus.Fresh{At: finished.UTC(), Interval: interval, State: state}); setErr != nil && ctx.Err() == nil {
			l.warnFocusFailure(ctx, e, setErr)
		}
	}
	tel.SetFocusInterval(l.target.AccountID, l.target.Region, interval)
}

// warnFocusFailure logs a failed cycle once per object per minute. A throttle is not a failure: the interval already
// backs off and the page says so.
func (l *Loop) warnFocusFailure(ctx context.Context, e *focusEntry, err error) {
	if ctx.Err() != nil || errorsThrottled(err) || errors.Is(err, store.ErrLeaseLost) {
		return
	}
	l.focusMu.Lock()
	warn := time.Since(e.lastWarn) >= focusWarnEvery
	if warn {
		e.lastWarn = time.Now()
	}
	l.focusMu.Unlock()
	if warn {
		l.log.Warn("poller: focus cycle failed", "object", e.obj.Member(), "err", err)
	}
}

// updateFocusRoundLocked re-estimates the time one round over all focused objects needs: for each rate category the
// calls of one round divided by the rate, the largest of them. The caller holds focusMu.
func (l *Loop) updateFocusRoundLocked() {
	perCategory := map[focus.Category]float64{}
	for _, e := range l.focusSet {
		for _, cycle := range e.recent {
			for category, n := range cycle {
				perCategory[category] += float64(n) / float64(len(e.recent))
			}
		}
	}
	var worst float64
	for _, n := range perCategory {
		worst = max(worst, n)
	}
	l.setFocusRound(time.Duration(worst / float64(l.callsPerSec) * float64(time.Second)))
}
