package api

import (
	"net/http"
	"testing"
)

// The fake estate holds a Spot-interrupted and a deployment-scaled task that both exited 1: the API marks them
// expected (with a cause) and leaves them out of the error counts.
func TestTasks_ExpectedStopsAreMarkedAndNotCountedAsErrors(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "sam", testPassword)
	if resp := putWindow(c, map[string]any{"stopped_tasks_window_minutes": 90}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	got := getTasks(t, c, "/api/tasks?filter=stopped&limit=500")
	causes := map[string]int{}
	for _, task := range got.Items {
		if task.StopKind == "expected" {
			causes[task.StopCause]++
			if task.ExitCode == nil || *task.ExitCode == 0 {
				t.Errorf("expected stop %s has exit %v, want non-zero", task.ShortID, task.ExitCode)
			}
		}
	}
	if causes["spot"] != 1 || causes["scaling"] != 1 {
		t.Errorf("expected stop causes = %v, want one spot and one scaling", causes)
	}
	if got.Summary.ByStatus.StoppedExpected != 2 {
		t.Errorf("stopped_expected = %d, want 2", got.Summary.ByStatus.StoppedExpected)
	}
	problems := getTasks(t, c, "/api/tasks?filter=problems&limit=500")
	for _, task := range problems.Items {
		if task.StopKind == "expected" {
			t.Errorf("problems filter lists expected stop %s", task.ShortID)
		}
	}
}
