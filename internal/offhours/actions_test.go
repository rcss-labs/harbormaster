package offhours

import (
	"reflect"
	"testing"
	"time"
)

func TestDesiredActions_WeekdaysPreset(t *testing.T) {
	w := weekdaysWeek(monToFri, 7, 20)
	got := DesiredActions(w, "Asia/Jerusalem", Capacity{Min: 1, Max: 3})
	days := "MON,TUE,WED,THU,FRI"
	want := []Action{
		{Name: "harbormaster-offhours-sleep-20", Schedule: "cron(0 20 ? * " + days + " *)", Timezone: "Asia/Jerusalem", Capacity: Capacity{0, 0}},
		{Name: "harbormaster-offhours-wake-07", Schedule: "cron(0 7 ? * " + days + " *)", Timezone: "Asia/Jerusalem", Capacity: Capacity{1, 3}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDesiredActions_AlwaysOnHasNone(t *testing.T) {
	var w Week
	for i := range w {
		w[i] = true
	}
	if got := DesiredActions(w, "UTC", Capacity{1, 1}); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDesiredActions_WrapsAcrossSunday(t *testing.T) {
	var w Week
	for _, i := range []int{6*24 + 22, 6*24 + 23, 0, 1} {
		w[i] = true
	}
	got := DesiredActions(w, "UTC", Capacity{1, 1})
	want := []Action{
		{Name: "harbormaster-offhours-sleep-02", Schedule: "cron(0 2 ? * MON *)", Timezone: "UTC", Capacity: Capacity{0, 0}},
		{Name: "harbormaster-offhours-wake-22", Schedule: "cron(0 22 ? * SUN *)", Timezone: "UTC", Capacity: Capacity{1, 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDesiredActions_SplitDayGroupsByHour(t *testing.T) {
	w := weekdaysWeek([]int{0}, 7, 12)
	for h := 13; h < 20; h++ {
		w[h] = true
	}
	got := DesiredActions(w, "UTC", Capacity{2, 4})
	var names, scheds []string
	for _, a := range got {
		names = append(names, a.Name)
		scheds = append(scheds, a.Schedule)
	}
	wantNames := []string{
		"harbormaster-offhours-sleep-12", "harbormaster-offhours-sleep-20",
		"harbormaster-offhours-wake-07", "harbormaster-offhours-wake-13",
	}
	wantScheds := []string{"cron(0 12 ? * MON *)", "cron(0 20 ? * MON *)", "cron(0 7 ? * MON *)", "cron(0 13 ? * MON *)"}
	if !reflect.DeepEqual(names, wantNames) || !reflect.DeepEqual(scheds, wantScheds) {
		t.Fatalf("names %v scheds %v", names, scheds)
	}
}

func TestWokenActionAndIsOwned(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	until := time.Date(2026, 11, 9, 9, 30, 0, 0, loc).UTC()
	a := WokenAction(until, "Asia/Jerusalem")
	want := Action{Name: "harbormaster-offhours-woken-until", Schedule: "at(2026-11-09T09:30:00)", Timezone: "Asia/Jerusalem", Capacity: Capacity{0, 0}}
	if a != want {
		t.Fatalf("got %+v", a)
	}
	if !IsOwned(a.Name) || !IsOwned("harbormaster-offhours-wake-07") || IsOwned("my-own-action") {
		t.Fatal("IsOwned wrong")
	}
}
