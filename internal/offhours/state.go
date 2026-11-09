package offhours

import (
	"time"
	_ "time/tzdata" // zone data must not depend on the host image
)

type StateKind string

const (
	Asleep StateKind = "asleep"
	Awake  StateKind = "awake"
	Woken  StateKind = "woken"
)

// State is a service's off-hours state. Until is the next wake for Asleep, the
// next sleep for Awake (nil when always on), and woken_until for Woken.
type State struct {
	Kind  StateKind
	Until *time.Time
}

func gridIndex(day, hour int) int { return ((day%7+7)%7)*24 + hour }

func mondayFirst(wd time.Weekday) int { return (int(wd) + 6) % 7 }

// StateAt returns the state at now. An unexpired wokenUntil wins over the grid.
func StateAt(w Week, loc *time.Location, now time.Time, wokenUntil *time.Time) State {
	if wokenUntil != nil && now.Before(*wokenUntil) {
		until := *wokenUntil
		return State{Kind: Woken, Until: &until}
	}
	local := now.In(loc)
	awake := w[gridIndex(mondayFirst(local.Weekday()), local.Hour())]
	kind := Asleep
	if awake {
		kind = Awake
	}
	st := State{Kind: kind}
	if next, ok := NextEdge(w, loc, now, !awake); ok {
		st.Until = &next
	}
	return st
}

// NextEdge returns the first wall-clock hour start after now whose grid value
// is wantAwake and differs from the hour before. It walks calendar days and
// builds each instant with time.Date so DST gaps and overlaps stay correct.
func NextEdge(w Week, loc *time.Location, now time.Time, wantAwake bool) (time.Time, bool) {
	start := now.In(loc)
	for dayOffset := 0; dayOffset <= 8; dayOffset++ {
		date := start.AddDate(0, 0, dayOffset)
		day := mondayFirst(date.Weekday())
		for hour := 0; hour < 24; hour++ {
			idx := gridIndex(day, hour)
			if w[idx] != wantAwake || w[(idx+len(w)-1)%len(w)] == wantAwake {
				continue
			}
			candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, 0, 0, 0, loc)
			if candidate.After(now) {
				return candidate, true
			}
		}
	}
	return time.Time{}, false
}

// WakeUntil is when a manual wake ends: now+WakeDuration, or the next wake
// edge if that comes sooner, so the grid takes over without a double start.
func WakeUntil(w Week, loc *time.Location, now time.Time) time.Time {
	until := now.Add(WakeDuration)
	if next, ok := NextEdge(w, loc, now, true); ok && next.Before(until) {
		return next
	}
	return until
}

// InGrace is true within Grace after a grid edge or after lastApplied, while
// Application Auto Scaling may still be converging on the change.
func InGrace(w Week, loc *time.Location, now time.Time, lastApplied *time.Time) bool {
	if lastApplied != nil {
		if since := now.Sub(*lastApplied); since >= 0 && since < Grace {
			return true
		}
	}
	local := now.In(loc)
	idx := gridIndex(mondayFirst(local.Weekday()), local.Hour())
	if w[idx] == w[(idx+len(w)-1)%len(w)] {
		return false
	}
	hourStart := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
	since := now.Sub(hourStart)
	return since >= 0 && since < Grace
}
