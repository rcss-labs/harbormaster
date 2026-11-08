package offhours

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ActionPrefix     = "harbormaster-offhours-"
	WokenActionName  = ActionPrefix + "woken-until"
	WakeDuration     = 2 * time.Hour
	Grace            = 5 * time.Minute
	wakeActionPrefix = ActionPrefix + "wake-"
	// sleepActionPrefix is the name prefix of sleep edges.
	sleepActionPrefix = ActionPrefix + "sleep-"
)

var dayNames = [7]string{"MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}

// Capacity is an ECS-style min/max pair applied by a scheduled action.
type Capacity struct{ Min, Max int }

// Action is one Application Auto Scaling scheduled action.
type Action struct {
	Name, Schedule, Timezone string
	Capacity                 Capacity
}

// DesiredActions derives one action per (edge kind, hour) with a day list
// (D9), so a Mon-Fri 07-20 grid needs two actions instead of ten. Edges wrap
// across the end of the week. Wake actions carry the wake capacity, sleep
// actions 0/0. The result is sorted by name.
func DesiredActions(w Week, tz string, wake Capacity) []Action {
	wakeDays := map[int][]string{}
	sleepDays := map[int][]string{}
	for i := range w {
		prev := w[(i+len(w)-1)%len(w)]
		day, hour := i/24, i%24
		switch {
		case w[i] && !prev:
			wakeDays[hour] = append(wakeDays[hour], dayNames[day])
		case !w[i] && prev:
			sleepDays[hour] = append(sleepDays[hour], dayNames[day])
		}
	}
	var actions []Action
	for hour, days := range wakeDays {
		actions = append(actions, Action{
			Name:     fmt.Sprintf("%s%02d", wakeActionPrefix, hour),
			Schedule: cron(hour, days),
			Timezone: tz,
			Capacity: wake,
		})
	}
	for hour, days := range sleepDays {
		actions = append(actions, Action{
			Name:     fmt.Sprintf("%s%02d", sleepActionPrefix, hour),
			Schedule: cron(hour, days),
			Timezone: tz,
		})
	}
	sort.Slice(actions, func(a, b int) bool { return actions[a].Name < actions[b].Name })
	return actions
}

// cron days arrive in Monday-first order because the grid is walked in order.
func cron(hour int, days []string) string {
	return fmt.Sprintf("cron(0 %d ? * %s *)", hour, strings.Join(days, ","))
}

// WokenAction is the one-shot action that puts the service back to sleep when
// a manual wake window ends.
func WokenAction(until time.Time, tz string) Action {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return Action{
		Name:     WokenActionName,
		Schedule: "at(" + until.In(loc).Format("2006-01-02T15:04:05") + ")",
		Timezone: tz,
	}
}

// IsOwned reports whether an action name belongs to Harbormaster.
func IsOwned(name string) bool { return strings.HasPrefix(name, ActionPrefix) }
