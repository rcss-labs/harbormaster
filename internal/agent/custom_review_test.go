package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// The golden files hold what phase 3 sent, made before the custom type existed. Nothing regenerates them.
func TestReviewer_BuiltInRunsSendTheSameBytes(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range []string{"cost", "security", "reliability", "capacity"} {
		for scope, empty := range map[string]bool{"three-entries": false, "empty-scope": true} {
			task := reviewTask(id)
			if empty {
				task.Scope.Entries = nil
			}
			req := firstRequest(t, task)
			if len(req.Messages) != 1 || req.Messages[0].Role != provider.RoleUser {
				t.Fatalf("%s, %s: messages = %+v", id, scope, req.Messages)
			}
			for part, got := range map[string]string{"system": req.System, "message": req.Messages[0].Text} {
				name := "review-" + id + "-" + scope + "." + part + ".txt"
				seen[name] = true
				want, err := os.ReadFile(filepath.Join("testdata", name))
				if err != nil {
					t.Error(err)
				} else if got != string(want) {
					t.Errorf("%s: sent\n%q\nwant\n%q", name, got, want)
				}
			}
		}
	}
	files, err := filepath.Glob(filepath.Join("testdata", "review-*.txt"))
	if err != nil || len(files) != len(seen) {
		t.Errorf("%d golden files, %d compared, err %v", len(files), len(seen), err)
	}
}
