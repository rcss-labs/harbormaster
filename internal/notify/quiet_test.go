package notify

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestHeldUntil(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ny, _ := time.LoadLocation("America/New_York")
	quiet := func(zone string) store.NotificationRoute {
		return store.NotificationRoute{QuietEnabled: true, QuietTimezone: zone}
	}
	at := func(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, loc)
	}

	tests := []struct {
		name     string
		route    store.NotificationRoute
		severity string
		now      time.Time
		held     bool
		until    time.Time
	}{
		{"21:59 is not held", quiet("Europe/Berlin"), "warning", at(berlin, 2026, 11, 20, 21, 59), false, time.Time{}},
		{"22:00 is held until 08:00 next day", quiet("Europe/Berlin"), "warning", at(berlin, 2026, 11, 20, 22, 0), true, at(berlin, 2026, 11, 21, 8, 0)},
		{"23:30 is held until 08:00 next day", quiet("Europe/Berlin"), "warning", at(berlin, 2026, 11, 20, 23, 30), true, at(berlin, 2026, 11, 21, 8, 0)},
		{"07:59 is held until 08:00 the same day", quiet("Europe/Berlin"), "warning", at(berlin, 2026, 11, 21, 7, 59), true, at(berlin, 2026, 11, 21, 8, 0)},
		{"08:00 is not held", quiet("Europe/Berlin"), "warning", at(berlin, 2026, 11, 21, 8, 0), false, time.Time{}},
		{"critical is never held", quiet("Europe/Berlin"), "critical", at(berlin, 2026, 11, 20, 23, 0), false, time.Time{}},
		{"quiet hours off", store.NotificationRoute{QuietTimezone: "Europe/Berlin"}, "warning", at(berlin, 2026, 11, 20, 23, 0), false, time.Time{}},
		// US clocks spring forward at 02:00 on 2027-03-14, inside the quiet window.
		{"spring forward still releases at 08:00 local", quiet("America/New_York"), "warning", at(ny, 2027, 3, 13, 23, 0), true, at(ny, 2027, 3, 14, 8, 0)},
		// 22:00 Jerusalem is 20:00 UTC in winter: a UTC reading would not hold it.
		{"the route zone decides, not UTC", quiet("Asia/Jerusalem"), "warning", time.Date(2026, 11, 20, 20, 30, 0, 0, time.UTC), true, time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)},
		{"an unloadable zone is UTC", quiet("Not/AZone"), "warning", time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC), true, time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		until, held := HeldUntil(tc.route, tc.severity, tc.now)
		if held != tc.held || (held && !until.Equal(tc.until)) {
			t.Errorf("%s: HeldUntil = %v, %v; want %v, %v", tc.name, until, held, tc.until, tc.held)
		}
	}
}
