package notify

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	quietStartHour = 22
	quietEndHour   = 8
)

// HeldUntil reports whether a delivery for this severity planned at now is held by the route's quiet hours, and
// until when. Only warnings are held; the window is 22:00 up to but not including 08:00 in the route's zone, and
// the release is 08:00 wall time (so a daylight saving change inside the window does not shift it). A zone that
// does not load is treated as UTC; the API rejects such a zone on save.
func HeldUntil(r store.NotificationRoute, severity string, now time.Time) (time.Time, bool) {
	if !r.QuietEnabled || severity == "critical" {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(r.QuietTimezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	switch {
	case local.Hour() >= quietStartHour:
		return time.Date(local.Year(), local.Month(), local.Day()+1, quietEndHour, 0, 0, 0, loc), true
	case local.Hour() < quietEndHour:
		return time.Date(local.Year(), local.Month(), local.Day(), quietEndHour, 0, 0, 0, loc), true
	}
	return time.Time{}, false
}
