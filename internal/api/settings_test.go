package api

import (
	"net/http"
	"testing"
)

func putWindow(c *apiClient, body any) *http.Response {
	return c.do(http.MethodPut, "/api/settings", body, nil)
}

func windowOf(t *testing.T, resp *http.Response) int {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out SettingsDTO
	decode(t, resp, &out)
	return out.StoppedTasksWindowMinutes
}

func TestSettings_DefaultReadableByEveryone(t *testing.T) {
	srv, _, _ := newTestServer(t)
	omer := login(t, srv, "omer", testPassword)
	if got := windowOf(t, omer.get("/api/settings")); got != 10 {
		t.Errorf("default window = %d, want 10", got)
	}
}

func TestSettings_PutAdminOnlyAndAudited(t *testing.T) {
	srv, _, st := newTestServer(t)
	omer := login(t, srv, "omer", testPassword)
	pavel := login(t, srv, "pavel", testPassword)

	resp := putWindow(omer, map[string]any{"stopped_tasks_window_minutes": 5})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("omer PUT = %d, want 403", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "forbidden" {
		t.Errorf("code = %q, want forbidden", code)
	}

	if got := windowOf(t, putWindow(pavel, map[string]any{"stopped_tasks_window_minutes": 5})); got != 5 {
		t.Errorf("PUT result = %d, want 5", got)
	}
	if got := windowOf(t, omer.get("/api/settings")); got != 5 {
		t.Errorf("omer GET = %d, want 5", got)
	}
	entry := lastAudit(t, st)
	if entry.Action != "settings_update" || entry.Target != "setting:stopped_tasks_window_minutes" ||
		entry.Summary != "Changed the stopped tasks window from 10 to 5 minutes." {
		t.Errorf("audit entry = %+v", entry)
	}
	if entry.Details["from"] != float64(10) || entry.Details["to"] != float64(5) {
		t.Errorf("audit details = %v", entry.Details)
	}

	if got := windowOf(t, putWindow(pavel, map[string]any{"stopped_tasks_window_minutes": 5})); got != 5 {
		t.Errorf("repeat PUT = %d, want 5", got)
	}
	if again := lastAudit(t, st); again.ID != entry.ID {
		t.Errorf("unchanged PUT wrote an audit row: %+v", again)
	}
}

func TestSettings_PutValidation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	for name, body := range map[string]any{
		"zero":          map[string]any{"stopped_tasks_window_minutes": 0},
		"too large":     map[string]any{"stopped_tasks_window_minutes": 1441},
		"missing":       map[string]any{},
		"unknown field": map[string]any{"stopped_tasks_window_minutes": 1, "x": 1},
	} {
		resp := putWindow(pavel, body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, n := range []int{1, 1440} {
		if got := windowOf(t, putWindow(pavel, map[string]any{"stopped_tasks_window_minutes": n})); got != n {
			t.Errorf("PUT %d returned %d", n, got)
		}
	}
}
