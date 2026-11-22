package config

import (
	"testing"
	"time"
)

func TestConfig_FocusDefaults(t *testing.T) {
	cfg, err := loadWithKV(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	want := FocusConfig{Interval: 5 * time.Second, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 5}
	if cfg.Focus != want {
		t.Fatalf("Focus = %+v, want %+v", cfg.Focus, want)
	}
}

func TestConfig_FocusReadsEveryVariable(t *testing.T) {
	cfg, err := loadWithKV(t, map[string]string{
		"HM_FOCUS_INTERVAL": "3s", "HM_FOCUS_TTL": "45s", "HM_FOCUS_MAX_OBJECTS": "10",
		"HM_FOCUS_MAX_PER_USER": "2", "HM_FOCUS_MAX_CALLS_PER_SEC": "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := FocusConfig{Interval: 3 * time.Second, TTL: 45 * time.Second, MaxObjects: 10, MaxPerUser: 2, MaxCallsPerSec: 7}
	if cfg.Focus != want {
		t.Fatalf("Focus = %+v, want %+v", cfg.Focus, want)
	}
}

func TestConfig_FocusIntervalBelowTwoSecondsIsRejected(t *testing.T) {
	_, err := loadWithKV(t, map[string]string{"HM_FOCUS_INTERVAL": "1s"})
	if err == nil || err.Error() != "HM_FOCUS_INTERVAL: must be at least 2s" {
		t.Fatalf("err = %v", err)
	}
}

func TestConfig_FocusTTLAndCountsAreValidated(t *testing.T) {
	cases := []struct{ env, value, want string }{
		{"HM_FOCUS_TTL", "12s", "HM_FOCUS_TTL: must be longer than 12s"},
		{"HM_FOCUS_MAX_OBJECTS", "0", "HM_FOCUS_MAX_OBJECTS: must be at least 1"},
		{"HM_FOCUS_MAX_PER_USER", "0", "HM_FOCUS_MAX_PER_USER: must be at least 1"},
		{"HM_FOCUS_MAX_CALLS_PER_SEC", "0", "HM_FOCUS_MAX_CALLS_PER_SEC: must be at least 1"},
	}
	for _, c := range cases {
		_, err := loadWithKV(t, map[string]string{c.env: c.value})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s=%s: err = %v, want %q", c.env, c.value, err, c.want)
		}
	}
}
