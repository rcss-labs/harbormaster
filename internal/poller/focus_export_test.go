package poller

import "github.com/rcss-labs/harbormaster/internal/focus"

// focusCycles is how many cycles the object's focus entry has started.
func (l *Loop) focusCycles(o focus.Object) int {
	l.focusMu.Lock()
	defer l.focusMu.Unlock()
	if e := l.focusSet[o.Member()]; e != nil {
		return e.cycles
	}
	return 0
}
