package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The seed (fake.mockupScaling) gives payments-api min 2, max 6, a cpu-target tracking policy at 60%
// with 60/300 cooldowns, and two scheduled actions named scheduled-1 and scheduled-2.
func TestGetScaling_ViewerSeesConfig(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)

	resp := login(t, srv, "pavel", testPassword).get("/api/services/" + paymentsID() + "/scaling")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out ScalingDTO
	decode(t, resp, &out)

	if !out.HasTarget || out.Min != 2 || out.Max != 6 {
		t.Errorf("capacity = has_target %v min %d max %d; want true, 2, 6", out.HasTarget, out.Min, out.Max)
	}
	if len(out.Policies) != 1 {
		t.Fatalf("policies = %d, want 1", len(out.Policies))
	}
	policy := out.Policies[0]
	if policy.Name != "cpu-target" || policy.Type != "TargetTrackingScaling" || policy.Metric != "ECSServiceAverageCPUUtilization" ||
		policy.TargetValue != 60 || policy.ScaleOutCooldown != 60 || policy.ScaleInCooldown != 300 {
		t.Errorf("policy = %+v; want cpu-target TargetTrackingScaling ECSServiceAverageCPUUtilization 60 60/300", policy)
	}
	if len(out.ScheduledActions) != 2 {
		t.Fatalf("scheduled_actions = %d, want 2", len(out.ScheduledActions))
	}
	first := out.ScheduledActions[0]
	if first.Name != "scheduled-1" || first.Schedule != "cron(0 7 ? * MON-FRI *)" || first.MinCapacity == nil ||
		*first.MinCapacity != 3 || first.MaxCapacity != nil {
		t.Errorf("first scheduled action = %+v; want scheduled-1, cron(0 7 ? * MON-FRI *), min 3, max null", first)
	}
	if len(out.Activities) != 1 || out.Activities[0].StatusCode != "Successful" {
		t.Errorf("activities = %+v; want one Successful activity", out.Activities)
	}
	if out.PolledAt == nil {
		t.Error("polled_at is null after a poll")
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

	resp := login(t, srv, "pavel", testPassword).get("/api/services/" + paymentsID() + "/scaling")
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
