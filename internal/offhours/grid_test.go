package offhours

import (
	"strings"
	"testing"
)

// weekdaysWeek returns a grid awake on the given days from startHour (inclusive) to endHour (exclusive).
func weekdaysWeek(days []int, startHour, endHour int) Week {
	var w Week
	for _, d := range days {
		for h := startHour; h < endHour; h++ {
			w[d*24+h] = true
		}
	}
	return w
}

var monToFri = []int{0, 1, 2, 3, 4}

func TestParseWeekRoundTrip(t *testing.T) {
	w := weekdaysWeek(monToFri, 7, 20)
	parsed, err := ParseWeek(w.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != w {
		t.Fatal("round trip changed the grid")
	}
	if w.AwakeHours() != 65 {
		t.Fatalf("awake hours = %d, want 65", w.AwakeHours())
	}
	if WeekFromRows(w.Rows()) != w {
		t.Fatal("rows round trip changed the grid")
	}
	if _, err := ParseWeek("101"); err == nil {
		t.Fatal("short string accepted")
	}
	if _, err := ParseWeek(strings.Repeat("2", 168)); err == nil {
		t.Fatal("non 0/1 string accepted")
	}
}

func TestValidate(t *testing.T) {
	valid := weekdaysWeek(monToFri, 7, 20)
	cases := []struct {
		name, tagKey, tagValue, tz string
		w                          Week
		wantErr                    string
	}{
		{"empty grid", "env", "dev", "UTC", Week{}, "Paint at least one awake hour. To stop a service for good, set its desired count to 0."},
		{"local zone", "env", "dev", "Local", valid, "timezone"},
		{"empty zone", "env", "dev", "", valid, "timezone"},
		{"unknown zone", "env", "dev", "Mars/Phobos", valid, "timezone"},
		{"aws tag key", "aws:foo", "dev", "UTC", valid, "aws:"},
		{"empty tag key", "", "dev", "UTC", valid, "tag key"},
		{"long tag value", "env", strings.Repeat("v", 257), "UTC", valid, "tag value"},
		{"blank name", "env", "dev", "UTC", valid, "name"},
		{"long name", "env", "dev", "UTC", valid, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "nightly"
			switch tc.name {
			case "blank name":
				name = "   "
			case "long name":
				name = strings.Repeat("n", 81)
			}
			loc, err := Validate(name, tc.tagKey, tc.tagValue, tc.tz, tc.w)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if loc != nil {
				t.Fatal("location returned alongside error")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		loc, err := Validate("nightly", "env", "dev", "Asia/Jerusalem", valid)
		if err != nil {
			t.Fatal(err)
		}
		if loc == nil || loc.String() != "Asia/Jerusalem" {
			t.Fatalf("loc = %v", loc)
		}
	})
	t.Run("empty tag value allowed", func(t *testing.T) {
		if _, err := Validate("nightly", "env", "", "UTC", valid); err != nil {
			t.Fatal(err)
		}
	})
}
