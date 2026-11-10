package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// enrolPaymentsAsleep seeds a polled estate and puts payments-api on a schedule that is never awake.
func enrolPaymentsAsleep(t *testing.T, st *store.Store) {
	t.Helper()
	user, err := st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := st.CreateOffHoursSchedule(t.Context(), store.OffHoursSchedule{
		Name: "never awake", Enabled: true, Selectors: []store.OffHoursSelector{{Kind: "aws", Key: "env", Value: "prod"}}, Timezone: "UTC", Awake: strings.Repeat("0", 168)}, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = st.InTx(t.Context(), func(tx store.Tx) error {
		return st.UpsertOffHoursEnrolment(t.Context(), tx, store.OffHoursEnrolment{
			Key:        model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"},
			ScheduleID: &sc.ID, SavedMin: 1, SavedMax: 4, State: "active"})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newGuardedEnv(t *testing.T) *apiClient {
	t.Helper()
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		s.Deploy.Guard = &offhours.Guard{Store: s.Store, Now: time.Now}
	})
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	enrolPaymentsAsleep(t, st)
	return login(t, srv, "sam", testPassword)
}

func wantOffHoursConflictBody(t *testing.T, resp *http.Response, reason string) {
	t.Helper()
	wantStatus(t, resp, http.StatusConflict)
	want := `payments-api is on the off-hours schedule "never awake". ` + reason
	if code, msg := errorCode(t, resp); code != "conflict" || msg != want {
		t.Errorf("error = %s %q, want conflict %q", code, msg, want)
	}
}

// A desired-count change on a service that is asleep on an off-hours schedule answers 409 with the schedule's message.
func TestDesiredCount_OffHoursConflictIs409(t *testing.T) {
	admin := newGuardedEnv(t)
	resp := admin.post("/api/services/"+paymentsID()+"/desired-count", map[string]any{"desired": 5})
	wantOffHoursConflictBody(t, resp, "It is asleep: wake it first.")
}

func TestChangeSet_OffHoursConflictIs409(t *testing.T) {
	admin := newGuardedEnv(t)
	resp := admin.post(changesetPath(), map[string]any{
		"base_version": "v", "scaling": map[string]any{"delete_scheduled_actions": []string{"harbormaster-offhours-wake-07"}}})
	wantOffHoursConflictBody(t, resp, "Scheduled actions named harbormaster-offhours-\u2026 belong to the schedule.")
	resp = admin.post(changesetPath(), map[string]any{
		"base_version": "v", "service": map[string]any{"desired_count": 5}})
	wantOffHoursConflictBody(t, resp, "It is asleep: wake it first.")
}
