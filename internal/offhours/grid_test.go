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
		name, tz string
		w        Week
		wantErr  string
	}{
		{"empty grid", "UTC", Week{}, "Paint at least one awake hour. To stop a service for good, set its desired count to 0."},
		{"local zone", "Local", valid, "timezone"},
		{"empty zone", "", valid, "timezone"},
		{"unknown zone", "Mars/Phobos", valid, "timezone"},
		{"blank name", "UTC", valid, "name"},
		{"long name", "UTC", valid, "name"},
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
			loc, err := Validate(name, tc.tz, tc.w)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if loc != nil {
				t.Fatal("location returned alongside error")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		loc, err := Validate("nightly", "Asia/Jerusalem", valid)
		if err != nil {
			t.Fatal(err)
		}
		if loc == nil || loc.String() != "Asia/Jerusalem" {
			t.Fatalf("loc = %v", loc)
		}
	})
}

func TestValidateAWSTag(t *testing.T) {
	cases := []struct{ name, key, value, wantErr string }{
		{"aws prefix", "aws:foo", "dev", "aws:"},
		{"empty key", "", "dev", "tag key"},
		{"long key", strings.Repeat("k", 129), "dev", "tag key"},
		{"long value", "env", strings.Repeat("v", 257), "tag value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAWSTag(tc.key, tc.value); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	t.Run("empty value allowed", func(t *testing.T) {
		if err := ValidateAWSTag("env", ""); err != nil {
			t.Fatal(err)
		}
	})
}
