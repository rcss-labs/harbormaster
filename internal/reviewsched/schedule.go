// Package reviewsched does the schedule arithmetic of scheduled reviews: when
// an occurrence falls, which period it belongs to. Everything is a pure
// function of the schedule and the instant it is given.
package reviewsched

import (
	"fmt"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // fallback zone data for hosts without a zoneinfo database
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

// location loads a canonical IANA zone name. Names that would reach the host
// zone or a file outside the zone database are refused.
func location(name string) (*time.Location, error) {
	if err := canonicalZoneName(name); err != nil {
		return nil, fmt.Errorf("timezone %q: %w", name, err)
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

func canonicalZoneName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("is empty")
	case strings.EqualFold(name, "local"), strings.EqualFold(name, "localtime"),
		strings.EqualFold(name, "posixrules"), strings.EqualFold(name, "factory"):
		return fmt.Errorf("is not an IANA zone")
	case strings.HasPrefix(name, "posix/"), strings.HasPrefix(name, "right/"):
		return fmt.Errorf("is not an IANA zone")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("is not an IANA zone")
		}
	}
	return nil
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
	// i = -1 also tries tomorrow, in case an occurrence is dated ahead of the local date.
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

// scheduledDate is the calendar date an occurrence was scheduled for. Usually
// that is the local date of the instant. A wall time skipped by a spring
// change runs when the gap ends, which can be on a later local date, so the
// search looks back from the local date for the day whose occurrence is at.
// Where two scheduled days share one instant (a midnight schedule across a
// skipped day) the later date wins, the local date of the instant. An instant
// that is no occurrence falls back to its local date.
func scheduledDate(s Schedule, loc *time.Location, at time.Time) time.Time {
	y, m, d := at.In(loc).Date()
	local := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		day := local.AddDate(0, 0, -i)
		if matches(s, day) && occurrence(day, s.AtMinute, loc).Equal(at) {
			return day
		}
	}
	return local
}

// PeriodKey identifies the period an occurrence belongs to, from the local
// date it was scheduled for: d:YYYY-MM-DD, w:YYYY-Www (ISO week) or m:YYYY-MM.
// An invalid schedule gives the empty string.
func PeriodKey(s Schedule, at time.Time) string {
	loc, err := prepare(s)
	if err != nil {
		return ""
	}
	date := scheduledDate(s, loc, at)
	switch s.Cadence {
	case Weekly:
		y, w := date.ISOWeek()
		return fmt.Sprintf("w:%04d-W%02d", y, w)
	case Monthly:
		return date.Format("m:2006-01")
	}
	return date.Format("d:2006-01-02")
}

// PeriodLabel is the period as shown to people: 25 Nov 2026, Week 48, 2026 or
// November 2026.
func PeriodLabel(s Schedule, at time.Time) string {
	loc, err := prepare(s)
	if err != nil {
		return ""
	}
	date := scheduledDate(s, loc, at)
	switch s.Cadence {
	case Weekly:
		y, w := date.ISOWeek()
		return fmt.Sprintf("Week %d, %d", w, y)
	case Monthly:
		return date.Format("January 2006")
	}
	return date.Format("2 Jan 2006")
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
