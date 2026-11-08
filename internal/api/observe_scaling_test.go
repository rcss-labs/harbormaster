package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The seed (fake.mockupScaling) gives payments-api min 2, max 6, a cpu-target tracking policy at 60%
// with 60/300 cooldowns, and two scheduled actions named scheduled-1 and scheduled-2. The test pins the
// wire JSON, not the Go DTO, so renamed or missing keys fail here.
func TestGetScaling_ViewerSeesConfig(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	pollAll(t, st, fk)

	resp := login(t, srv, "dana", testPassword).get("/api/services/" + paymentsID() + "/scaling")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)

	for _, key := range []string{"has_target", "min", "max", "policies", "scheduled_actions", "activities", "polled_at"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response is missing %q", key)
		}
	}
	if body["has_target"] != true || body["min"] != float64(2) || body["max"] != float64(6) {
		t.Errorf("has_target, min, max = %v, %v, %v; want true, 2, 6", body["has_target"], body["min"], body["max"])
	}
	if polled, _ := body["polled_at"].(string); polled == "" {
		t.Errorf("polled_at = %v; want a timestamp after a poll", body["polled_at"])
	}

	policies := jsonArray(t, body, "policies")
	if len(policies) != 1 {
		t.Fatalf("policies = %d, want 1", len(policies))
	}
	assertFields(t, "policies[0]", policies[0], map[string]any{
		"name": "cpu-target", "type": "TargetTrackingScaling", "metric": "ECSServiceAverageCPUUtilization",
		"target_value": float64(60), "scale_out_cooldown": float64(60), "scale_in_cooldown": float64(300),
	})

	actions := jsonArray(t, body, "scheduled_actions")
	if len(actions) != 2 {
		t.Fatalf("scheduled_actions = %d, want 2", len(actions))
	}
	assertFields(t, "scheduled_actions[0]", actions[0], map[string]any{
		"name": "scheduled-1", "schedule": "cron(0 7 ? * MON-FRI *)", "timezone": "",
		"min_capacity": float64(3), "max_capacity": nil,
	})
	assertFields(t, "scheduled_actions[1]", actions[1], map[string]any{
		"name": "scheduled-2", "schedule": "cron(0 20 ? * MON-FRI *)", "timezone": "",
		"min_capacity": float64(2), "max_capacity": nil,
	})

	activities := jsonArray(t, body, "activities")
	if len(activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(activities))
	}
	activity := activities[0]
	for _, key := range []string{"id", "description", "cause", "status_code", "status_message", "started_at", "ended_at"} {
		if _, ok := activity[key]; !ok {
			t.Errorf("activities[0] is missing %q", key)
		}
	}
	if id, _ := activity["id"].(string); id == "" {
		t.Error("activities[0].id is empty")
	}
	if description, _ := activity["description"].(string); !strings.HasPrefix(description, "Setting desired count to ") {
		t.Errorf("activities[0].description = %q; want the seeded desired-count text", description)
	}
	if activity["cause"] == "" || activity["status_code"] != "Successful" || activity["status_message"] == "" {
		t.Errorf("activities[0] cause, status_code, status_message = %v, %v, %v; want non-empty cause and status_message, status_code Successful",
			activity["cause"], activity["status_code"], activity["status_message"])
	}
	if started, _ := activity["started_at"].(string); started == "" {
		t.Error("activities[0].started_at is empty")
	}
}

// jsonArray returns body[key] as a list of objects, failing the test if it is missing or not an array.
func jsonArray(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("%s = %v; want a JSON array", key, body[key])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s entry = %v; want an object", key, item)
		}
		out = append(out, obj)
	}
	return out
}

// assertFields checks that every expected key has the expected JSON value in obj.
func assertFields(t *testing.T, label string, obj map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got := obj[key]; got != value {
			t.Errorf("%s.%s = %v (%T); want %v", label, key, got, got, value)
		}
	}
}

func TestGetScaling_NoViewIs404(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)

	resp := login(t, srv, "omer", testPassword).get("/api/services/" + paymentsID() + "/scaling")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

func TestGetScaling_NeverPolledIs200Empty(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	key, err := model.ParseServiceID(paymentsID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(t.Context(),
		`delete from scaling where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		key.AccountID, key.Region, key.Cluster, key.Service); err != nil {
		t.Fatal(err)
	}

	resp := login(t, srv, "sam", testPassword).get("/api/services/" + paymentsID() + "/scaling")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	decode(t, resp, &raw)

	if string(raw["has_target"]) != "false" || string(raw["polled_at"]) != "null" {
		t.Errorf("has_target = %s, polled_at = %s; want false, null", raw["has_target"], raw["polled_at"])
	}
	for _, field := range []string{"policies", "scheduled_actions", "activities"} {
		if string(raw[field]) != "[]" {
			t.Errorf("%s = %s; want []", field, raw[field])
		}
	}
}
