package offhours

import (
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

func TestStateAt(t *testing.T) {
	loc := mustLoc(t, "Asia/Jerusalem")
	w := weekdaysWeek(monToFri, 7, 20)

	// 2026-11-07 is a Saturday.
	sat := time.Date(2026, 11, 7, 23, 0, 0, 0, loc)
	s := StateAt(w, loc, sat, nil)
	monWake := time.Date(2026, 11, 9, 7, 0, 0, 0, loc)
	if s.Kind != Asleep || s.Until == nil || !s.Until.Equal(monWake) {
		t.Fatalf("sat: %+v", s)
	}

	tue := time.Date(2026, 11, 10, 10, 0, 0, 0, loc)
	s = StateAt(w, loc, tue, nil)
	tueSleep := time.Date(2026, 11, 10, 20, 0, 0, 0, loc)
	if s.Kind != Awake || s.Until == nil || !s.Until.Equal(tueSleep) {
		t.Fatalf("tue: %+v", s)
	}

	woken := sat.Add(time.Hour)
	s = StateAt(w, loc, sat, &woken)
	if s.Kind != Woken || s.Until == nil || !s.Until.Equal(woken) {
		t.Fatalf("woken: %+v", s)
	}
	past := sat.Add(-time.Minute)
	if s = StateAt(w, loc, sat, &past); s.Kind != Asleep {
		t.Fatalf("expired wake: %+v", s)
	}
}

func TestStateAt_AlwaysOnHasNoUntil(t *testing.T) {
	var w Week
	for i := range w {
		w[i] = true
	}
	s := StateAt(w, time.UTC, time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), nil)
	if s.Kind != Awake || s.Until != nil {
		t.Fatalf("%+v", s)
	}
}

func TestStateAt_AcrossDSTChange(t *testing.T) {
	loc := mustLoc(t, "Europe/Berlin")
	var days = []int{0, 1, 2, 3, 4, 5, 6}
	w := weekdaysWeek(days, 7, 20)
	now := time.Date(2026, 3, 28, 21, 0, 0, 0, loc)
	s := StateAt(w, loc, now, nil)
	want := time.Date(2026, 3, 29, 5, 0, 0, 0, time.UTC)
	if s.Kind != Asleep || s.Until == nil || !s.Until.Equal(want) {
		t.Fatalf("%+v want %v", s, want)
	}
}

func TestNextEdge_NoneWhenGridConstant(t *testing.T) {
	var w Week
	if _, ok := NextEdge(w, time.UTC, time.Now(), true); ok {
		t.Fatal("edge found in empty grid")
	}
}

func TestWakeUntil_ClampsToNextWakeEdge(t *testing.T) {
	loc := mustLoc(t, "Asia/Jerusalem")
	w := weekdaysWeek(monToFri, 7, 20)
	mon6 := time.Date(2026, 11, 9, 6, 0, 0, 0, loc)
	if got, want := WakeUntil(w, loc, mon6), time.Date(2026, 11, 9, 7, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("mon 06:00 got %v want %v", got, want)
	}
	sat := time.Date(2026, 11, 7, 23, 0, 0, 0, loc)
	if got, want := WakeUntil(w, loc, sat), time.Date(2026, 11, 8, 1, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("sat 23:00 got %v want %v", got, want)
	}
}

func TestInGrace(t *testing.T) {
	loc := mustLoc(t, "Asia/Jerusalem")
	w := weekdaysWeek(monToFri, 7, 20)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, loc) }
	if !InGrace(w, loc, at(7, 3), nil) {
		t.Fatal("07:03 should be in grace")
	}
	if InGrace(w, loc, at(7, 6), nil) {
		t.Fatal("07:06 should be out of grace")
	}
	if InGrace(w, loc, at(10, 3), nil) {
		t.Fatal("10:03 is not near an edge")
	}
	last := at(10, 1)
	if !InGrace(w, loc, at(10, 3), &last) {
		t.Fatal("2 minutes after lastApplied should be in grace")
	}
	if InGrace(w, loc, at(10, 7), &last) {
		t.Fatal("6 minutes after lastApplied should be out")
	}
}
