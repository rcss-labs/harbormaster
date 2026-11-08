package api

import (
	"context"
	"testing"
)

// TestServiceOffHours_UnenrolledStateFromPolledScaling pins the state of a service the schedule matches but has not
// enrolled yet: it is read from the polled scaling snapshot, never from AWS.
func TestServiceOffHours_UnenrolledStateFromPolledScaling(t *testing.T) {
	const (
		zeroCapacity   = "Not managed: already at 0 tasks."
		minZero        = "Not managed: its auto scaling minimum is 0, so it could not wake on its own."
		needsAttention = "Needs attention: asleep with no wake action. Check its scheduled actions."
		sleepOnly      = `[{"Name":"harbormaster-offhours-sleep-20","Schedule":"cron(0 20 * * ? *)"}]`
		wakeAndSleep   = `[{"Name":"harbormaster-offhours-wake-07","MinCapacity":2,"MaxCapacity":4},{"Name":"harbormaster-offhours-sleep-20"}]`
	)
	cases := []struct {
		name       string
		desired    int
		hasTarget  bool
		min, max   int
		actions    string
		wantState  string
		wantReason string
	}{
		{"running without a target", 1, false, 0, 0, `[]`, "pending", ""},
		{"desired 0 without a target", 0, false, 0, 0, `[]`, "not_managed", zeroCapacity},
		{"target at 0/0", 0, true, 0, 0, `[]`, "not_managed", zeroCapacity},
		{"target with minimum 0", 1, true, 0, 4, `[]`, "not_managed", minZero},
		{"target at 0/0 with actions but no wake action", 0, true, 0, 0, sleepOnly, "pending", needsAttention},
		{"target at 0/0 with a recoverable wake action", 0, true, 0, 0, wakeAndSleep, "pending", ""},
		{"ordinary target", 1, true, 1, 4, `[]`, "pending", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newOffHoursEnv(t, saturdayNight(t))
			ctx := context.Background()
			env.createSchedule(t, env.admin(t), scheduleBody("staging nights", "env", "staging"))
			if _, err := env.st.Pool.Exec(ctx, `update services set desired = $5 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
				stagingWebKey.AccountID, stagingWebKey.Region, stagingWebKey.Cluster, stagingWebKey.Service, tc.desired); err != nil {
				t.Fatal(err)
			}
			if _, err := env.st.Pool.Exec(ctx, `insert into scaling (account_id, region, cluster_name, service_name, has_target, min_capacity, max_capacity,
				  policies, scheduled_actions, activities, polled_at)
				 values ($1, $2, $3, $4, $5, $6, $7, '[]', $8::jsonb, '[]', now())
				 on conflict (account_id, region, cluster_name, service_name) do update set has_target = excluded.has_target,
				  min_capacity = excluded.min_capacity, max_capacity = excluded.max_capacity, scheduled_actions = excluded.scheduled_actions`,
				stagingWebKey.AccountID, stagingWebKey.Region, stagingWebKey.Cluster, stagingWebKey.Service, tc.hasTarget, tc.min, tc.max, tc.actions); err != nil {
				t.Fatal(err)
			}

			var state struct {
				Enrolled     bool   `json:"enrolled"`
				ScheduleName string `json:"schedule_name"`
				State        string `json:"state"`
				Reason       string `json:"reason"`
			}
			decode(t, login(t, env.srv, "oncall", testPassword).get(serviceOffHoursPath(stagingWebKey, "")), &state)
			if state.Enrolled || state.ScheduleName != "staging nights" || state.State != tc.wantState || state.Reason != tc.wantReason {
				t.Errorf("service state = %+v, want not enrolled on staging nights, %s %q", state, tc.wantState, tc.wantReason)
			}

			var detail scheduleResp
			decode(t, env.admin(t).get("/api/offhours/schedules/"+itoa(1)), &detail)
			for _, svc := range detail.Services {
				if svc.Service == "web" && (svc.State != tc.wantState || svc.Reason != tc.wantReason) {
					t.Errorf("schedule row = %s %q, want %s %q", svc.State, svc.Reason, tc.wantState, tc.wantReason)
				}
			}
		})
	}
}

func TestServiceOffHours_NoScheduleMatches(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	var state struct {
		Enrolled     bool   `json:"enrolled"`
		ScheduleName string `json:"schedule_name"`
		State        string `json:"state"`
	}
	decode(t, login(t, env.srv, "oncall", testPassword).get(serviceOffHoursPath(stagingWebKey, "")), &state)
	if state.Enrolled || state.ScheduleName != "" || state.State != "" {
		t.Errorf("state = %+v, want an empty answer", state)
	}
}
