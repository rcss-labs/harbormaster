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

// A desired-count change on a service that is asleep on an off-hours schedule answers 409 with the schedule's message.
func TestDesiredCount_OffHoursConflictIs409(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		s.Deploy.Guard = &offhours.Guard{Store: s.Store, Now: time.Now}
	})
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)
	user, err := st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := st.CreateOffHoursSchedule(t.Context(), store.OffHoursSchedule{
		Name: "never awake", Enabled: true, TagKey: "env", TagValue: "prod", Timezone: "UTC", Awake: strings.Repeat("0", 168)}, user.ID)
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
	resp := admin.post("/api/services/"+paymentsID()+"/desired-count", map[string]any{"desired": 5})
	wantStatus(t, resp, http.StatusConflict)
	if code, msg := errorCode(t, resp); code != "conflict" || msg != `payments-api is on the off-hours schedule "never awake". It is asleep: wake it first.` {
		t.Errorf("error = %s %q", code, msg)
	}
}
