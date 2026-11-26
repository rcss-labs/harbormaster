package store_test

import (
	"math"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestFitsBudget(t *testing.T) {
	for name, tc := range map[string]struct {
		budget, settled, reserved, request int64
		want                               bool
	}{
		"plenty":                      {1000, 100, 100, 500, true},
		"exactly the room left":       {1000, 400, 100, 500, true},
		"one over the room left":      {1000, 400, 100, 501, false},
		"allowance 0 covers nothing":  {0, 0, 0, 0, false},
		"spent more than the budget":  {1000, 1001, 0, 0, false},
		"reserved more than the room": {1000, 600, 401, 0, false},
		"at the int64 cap, nothing":   {math.MaxInt64, math.MaxInt64, math.MaxInt64, 1, false},
		"at the int64 cap, reserved":  {math.MaxInt64, 1, math.MaxInt64, 0, false},
		"at the int64 cap, room":      {math.MaxInt64, 0, 0, math.MaxInt64, true},
		"free run, room":              {1, 0, 0, 0, true},
	} {
		if got := store.FitsBudget(tc.budget, tc.settled, tc.reserved, tc.request); got != tc.want {
			t.Errorf("%s: FitsBudget(%d, %d, %d, %d) = %v, want %v", name, tc.budget, tc.settled, tc.reserved, tc.request, got, tc.want)
		}
	}
}
