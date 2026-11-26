package reviewsched

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

var testZones = []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Kolkata", "Pacific/Kiritimati", "Pacific/Pago_Pago"}

func TestValidate_Table(t *testing.T) {
	ok := Schedule{Cadence: "daily", AtMinute: 480, Timezone: "Europe/Berlin"}
	cases := []struct {
		name   string
		mutate func(s *Schedule)
		bad    bool
	}{
		{"valid daily", func(s *Schedule) {}, false},
		{"valid weekly", func(s *Schedule) { s.Cadence = "weekly"; s.Weekday = 7 }, false},
		{"valid monthly", func(s *Schedule) { s.Cadence = "monthly"; s.MonthDay = 28 }, false},
		{"minute 0", func(s *Schedule) { s.AtMinute = 0 }, false},
		{"minute 1439", func(s *Schedule) { s.AtMinute = 1439 }, false},
		{"minute 1440", func(s *Schedule) { s.AtMinute = 1440 }, true},
		{"minute -1", func(s *Schedule) { s.AtMinute = -1 }, true},
		{"empty cadence", func(s *Schedule) { s.Cadence = "" }, true},
		{"unknown cadence", func(s *Schedule) { s.Cadence = "hourly" }, true},
		{"weekly no weekday", func(s *Schedule) { s.Cadence = "weekly" }, true},
		{"weekly weekday 8", func(s *Schedule) { s.Cadence = "weekly"; s.Weekday = 8 }, true},
		{"weekly with month day", func(s *Schedule) { s.Cadence = "weekly"; s.Weekday = 1; s.MonthDay = 3 }, true},
		{"monthly no day", func(s *Schedule) { s.Cadence = "monthly" }, true},
		{"monthly day 29", func(s *Schedule) { s.Cadence = "monthly"; s.MonthDay = 29 }, true},
		{"monthly with weekday", func(s *Schedule) { s.Cadence = "monthly"; s.MonthDay = 3; s.Weekday = 2 }, true},
		{"daily with weekday", func(s *Schedule) { s.Weekday = 2 }, true},
		{"daily with month day", func(s *Schedule) { s.MonthDay = 2 }, true},
		{"empty zone", func(s *Schedule) { s.Timezone = "" }, true},
		{"Local zone", func(s *Schedule) { s.Timezone = "Local" }, true},
		{"unknown zone", func(s *Schedule) { s.Timezone = "Mars/Olympus" }, true},
		{"zone with dots", func(s *Schedule) { s.Timezone = "../etc/passwd" }, true},
	}
	for _, c := range cases {
		s := ok
		c.mutate(&s)
		err := Validate(s)
		if c.bad && err == nil {
			t.Errorf("%s: want an error", c.name)
		}
		if !c.bad && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestInvalidScheduleNeverGivesATime(t *testing.T) {
	bad := Schedule{Cadence: "daily", AtMinute: 5000, Timezone: "UTC"}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if at, err := Last(bad, now); err == nil || !at.IsZero() {
		t.Errorf("Last: %v %v", at, err)
	}
	if at, err := Next(bad, now); err == nil || !at.IsZero() {
		t.Errorf("Next: %v %v", at, err)
	}
	if k := PeriodKey(bad, now); k != "" {
		t.Errorf("PeriodKey %q", k)
	}
	if l := PeriodLabel(bad, now); l != "" {
		t.Errorf("PeriodLabel %q", l)
	}
}

// wall builds an instant from a local wall time in a zone that is known to be unambiguous.
func wall(t *testing.T, zone string, y int, mo time.Month, d, h, mi int) time.Time {
	t.Helper()
	return time.Date(y, mo, d, h, mi, 0, 0, mustLoc(t, zone))
}

type lastCase struct {
	name string
	s    Schedule
	now  time.Time
	want time.Time
}

func runLast(t *testing.T, cases []lastCase) {
	t.Helper()
	for _, c := range cases {
		got, err := Last(c.s, c.now)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%s: got %s want %s", c.name, got.UTC(), c.want.UTC())
		}
	}
}

func TestLast_Daily(t *testing.T) {
	var cases []lastCase
	for _, z := range testZones {
		s := Schedule{Cadence: "daily", AtMinute: 8*60 + 30, Timezone: z}
		today := wall(t, z, 2026, time.November, 25, 8, 30)
		yesterday := wall(t, z, 2026, time.November, 24, 8, 30)
		cases = append(cases,
			lastCase{z + " exactly at", s, today, today},
			lastCase{z + " a second after", s, today.Add(time.Second), today},
			lastCase{z + " a second before", s, today.Add(-time.Second), yesterday},
			lastCase{z + " evening", s, wall(t, z, 2026, time.November, 25, 23, 59), today},
		)
	}
	runLast(t, cases)
}

func TestLast_Weekly(t *testing.T) {
	var cases []lastCase
	for _, z := range testZones {
		s := Schedule{Cadence: "weekly", AtMinute: 9 * 60, Weekday: 3, Timezone: z} // Wednesday
		wed := wall(t, z, 2026, time.November, 25, 9, 0)
		prev := wall(t, z, 2026, time.November, 18, 9, 0)
		cases = append(cases,
			lastCase{z + " at", s, wed, wed},
			lastCase{z + " before", s, wed.Add(-time.Minute), prev},
			lastCase{z + " Sunday after", s, wall(t, z, 2026, time.November, 29, 20, 0), wed},
			lastCase{z + " Tuesday before", s, wall(t, z, 2026, time.November, 24, 20, 0), prev},
		)
	}
	runLast(t, cases)
}

func TestLast_Monthly(t *testing.T) {
	var cases []lastCase
	for _, z := range testZones {
		s := Schedule{Cadence: "monthly", AtMinute: 0, MonthDay: 28, Timezone: z}
		nov := wall(t, z, 2026, time.November, 28, 0, 0)
		oct := wall(t, z, 2026, time.October, 28, 0, 0)
		cases = append(cases,
			lastCase{z + " at", s, nov, nov},
			lastCase{z + " before", s, nov.Add(-time.Second), oct},
			lastCase{z + " month end", s, wall(t, z, 2026, time.December, 27, 23, 0), nov},
		)
		s1 := Schedule{Cadence: "monthly", AtMinute: 23*60 + 59, MonthDay: 1, Timezone: z}
		cases = append(cases, lastCase{z + " year start", s1, wall(t, z, 2027, time.January, 1, 12, 0), wall(t, z, 2026, time.December, 1, 23, 59)})
	}
	runLast(t, cases)
}

func TestOccurrence_SpringGapRunsWhenTheGapEnds(t *testing.T) {
	s := Schedule{Cadence: "daily", AtMinute: 2*60 + 30, Timezone: "Europe/Berlin"}
	want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)
	got, err := Last(s, now)
	if err != nil || !got.Equal(want) {
		t.Fatalf("Last: %v %v, want %s", got, err, want)
	}
	// Before the gap ends the previous day's occurrence is the latest.
	before, _ := Last(s, want.Add(-time.Second))
	if !before.Equal(time.Date(2026, 3, 28, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("before the gap: %s", before)
	}
	// Once: the next occurrence is the following day at 02:30 local (CEST).
	next, err := Next(s, want)
	if err != nil || !next.Equal(time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("Next: %v %v", next, err)
	}
	if PeriodKey(s, got) != "d:2026-03-29" {
		t.Fatalf("key %s", PeriodKey(s, got))
	}
}

func TestOccurrence_AutumnOverlapRunsOnceAtTheEarlierInstant(t *testing.T) {
	s := Schedule{Cadence: "daily", AtMinute: 2*60 + 30, Timezone: "Europe/Berlin"}
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	// An hour later the same wall time shows again; it must not count.
	got, err := Last(s, time.Date(2026, 10, 25, 1, 45, 0, 0, time.UTC))
	if err != nil || !got.Equal(want) {
		t.Fatalf("Last: %v %v, want %s", got, err, want)
	}
	got, _ = Last(s, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if !got.Equal(want) {
		t.Fatalf("Last later that day: %s", got)
	}
	next, err := Next(s, want)
	if err != nil || !next.Equal(time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("Next: %v %v", next, err)
	}
	next, _ = Next(s, want.Add(time.Hour))
	if !next.Equal(time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("Next from the repeated hour: %s", next)
	}
}

func TestPeriodKey_UsesTheLocalDateNotUTC(t *testing.T) {
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) // 26 Nov in Kiritimati, 25 Nov (01:00) in Pago Pago
	kiri := PeriodKey(Schedule{Cadence: "daily", Timezone: "Pacific/Kiritimati"}, at)
	pago := PeriodKey(Schedule{Cadence: "daily", Timezone: "Pacific/Pago_Pago"}, at)
	if kiri != "d:2026-11-26" || pago != "d:2026-11-25" {
		t.Fatalf("kiritimati %s, pago pago %s", kiri, pago)
	}
	mAt := time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)
	if k := PeriodKey(Schedule{Cadence: "monthly", MonthDay: 1, Timezone: "Pacific/Kiritimati"}, mAt); k != "m:2026-12" {
		t.Fatalf("monthly %s", k)
	}
	if k := PeriodKey(Schedule{Cadence: "monthly", MonthDay: 1, Timezone: "Pacific/Pago_Pago"}, mAt); k != "m:2026-11" {
		t.Fatalf("monthly %s", k)
	}
}

func TestPeriodKey_WeeklyIsTheISOWeekAcrossAYearEnd(t *testing.T) {
	s := Schedule{Cadence: "weekly", Weekday: 1, Timezone: "Europe/Berlin"}
	want := "w:2026-W01"
	for d := 0; d < 7; d++ {
		at := wall(t, "Europe/Berlin", 2025, time.December, 29+d, 12, 0)
		if got := PeriodKey(s, at); got != want {
			t.Errorf("day %s: %s, want %s", at.Format("2006-01-02"), got, want)
		}
	}
	if got := PeriodKey(s, wall(t, "Europe/Berlin", 2026, time.January, 5, 12, 0)); got != "w:2026-W02" {
		t.Errorf("next week %s", got)
	}
	if got := PeriodKey(s, wall(t, "Europe/Berlin", 2025, time.December, 28, 12, 0)); got != "w:2025-W52" {
		t.Errorf("week before %s", got)
	}
	// Sunday 23:30 UTC is already Monday in Berlin: the week follows the local date.
	if got := PeriodKey(s, time.Date(2026, 1, 4, 23, 30, 0, 0, time.UTC)); got != "w:2026-W02" {
		t.Errorf("local date rule %s", got)
	}
}

func TestPeriodKey_ConsecutiveOccurrencesDiffer(t *testing.T) {
	for _, z := range []string{"Europe/Berlin", "America/New_York", "America/Nuuk", "America/Scoresbysund"} {
		for _, s := range []Schedule{
			{Cadence: "daily", AtMinute: 23*60 + 30, Timezone: z},
			{Cadence: "daily", AtMinute: 2*60 + 30, Timezone: z},
			{Cadence: "weekly", AtMinute: 2*60 + 30, Weekday: 7, Timezone: z},
			{Cadence: "monthly", AtMinute: 2*60 + 30, MonthDay: 28, Timezone: z},
		} {
			// 800 daily steps cross two spring and two autumn changes; weekly and monthly walk the same count.
			at, err := Next(s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			prev := PeriodKey(s, at)
			seen := map[string]bool{prev: true}
			for i := 0; i < 800; i++ {
				nxt, err := Next(s, at)
				if err != nil {
					t.Fatal(err)
				}
				if !nxt.After(at) {
					t.Fatalf("%s %s: Next did not advance from %s", z, s.Cadence, at)
				}
				k := PeriodKey(s, nxt)
				if k == prev || seen[k] {
					t.Fatalf("%s %s: key %s repeats at %s", z, s.Cadence, k, nxt)
				}
				seen[k] = true
				prev, at = k, nxt
			}
		}
	}
}

func TestLastAndNext_AreInverses(t *testing.T) {
	rng := rand.New(rand.NewSource(20261125))
	zones := append(append([]string{}, testZones...), "Australia/Lord_Howe", "America/Sao_Paulo", "Africa/Cairo", "Pacific/Apia")
	cadences := []string{"daily", "weekly", "monthly"}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3000; i++ {
		s := Schedule{Cadence: cadences[rng.Intn(3)], AtMinute: rng.Intn(1440), Timezone: zones[rng.Intn(len(zones))]}
		switch s.Cadence {
		case "weekly":
			s.Weekday = 1 + rng.Intn(7)
		case "monthly":
			s.MonthDay = 1 + rng.Intn(28)
		}
		if i%3 == 0 { // bias towards the wall times where zones change: 01:00 to 04:30, plus 23:30 for Nuuk
			s.AtMinute = (rng.Intn(4)+1)*60 + []int{0, 30}[rng.Intn(2)]
			if i%9 == 0 {
				s.AtMinute = 23*60 + 30
			}
		}
		now := base.Add(time.Duration(rng.Int63n(int64(3 * 366 * 24 * time.Hour))))
		at, err := Last(s, now)
		if err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
		if at.After(now) {
			t.Fatalf("%+v: Last %s after now %s", s, at, now)
		}
		if again, _ := Last(s, at); !again.Equal(at) {
			t.Fatalf("%+v: Last(at) = %s, want %s", s, again, at)
		}
		nxt, err := Next(s, at)
		if err != nil {
			t.Fatal(err)
		}
		if !nxt.After(at) {
			t.Fatalf("%+v: Next %s not after %s", s, nxt, at)
		}
		if nxt.After(now) {
			// Nothing lies between Last(now) and now.
			if n2, _ := Next(s, now); !n2.Equal(nxt) {
				t.Fatalf("%+v: Next(now)=%s, Next(Last)=%s", s, n2, nxt)
			}
		} else {
			t.Fatalf("%+v: occurrence %s lies between Last and now %s", s, nxt, now)
		}
		if back, _ := Last(s, nxt); !back.Equal(nxt) {
			t.Fatalf("%+v: Last(Next) = %s want %s", s, back, nxt)
		}
		if PeriodKey(s, at) == PeriodKey(s, nxt) {
			t.Fatalf("%+v: same key for %s and %s", s, at, nxt)
		}
		// Next from just before an occurrence is that occurrence.
		if n3, _ := Next(s, at.Add(-time.Nanosecond)); !n3.Equal(at) {
			t.Fatalf("%+v: Next(at-1ns) = %s want %s", s, n3, at)
		}
	}
}

func TestLast_DoesNotDependOnTheZoneOfNow(t *testing.T) {
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)
	for _, sz := range testZones {
		s := Schedule{Cadence: "weekly", AtMinute: 7*60 + 15, Weekday: 2, Timezone: sz}
		want, err := Last(s, now)
		if err != nil {
			t.Fatal(err)
		}
		wantNext, _ := Next(s, now)
		for _, nz := range testZones {
			in := now.In(mustLoc(t, nz))
			if got, err := Last(s, in); err != nil || !got.Equal(want) {
				t.Errorf("schedule in %s, now in %s: Last %v %v, want %s", sz, nz, got, err, want)
			}
			if got, _ := Next(s, in); !got.Equal(wantNext) {
				t.Errorf("schedule in %s, now in %s: Next %s, want %s", sz, nz, got, wantNext)
			}
		}
	}
}

// Kiritimati (+14) is already on 26 Nov when Pago Pago (-11) is still on 25 Nov; reading
// the date of now in its own zone would pick the wrong day.
func TestLast_ZoneOfNowDoesNotChangeTheDay(t *testing.T) {
	s := Schedule{Cadence: "daily", AtMinute: 15, Timezone: "Pacific/Kiritimati"}
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC)
	want := time.Date(2026, 11, 25, 10, 15, 0, 0, time.UTC)
	got, err := Last(s, now.In(mustLoc(t, "Pacific/Pago_Pago")))
	if err != nil || got.IsZero() || !got.Equal(want) {
		t.Fatalf("Last: %v %v, want %s", got, err, want)
	}
}

func TestOccurrence_LordHoweHalfHourChanges(t *testing.T) {
	// Spring: 4 Oct 2026 02:00 +10:30 becomes 02:30 +11:00, so 02:15 does not exist.
	spring := Schedule{Cadence: "daily", AtMinute: 2*60 + 15, Timezone: "Australia/Lord_Howe"}
	got, err := Last(spring, time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC); err != nil || !got.Equal(want) {
		t.Fatalf("spring gap: %v %v, want %s", got, err, want)
	}
	// Autumn: 5 Apr 2026 02:00 +11:00 becomes 01:30 +10:30, so 01:45 happens twice.
	autumn := Schedule{Cadence: "daily", AtMinute: 1*60 + 45, Timezone: "Australia/Lord_Howe"}
	got, err = Last(autumn, time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 4, 4, 14, 45, 0, 0, time.UTC); err != nil || !got.Equal(want) {
		t.Fatalf("autumn overlap: %v %v, want %s", got, err, want)
	}
}

// Nuuk and Scoresbysund skip 23:00 to 24:00 on 28 March 2026, so a 23:30
// schedule runs when the gap ends, after midnight, yet it belongs to the 28th.
func TestPeriodKey_IsTheScheduledDateWhenAGapPushesTheInstantPastMidnight(t *testing.T) {
	for _, zone := range []string{"America/Nuuk", "America/Scoresbysund"} {
		s := Schedule{Cadence: "daily", AtMinute: 23*60 + 30, Timezone: zone}
		gapEnd := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)
		got, err := Last(s, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC))
		if err != nil || !got.Equal(gapEnd) {
			t.Fatalf("%s: Last %v %v, want %s", zone, got, err, gapEnd)
		}
		next, _ := Next(s, got)
		prev, _ := Last(s, got.Add(-time.Second))
		keys := []string{PeriodKey(s, prev), PeriodKey(s, got), PeriodKey(s, next)}
		want := []string{"d:2026-03-27", "d:2026-03-28", "d:2026-03-29"}
		for i := range keys {
			if keys[i] != want[i] {
				t.Errorf("%s: keys %v want %v", zone, keys, want)
				break
			}
		}
		if l := PeriodLabel(s, got); l != "28 Mar 2026" {
			t.Errorf("%s: label of the gap occurrence %q", zone, l)
		}
		if l := PeriodLabel(s, next); l != "29 Mar 2026" {
			t.Errorf("%s: label of the next occurrence %q", zone, l)
		}
	}
}

// A period key of an instant that is no occurrence falls back to its local date.
func TestPeriodKey_NonOccurrenceInstantUsesItsLocalDate(t *testing.T) {
	s := Schedule{Cadence: "daily", AtMinute: 23*60 + 30, Timezone: "America/Nuuk"}
	if k := PeriodKey(s, time.Date(2026, 3, 29, 1, 5, 0, 0, time.UTC)); k != "d:2026-03-29" {
		t.Errorf("key %s", k)
	}
}

// Samoa skipped 30 Dec 2011 entirely: clocks went from 29 Dec 23:59 to 31 Dec 00:00.
func TestPeriodKey_ApiaSkippedDay(t *testing.T) {
	from := time.Date(2011, 12, 29, 21, 0, 0, 0, time.UTC) // after 29 Dec 10:00 local
	mid := Schedule{Cadence: "daily", AtMinute: 10 * 60, Timezone: "Pacific/Apia"}
	at, err := Next(mid, from)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2011, 12, 30, 10, 0, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("skipped-day occurrence %s, want the gap end %s", at, want)
	}
	if k := PeriodKey(mid, at); k != "d:2011-12-30" {
		t.Errorf("the skipped day keeps its own key: %s", k)
	}
	nxt, _ := Next(mid, at)
	if k := PeriodKey(mid, nxt); k != "d:2011-12-31" || !nxt.After(at) {
		t.Errorf("next occurrence %s key %s", nxt, k)
	}
	// A midnight schedule has two dates, 30 and 31 Dec, at one instant. The
	// documented choice is that the date of the instant wins: it runs once, as the 31st.
	midnight := Schedule{Cadence: "daily", AtMinute: 0, Timezone: "Pacific/Apia"}
	a, _ := Next(midnight, from)
	b, _ := Next(midnight, a)
	if !b.After(a) || PeriodKey(midnight, a) != "d:2011-12-31" || PeriodKey(midnight, b) != "d:2012-01-01" {
		t.Errorf("midnight: %s %s %s %s", a, b, PeriodKey(midnight, a), PeriodKey(midnight, b))
	}
}

func TestPeriodKeyAndLabel_FarZoneAndYearEnd(t *testing.T) {
	at := time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)
	kiri := Schedule{Cadence: "monthly", MonthDay: 1, Timezone: "Pacific/Kiritimati"}
	if l := PeriodLabel(kiri, at); l != "December 2026" {
		t.Errorf("Kiritimati month label %q", l)
	}
	pago := Schedule{Cadence: "monthly", MonthDay: 1, Timezone: "Pacific/Pago_Pago"}
	if l := PeriodLabel(pago, at); l != "November 2026" {
		t.Errorf("Pago Pago month label %q", l)
	}
	wk := Schedule{Cadence: "weekly", Weekday: 1, Timezone: "Europe/Berlin"}
	if l := PeriodLabel(wk, wall(t, "Europe/Berlin", 2025, time.December, 29, 12, 0)); l != "Week 1, 2026" {
		t.Errorf("year-end week label %q", l)
	}
	if l := PeriodLabel(wk, wall(t, "Europe/Berlin", 2027, time.January, 3, 12, 0)); l != "Week 53, 2026" {
		t.Errorf("week 53 label %q", l)
	}
}

func TestValidate_RefusesNonCanonicalZoneNames(t *testing.T) {
	for _, name := range []string{
		"", "Local", "local", "LOCAL", "localtime", "posixrules", "Factory",
		"posix/Europe/Berlin", "right/Europe/Berlin", "posix/UTC",
		"/Europe/Berlin", "Europe//Berlin", "Europe/", ".", "..", "Europe/../UTC", "./UTC", "Europe/./Berlin",
		"Europe/Nowhere",
	} {
		if err := Validate(Schedule{Cadence: "daily", Timezone: name}); err == nil {
			t.Errorf("zone %q was accepted", name)
		}
	}
	for _, name := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+5"} {
		if err := Validate(Schedule{Cadence: "daily", Timezone: name}); err != nil {
			t.Errorf("zone %q refused: %v", name, err)
		}
	}
}

func TestPeriodLabel(t *testing.T) {
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Cadence: "daily", Timezone: "UTC"}, "25 Nov 2026"},
		{Schedule{Cadence: "weekly", Weekday: 1, Timezone: "UTC"}, "Week 48, 2026"},
		{Schedule{Cadence: "monthly", MonthDay: 1, Timezone: "UTC"}, "November 2026"},
	} {
		if got := PeriodLabel(c.s, at); got != c.want {
			t.Errorf("%s: %q want %q", c.s.Cadence, got, c.want)
		}
	}
	if got := PeriodLabel(Schedule{Cadence: "daily", Timezone: "Pacific/Kiritimati"}, at); !strings.HasPrefix(got, "26 Nov") {
		t.Errorf("local date: %q", got)
	}
}

func TestRangeAndRunsPerMonth(t *testing.T) {
	for cadence, want := range map[string]string{"daily": "24h", "weekly": "7d", "monthly": "30d", "bogus": "7d", "": "7d"} {
		if got := Range(cadence); got != want {
			t.Errorf("Range(%q) = %q want %q", cadence, got, want)
		}
	}
	for cadence, want := range map[string]int{"daily": 31, "weekly": 5, "monthly": 1} {
		if got := RunsPerMonth(cadence); got != want {
			t.Errorf("RunsPerMonth(%q) = %d want %d", cadence, got, want)
		}
	}
	if err := Validate(Schedule{Cadence: "bogus", Timezone: "UTC"}); err == nil {
		t.Error("Validate must refuse an unknown cadence")
	}
}

func TestCanonicalZoneName_Refusals(t *testing.T) {
	for _, name := range []string{"localtime", "LocalTime", "posix/UTC", "right/UTC", "Europe//Berlin", ".."} {
		if err := canonicalZoneName(name); err == nil {
			t.Errorf("%q passed", name)
		}
	}
	if err := canonicalZoneName("Europe/Berlin"); err != nil {
		t.Error(err)
	}
}

// Apia skipped Friday 30 Dec 2011, so a Friday midnight schedule runs at the instant
// Saturday starts. It keeps the Friday's week, and runs once.
func TestPeriodKey_WeeklyAcrossASkippedWeekday(t *testing.T) {
	s := Schedule{Cadence: "weekly", AtMinute: 0, Weekday: 5, Timezone: "Pacific/Apia"}
	at, err := Next(s, time.Date(2011, 12, 29, 21, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2011, 12, 30, 10, 0, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("occurrence %s, want %s", at, want)
	}
	if k, l := PeriodKey(s, at), PeriodLabel(s, at); k != "w:2011-W52" || l != "Week 52, 2011" {
		t.Errorf("key %s label %s", k, l)
	}
	next, _ := Next(s, at)
	if want := time.Date(2012, 1, 5, 10, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next %s, want %s", next, want)
	}
	if k := PeriodKey(s, next); k != "w:2012-W01" {
		t.Errorf("next key %s", k)
	}
}
