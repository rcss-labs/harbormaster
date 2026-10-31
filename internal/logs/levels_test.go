package logs

import "testing"

func TestLevel_Table(t *testing.T) {
	tests := []struct{ msg, want string }{
		{"level=error x", "error"},
		{`{"level":"warn"}`, "warn"},
		{"ERROR: boom", "error"},
		{"Errors: 0", ""},
		{"[WARN] x", "warn"},
		{"warned", ""},
		{"level=warning slow", "warn"},
		{"FATAL: out of memory", "error"},
		{"GET /health 200 2ms", ""},
	}
	for _, tc := range tests {
		if got := Level(tc.msg); got != tc.want {
			t.Errorf("Level(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}
