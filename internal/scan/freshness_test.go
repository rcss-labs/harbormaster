package scan

import (
	"testing"
	"time"
)

func TestDBFreshness_Bands(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 120 * time.Hour
	ago := func(h int) *time.Time { v := now.Add(-time.Duration(h) * time.Hour); return &v }
	tests := []struct {
		built *time.Time
		want  Freshness
	}{
		{ago(35), "fresh"}, {ago(36), "warn"}, {ago(119), "warn"}, {ago(120), "stale"}, {nil, "none"},
	}
	for _, tt := range tests {
		if got := DBFreshness(tt.built, now, staleAfter); got != tt.want {
			t.Errorf("DBFreshness(%v) = %q, want %q", tt.built, got, tt.want)
		}
	}
}
