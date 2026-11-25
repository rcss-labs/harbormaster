// Package reviewsched does the schedule arithmetic of scheduled reviews: when
// an occurrence falls, which period it belongs to. Everything is a pure
// function of the schedule and the instant it is given.
package reviewsched

import (
	"fmt"
	"sync"
	"time"
	_ "time/tzdata" // zone data travels with the binary; the host zone is never consulted
)

const (
	Daily   = "daily"
	Weekly  = "weekly"
	Monthly = "monthly"
)

// Schedule is a cadence, a local minute of the day, a weekday (weekly only,
// Monday = 1) or a day of the month (monthly only, 1 to 28) and an IANA zone.
type Schedule struct {
	Cadence                     string
	AtMinute, Weekday, MonthDay int
	Timezone                    string
}

// searchDays bounds how far back or forward an occurrence is looked for; the
// sparsest cadence has one every 31 days.
const searchDays = 70

var zones sync.Map // name -> *time.Location

func location(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("timezone %q is not an IANA zone name", name)
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", name, err)
	}
	zones.Store(name, loc)
	return loc, nil
}

// Validate checks every field and that the zone loads.
func Validate(s Schedule) error {
	switch s.Cadence {
	case Daily, Weekly, Monthly:
	default:
		return fmt.Errorf("cadence %q must be daily, weekly or monthly", s.Cadence)
	}
	if s.AtMinute < 0 || s.AtMinute > 1439 {
		return fmt.Errorf("at_minute %d must be 0 to 1439", s.AtMinute)
	}
	if s.Cadence == Weekly {
		if s.Weekday < 1 || s.Weekday > 7 {
			return fmt.Errorf("weekday %d must be 1 to 7", s.Weekday)
		}
	} else if s.Weekday != 0 {
		return fmt.Errorf("weekday applies to weekly schedules only")
	}
	if s.Cadence == Monthly {
		if s.MonthDay < 1 || s.MonthDay > 28 {
			return fmt.Errorf("month_day %d must be 1 to 28", s.MonthDay)
		}
	} else if s.MonthDay != 0 {
		return fmt.Errorf("month_day applies to monthly schedules only")
	}
	_, err := location(s.Timezone)
	return err
}

// Last returns the latest occurrence at or before now.
func Last(s Schedule, now time.Time) (time.Time, error) {
	loc, err := prepare(s)
	if err != nil {
		return time.Time{}, err
	}
	y, m, d := now.In(loc).Date()
	// Start a day ahead: with an early-instant overlap or a far zone the
	// nominal date of the latest occurrence can trail the local date.
	for i := -1; i < searchDays; i++ {
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i)
		if !matches(s, day) {
			continue
		}
		at := occurrence(day, s.AtMinute, loc)
		if !at.After(now) {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("no occurrence found before %s", now.UTC().Format(time.RFC3339))
}

// Next returns the first occurrence after now.
func Next(s Schedule, now time.Time) (time.Time, error) {
	loc, err := prepare(s)
	if err != nil {
		return time.Time{}, err
	}
	y, m, d := now.In(loc).Date()
	for i := -1; i < searchDays; i++ {
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		if !matches(s, day) {
			continue
		}
		at := occurrence(day, s.AtMinute, loc)
		if at.After(now) {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("no occurrence found after %s", now.UTC().Format(time.RFC3339))
}

func prepare(s Schedule) (*time.Location, error) {
	if err := Validate(s); err != nil {
		return nil, err
	}
	return location(s.Timezone)
}

// matches reports whether the calendar day (held as a UTC date) is one the
// schedule runs on.
func matches(s Schedule, day time.Time) bool {
	switch s.Cadence {
	case Weekly:
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		return wd == s.Weekday
	case Monthly:
		return day.Day() == s.MonthDay
	}
	return true
}

// occurrence is the instant at which loc's wall clock shows the given date
// and minute. A time skipped by a spring change is the instant the gap ends;
// a time shown twice by an autumn change is the earlier instant.
func occurrence(day time.Time, atMinute int, loc *time.Location) time.Time {
	y, m, d := day.Date()
	wallUTC := time.Date(y, m, d, atMinute/60, atMinute%60, 0, 0, time.UTC)
	_, offBefore := wallUTC.Add(-24 * time.Hour).In(loc).Zone()
	_, offAfter := wallUTC.Add(24 * time.Hour).In(loc).Zone()

	var best time.Time
	for _, off := range [2]int{offBefore, offAfter} {
		cand := wallUTC.Add(-time.Duration(off) * time.Second)
		if sameWall(cand.In(loc), wallUTC) && (best.IsZero() || cand.Before(best)) {
			best = cand
		}
	}
	if !best.IsZero() {
		return best
	}
	// The wall time falls in a gap: find the second the offset changes.
	lo, hi := wallUTC.Add(-24*time.Hour).Unix(), wallUTC.Add(24*time.Hour).Unix()
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if _, off := time.Unix(mid, 0).In(loc).Zone(); off == offBefore {
			lo = mid
		} else {
			hi = mid
		}
	}
	return time.Unix(hi, 0).UTC()
}

func sameWall(local, wallUTC time.Time) bool {
	y, m, d := local.Date()
	return y == wallUTC.Year() && m == wallUTC.Month() && d == wallUTC.Day() &&
		local.Hour() == wallUTC.Hour() && local.Minute() == wallUTC.Minute()
}

// PeriodKey identifies the period an occurrence belongs to, from its local
// date: d:YYYY-MM-DD, w:YYYY-Www (ISO week) or m:YYYY-MM. An invalid schedule
// gives the empty string.
func PeriodKey(s Schedule, at time.Time) string {
	loc, err := prepare(s)
	if err != nil {
		return ""
	}
	local := at.In(loc)
	switch s.Cadence {
	case Weekly:
		y, w := local.ISOWeek()
		return fmt.Sprintf("w:%04d-W%02d", y, w)
	case Monthly:
		return local.Format("m:2006-01")
	}
	return local.Format("d:2006-01-02")
}

// PeriodLabel is the period as shown to people: 25 Nov 2026, Week 48, 2026 or
// November 2026.
func PeriodLabel(s Schedule, at time.Time) string {
	loc, err := prepare(s)
	if err != nil {
		return ""
	}
	local := at.In(loc)
	switch s.Cadence {
	case Weekly:
		y, w := local.ISOWeek()
		return fmt.Sprintf("Week %d, %d", w, y)
	case Monthly:
		return local.Format("January 2006")
	}
	return local.Format("2 Jan 2006")
}

// Range is the analytics range a cadence reviews; unknown cadences get 7d.
func Range(cadence string) string {
	switch cadence {
	case Daily:
		return "24h"
	case Monthly:
		return "30d"
	}
	return "7d"
}

// RunsPerMonth is the most runs a cadence can have in one calendar month.
func RunsPerMonth(cadence string) int {
	switch cadence {
	case Daily:
		return 31
	case Weekly:
		return 5
	case Monthly:
		return 1
	}
	return 0
}
