package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func never(store.NotificationRoute, string) (time.Time, bool) { return time.Time{}, false }

// planFixture is one open incident of service "api" whose investigation is done. Routes are added with addRoute.
type planFixture struct {
	t        *testing.T
	s        *store.Store
	incident store.NotificationIncident
	investID int64
}

func newPlanFixture(t *testing.T, alerts ...model.Alert) (*planFixture, []store.NotificationIncident) {
	t.Helper()
	s := storetest.Open(t)
	if len(alerts) == 0 {
		alerts = []model.Alert{notifyAlert("api", "k1", model.AlertDanger)}
	}
	incs := invIncidents(t, s, alerts...)
	return &planFixture{t: t, s: s, incident: incs[0]}, incs
}

// addRoute creates an enabled route with the switch on and an Opened row for inc in the given status.
func (f *planFixture) addRoute(name, openedStatus string, inc store.NotificationIncident) store.NotificationRoute {
	f.t.Helper()
	route := invRoute(f.t, f.s, name, true, true)
	invDelivery(f.t, f.s, route, inc, "opened", openedStatus, notifyT0)
	return route
}

// finish records the requests and completes the investigation of the subject with a summary.
func (f *planFixture) finish() {
	f.t.Helper()
	invRecord(f.t, f.s, invNow)
	inv := claimed(f.t, f.s, invNow)
	f.investID = inv.ID
	res := &store.InvestigationResult{Summary: "The api ran out of memory.", Evidence: []store.InvestigationEvidence{viewEvidence("oom")}}
	if err := f.s.FinishInvestigation(context.Background(), inv.Claim(), "done", "", res, invNow); err != nil {
		f.t.Fatal(err)
	}
}

func (f *planFixture) plan(held func(store.NotificationRoute, string) (time.Time, bool)) int {
	f.t.Helper()
	n, err := f.s.PlanDiagnosisDeliveries(context.Background(), invNow, held, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *planFixture) diagnosisRows() int {
	return invCount(f.t, f.s, `notification_deliveries where event = 'diagnosis'`)
}

func (f *planFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.s.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *planFixture) hasDiagnosis(route store.NotificationRoute, inc store.NotificationIncident) bool {
	f.t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), `select count(*) from notification_deliveries
		where route_id = $1 and incident_id = $2 and event = 'diagnosis'`, route.ID, inc.ID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n == 1
}

func TestPlanDiagnosis_OnlyAfterOpenedDelivered(t *testing.T) {
	f, _ := newPlanFixture(t)
	routes := map[string]store.NotificationRoute{}
	for _, status := range []string{"pending", "held", "failed", "cancelled", "delivered"} {
		routes[status] = f.addRoute("r-"+status, status, f.incident)
	}
	f.finish()
	if n := f.plan(never); n != 1 {
		t.Fatalf("planned %d, want 1 (only the route whose Opened was delivered)", n)
	}
	for status, route := range routes {
		if got, want := f.hasDiagnosis(route, f.incident), status == "delivered"; got != want {
			t.Errorf("route with %s Opened: diagnosis row = %v, want %v", status, got, want)
		}
	}
}

func TestPlanDiagnosis_RowCarriesTheOpenedSnapshotAndTheInvestigation(t *testing.T) {
	f, _ := newPlanFixture(t)
	route := f.addRoute("r", "delivered", f.incident)
	f.finish()
	f.plan(never)
	rows, _, err := f.s.ListNotificationDeliveries(context.Background(), store.DeliveryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got *store.NotificationDelivery
	for i := range rows {
		if rows[i].Event == "diagnosis" {
			got = &rows[i]
		}
	}
	if got == nil || got.Details.InvestigationID != f.investID || got.Severity != "critical" || got.AlertTitle != f.incident.Title ||
		got.Key != f.incident.Key || got.Kind != f.incident.Kind || *got.RouteID != route.ID || got.Status != "pending" {
		t.Fatalf("diagnosis row = %+v, want the Opened snapshot and investigation %d", got, f.investID)
	}
	st := readDiagnosis(t, f.s, got.ID)
	if st.Due == nil || !st.Due.Equal(invNow) || st.Next != nil || st.HeldUntil != nil {
		t.Fatalf("diagnosis timing = %+v, want due now, no next attempt, not held", st)
	}
}

func TestPlanDiagnosis_EveryRouteThatDeliveredOpened(t *testing.T) {
	f, _ := newPlanFixture(t)
	a := f.addRoute("a", "delivered", f.incident)
	b := f.addRoute("b", "delivered", f.incident)
	f.finish()
	if n := f.plan(never); n != 2 || !f.hasDiagnosis(a, f.incident) || !f.hasDiagnosis(b, f.incident) {
		t.Fatalf("planned %d, want one row for each of the two routes", n)
	}
}

func TestPlanDiagnosis_SecondIncidentInTheWindowGetsItsOwnRow(t *testing.T) {
	f, incs := newPlanFixture(t, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertDanger))
	route := invRoute(t, f.s, "r", true, true)
	invDelivery(t, f.s, route, incs[0], "opened", "delivered", notifyT0)
	invDelivery(t, f.s, route, incs[1], "opened", "pending", notifyT0)
	f.finish()
	if n := f.plan(never); n != 1 || !f.hasDiagnosis(route, incs[0]) || f.hasDiagnosis(route, incs[1]) {
		t.Fatalf("first plan = %d, want only the incident whose Opened was delivered", n)
	}
	f.exec(`update notification_deliveries set status = 'delivered' where incident_id = $1 and event = 'opened'`, incs[1].ID)
	if n := f.plan(never); n != 1 || !f.hasDiagnosis(route, incs[1]) {
		t.Fatalf("second plan = %d, want the second incident's own row", n)
	}
	if invCount(t, f.s, "agent_investigations") != 1 {
		t.Fatal("a second investigation was created")
	}
}

func TestPlanDiagnosis_SkipsPagerDuty(t *testing.T) {
	f, _ := newPlanFixture(t)
	pd := f.addRoute("pd", "delivered", f.incident)
	f.exec(`update notification_routes set destination = 'pagerduty' where id = $1`, pd.ID)
	f.finish()
	if n := f.plan(never); n != 0 || f.diagnosisRows() != 0 {
		t.Fatalf("planned %d for a PagerDuty route, want none", n)
	}
}

func TestPlanDiagnosis_SkipsSwitchOffDisabledAndDeletedRoutes(t *testing.T) {
	f, _ := newPlanFixture(t)
	off := f.addRoute("off", "delivered", f.incident)
	disabled := f.addRoute("disabled", "delivered", f.incident)
	deleted := f.addRoute("deleted", "delivered", f.incident)
	on := f.addRoute("on", "delivered", f.incident)
	f.finish()
	f.exec(`update notification_routes set investigate = false where id = $1`, off.ID)
	f.exec(`update notification_routes set enabled = false where id = $1`, disabled.ID)
	f.exec(`delete from notification_routes where id = $1`, deleted.ID)
	if n := f.plan(never); n != 1 || !f.hasDiagnosis(on, f.incident) || f.diagnosisRows() != 1 {
		t.Fatalf("planned %d, want only the enabled route with the switch on", n)
	}
}

func TestPlanDiagnosis_SkipsResolvedIncidents(t *testing.T) {
	f, _ := newPlanFixture(t)
	f.addRoute("r", "delivered", f.incident)
	f.finish()
	f.exec(`update notification_incidents set resolved_at = $1 where id = $2`, invNow, f.incident.ID)
	if n := f.plan(never); n != 0 {
		t.Fatalf("planned %d for a resolved incident, want none", n)
	}
}

func TestPlanDiagnosis_SkipsFailedSkippedAndRunningInvestigations(t *testing.T) {
	alerts := []model.Alert{notifyAlert("a", "k", model.AlertDanger), notifyAlert("b", "k", model.AlertDanger),
		notifyAlert("c", "k", model.AlertDanger), notifyAlert("d", "k", model.AlertDanger), notifyAlert("e", "k", model.AlertDanger)}
	f, incs := newPlanFixture(t, alerts...)
	route := invRoute(t, f.s, "r", true, true)
	for _, inc := range incs {
		invDelivery(t, f.s, route, inc, "opened", "delivered", notifyT0)
	}
	invRecord(t, f.s, invNow)
	states := map[string]string{"a": "failed", "b": "skipped", "c": "running", "d": "requested", "e": "done"}
	for service, state := range states {
		f.exec(`update agent_investigations set state = $1, summary = 'a summary' where service_name = $2`, state, service)
	}
	if n := f.plan(never); n != 1 {
		t.Fatalf("planned %d, want 1 (the done investigation only)", n)
	}
	for _, inc := range incs {
		if got, want := f.hasDiagnosis(route, inc), inc.Key.Service == "e"; got != want {
			t.Errorf("service %s: diagnosis row = %v, want %v", inc.Key.Service, got, want)
		}
	}
}

func TestPlanDiagnosis_SkipsADoneInvestigationWithNoSummary(t *testing.T) {
	f, _ := newPlanFixture(t)
	f.addRoute("r", "delivered", f.incident)
	invRecord(t, f.s, invNow)
	f.exec(`update agent_investigations set state = 'done', summary = ''`)
	if n := f.plan(never); n != 0 {
		t.Fatalf("planned %d for an empty summary, want none", n)
	}
}

func TestPlanDiagnosis_QuietHoursSetTheDueTime(t *testing.T) {
	f, _ := newPlanFixture(t)
	f.addRoute("r", "delivered", f.incident)
	f.finish()
	until := invNow.Add(6 * time.Hour)
	var seen string
	n := f.plan(func(_ store.NotificationRoute, severity string) (time.Time, bool) {
		seen = severity
		return until, true
	})
	if n != 1 || seen != "critical" {
		t.Fatalf("planned %d, severity asked about = %q, want 1 and the Opened row's severity", n, seen)
	}
	var id int64
	if err := f.s.Pool.QueryRow(context.Background(), `select id from notification_deliveries where event = 'diagnosis'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	st := readDiagnosis(t, f.s, id)
	if st.Status != "pending" || st.Due == nil || !st.Due.Equal(until) || st.Next != nil {
		t.Fatalf("held diagnosis = %+v, want pending and due at the end of quiet hours", st)
	}
}

func TestPlanDiagnosis_CriticalIsNotHeld(t *testing.T) {
	f, incs := newPlanFixture(t, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertWarning))
	route := invRoute(t, f.s, "r", true, true)
	for _, inc := range incs {
		invDelivery(t, f.s, route, inc, "opened", "delivered", notifyT0)
	}
	f.exec(`update notification_deliveries set severity = 'warning' where incident_id = $1`, incs[1].ID)
	f.finish()
	until := invNow.Add(6 * time.Hour)
	f.plan(func(_ store.NotificationRoute, severity string) (time.Time, bool) {
		return until, severity == "warning"
	})
	due := map[int64]time.Time{}
	rows, err := f.s.Pool.Query(context.Background(), `select incident_id, diagnosis_due_at from notification_deliveries where event = 'diagnosis'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			t.Fatal(err)
		}
		due[id] = at
	}
	if !due[incs[0].ID].Equal(invNow) || !due[incs[1].ID].Equal(until) {
		t.Fatalf("due times = %v, want the critical row now and the warning row at %v", due, until)
	}
}

func TestPlanDiagnosis_IdempotentAcrossCyclesAndHolders(t *testing.T) {
	f, _ := newPlanFixture(t)
	f.addRoute("a", "delivered", f.incident)
	f.addRoute("b", "delivered", f.incident)
	f.finish()
	var total atomic.Int64
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := f.s.PlanDiagnosisDeliveries(context.Background(), invNow, never, 100)
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		}()
	}
	wg.Wait()
	if total.Load() != 2 || f.diagnosisRows() != 2 {
		t.Fatalf("six concurrent planners planned %d rows (table has %d), want 2 in all", total.Load(), f.diagnosisRows())
	}
	if n := f.plan(never); n != 0 {
		t.Fatalf("a later cycle planned %d, want 0", n)
	}
}

func TestPlanDiagnosis_LimitBoundsOneCall(t *testing.T) {
	f, _ := newPlanFixture(t)
	f.addRoute("a", "delivered", f.incident)
	f.addRoute("b", "delivered", f.incident)
	f.finish()
	n, err := f.s.PlanDiagnosisDeliveries(context.Background(), invNow, never, 1)
	if err != nil || n != 1 {
		t.Fatalf("limit 1 planned %d, %v, want 1", n, err)
	}
	if n := f.plan(never); n != 1 {
		t.Fatalf("the next call planned %d, want the remaining 1", n)
	}
}
