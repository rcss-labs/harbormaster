package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// offHoursEnv is a test server over the mockup estate with the off-hours applier wired to the fake AWS and a fixed clock.
type offHoursEnv struct {
	srv   *httptest.Server
	fk    *fake.Fake
	st    *store.Store
	kicks *atomic.Int32
	now   time.Time
}

var stagingWebKey = model.ServiceKey{
	ClusterKey: model.ClusterKey{AccountID: stagingAccountID, Region: "us-east-1", Cluster: "staging-web"}, Service: "web"}

// berlinTime returns a wall-clock time in Europe/Berlin.
func berlinTime(t *testing.T, year int, month time.Month, day, hour int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(year, month, day, hour, 0, 0, 0, loc)
}

// saturdayNight is asleep for a weekday-hours grid; mondayNoon is awake.
func saturdayNight(t *testing.T) time.Time { return berlinTime(t, 2026, time.November, 7, 23) }
func mondayNoon(t *testing.T) time.Time    { return berlinTime(t, 2026, time.November, 9, 12) }

func newOffHoursEnv(t *testing.T, now time.Time) *offHoursEnv {
	t.Helper()
	env := &offHoursEnv{kicks: &atomic.Int32{}, now: now}
	clock := func() time.Time { return now }
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		s.Now = clock
		s.OffHours = &offhours.Applier{Store: s.Store, Clients: s.Clients, Now: clock}
		s.KickOffHours = func() { env.kicks.Add(1) }
	})
	env.srv, env.fk, env.st = srv, fk, st
	seedMockupTags(t, st)
	for _, user := range []string{"dana", "oncall"} {
		setPassword(t, st, user)
	}
	pollAll(t, st, fk)
	return env
}

func (e *offHoursEnv) admin(t *testing.T) *apiClient { return login(t, e.srv, "sam", testPassword) }

// tagService adds an AWS tag to a polled service, as a Terraform apply would.
func (e *offHoursEnv) tagService(t *testing.T, key model.ServiceKey, tagKey, tagValue string) {
	t.Helper()
	_, err := e.st.Pool.Exec(context.Background(),
		`update services set raw = jsonb_set(raw, '{Tags}', coalesce(raw -> 'Tags', '[]'::jsonb) || jsonb_build_array(jsonb_build_object('Key', $5::text, 'Value', $6::text)))
		 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		key.AccountID, key.Region, key.Cluster, key.Service, tagKey, tagValue)
	if err != nil {
		t.Fatal(err)
	}
}

// weekdayGrid is awake Monday to Friday 07:00-20:00 (65 hours).
func weekdayGrid() [7][24]bool {
	var grid [7][24]bool
	for day := 0; day < 5; day++ {
		for hour := 7; hour < 20; hour++ {
			grid[day][hour] = true
		}
	}
	return grid
}

func scheduleBody(name, tagKey, tagValue string) map[string]any {
	return map[string]any{"name": name, "enabled": true, "tag_key": tagKey, "tag_value": tagValue,
		"timezone": "Europe/Berlin", "awake": weekdayGrid()}
}

type scheduleResp struct {
	ID                int64         `json:"id"`
	Name              string        `json:"name"`
	Version           int           `json:"version"`
	MonthlySaving     *float64      `json:"monthly_saving"`
	UnpricedServices  int           `json:"unpriced_services"`
	AwakeHoursPerWeek int           `json:"awake_hours_per_week"`
	ServiceCount      int           `json:"service_count"`
	AsleepCount       int           `json:"asleep_count"`
	Services          []serviceResp `json:"services"`
}

type serviceResp struct {
	ServiceID           string   `json:"service_id"`
	Service             string   `json:"service"`
	Cluster             string   `json:"cluster"`
	State               string   `json:"state"`
	Reason              string   `json:"reason"`
	MonthlyAlwaysOn     *float64 `json:"monthly_always_on"`
	LooksLikeProduction []string `json:"looks_like_production"`
	CanOperate          bool     `json:"can_operate"`
}

func (e *offHoursEnv) createSchedule(t *testing.T, c *apiClient, body map[string]any) scheduleResp {
	t.Helper()
	resp := c.post("/api/offhours/schedules", body)
	if resp.StatusCode != http.StatusCreated {
		code, msg := errorCode(t, resp)
		t.Fatalf("create schedule: %d %s %s", resp.StatusCode, code, msg)
	}
	var out scheduleResp
	decode(t, resp, &out)
	return out
}

func TestOffHoursSchedules_AdminOnly(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	dana := login(t, env.srv, "dana", testPassword)
	calls := []struct{ method, path string }{
		{http.MethodGet, "/api/offhours/schedules"},
		{http.MethodGet, "/api/offhours/schedules/1"},
		{http.MethodPost, "/api/offhours/schedules"},
		{http.MethodPut, "/api/offhours/schedules/1"},
		{http.MethodDelete, "/api/offhours/schedules/1"},
		{http.MethodPost, "/api/offhours/preview"},
		{http.MethodGet, "/api/offhours/tags"},
	}
	for _, call := range calls {
		resp := dana.do(call.method, call.path, scheduleBody("x", "env", "staging"), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", call.method, call.path, resp.StatusCode)
		}
	}
}

func TestOffHoursSchedules_CreateValidatesAndAudits(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)

	empty := scheduleBody("staging nights", "env", "staging")
	empty["awake"] = [7][24]bool{}
	resp := c.post("/api/offhours/schedules", empty)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty grid = %d, want 422", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "invalid" {
		t.Errorf("code = %q, want invalid", code)
	}
	if env.kicks.Load() != 0 {
		t.Errorf("kicks after a rejected create = %d, want 0", env.kicks.Load())
	}

	created := env.createSchedule(t, c, scheduleBody("staging nights", "env", "staging"))
	if created.Version != 1 || created.AwakeHoursPerWeek != 65 {
		t.Errorf("created = %+v, want version 1 and 65 awake hours", created)
	}
	if env.kicks.Load() != 1 {
		t.Errorf("kicks = %d, want 1", env.kicks.Load())
	}
	entries, err := env.st.ListAudit(t.Context(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var audits int
	for _, entry := range entries {
		if entry.Action == "offhours_schedule_create" {
			audits++
			if entry.Target != "offhours:"+itoa(created.ID) || entry.ActorUsername != "sam" {
				t.Errorf("audit = %+v", entry)
			}
		}
	}
	if audits != 1 {
		t.Errorf("create audit rows = %d, want 1", audits)
	}
}

func TestOffHoursSchedules_OverlapListsServices(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	env.tagService(t, stagingWebKey, "team", "web")
	env.createSchedule(t, c, scheduleBody("staging nights", "env", "staging"))

	resp := c.post("/api/offhours/schedules", scheduleBody("web team", "team", "web"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("overlap = %d, want 409", resp.StatusCode)
	}
	code, msg := errorCode(t, resp)
	if code != "conflict" || !strings.Contains(msg, `Schedule "staging nights" already matches 1 of these services: staging-web/web`) {
		t.Errorf("overlap = %s %q", code, msg)
	}

	resp = c.post("/api/offhours/schedules", scheduleBody("again", "env", "staging"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("identical selector = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != `A schedule for env=staging already exists: "staging nights".` {
		t.Errorf("identical selector message = %q", msg)
	}
}

func TestOffHoursSchedules_StaleVersionConflicts(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	created := env.createSchedule(t, c, scheduleBody("staging nights", "env", "staging"))
	path := "/api/offhours/schedules/" + itoa(created.ID)

	update := scheduleBody("staging nights", "env", "staging")
	update["enabled"] = false
	update["version"] = created.Version
	resp := c.do(http.MethodPut, path, update, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d, want 200", resp.StatusCode)
	}
	var updated scheduleResp
	decode(t, resp, &updated)
	if updated.Version != 2 {
		t.Errorf("version = %d, want 2", updated.Version)
	}

	resp = c.do(http.MethodPut, path, update, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale update = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "This schedule changed since you opened it. Reload to see the latest." {
		t.Errorf("stale message = %q", msg)
	}

	resp = c.do(http.MethodDelete, path, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", resp.StatusCode)
	}
}

func TestOffHoursPreview_WarnsOnProduction(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	body := scheduleBody("prod nights", "env", "prod")
	resp := c.post("/api/offhours/preview", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d", resp.StatusCode)
	}
	var preview struct {
		Services []serviceResp `json:"services"`
	}
	decode(t, resp, &preview)
	if len(preview.Services) != 4 {
		t.Fatalf("services = %d, want 4", len(preview.Services))
	}
	for _, svc := range preview.Services {
		if len(svc.LooksLikeProduction) == 0 {
			t.Errorf("%s has no production reasons", svc.Service)
		}
	}
	env.createSchedule(t, c, body) // a warning never blocks the save
}

func TestOffHoursPreview_SleepNowCount(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	var preview struct {
		Services          []serviceResp `json:"services"`
		SleepNow          int           `json:"sleep_now"`
		AwakeHoursPerWeek int           `json:"awake_hours_per_week"`
	}
	decode(t, c.post("/api/offhours/preview", scheduleBody("staging nights", "env", "staging")), &preview)
	if len(preview.Services) < 2 || preview.SleepNow != len(preview.Services) || preview.AwakeHoursPerWeek != 65 {
		t.Errorf("preview = %+v, want every staging service asleep on Saturday night", preview)
	}

	env = newOffHoursEnv(t, mondayNoon(t))
	decode(t, env.admin(t).post("/api/offhours/preview", scheduleBody("staging nights", "env", "staging")), &preview)
	if preview.SleepNow != 0 {
		t.Errorf("sleep_now on Monday noon = %d, want 0", preview.SleepNow)
	}
}

func TestOffHoursList_SavingUsesCostPackage(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	env.tagService(t, stagingWebKey, "team", "web")
	env.createSchedule(t, c, scheduleBody("web team", "team", "web"))

	var list struct {
		Items              []scheduleResp `json:"items"`
		MonthlySavingTotal *float64       `json:"monthly_saving_total"`
		PricesAsOf         string         `json:"prices_as_of"`
	}
	decode(t, c.get("/api/offhours/schedules"), &list)
	rate, ok := cost.RateFor("us-east-1", "")
	if !ok {
		t.Fatal("no us-east-1 rate")
	}
	want := rate.Monthly(256, 512) * 103 / 168
	if len(list.Items) != 1 || list.Items[0].MonthlySaving == nil || abs(*list.Items[0].MonthlySaving-want) > 0.01 {
		t.Fatalf("items = %+v, want a saving of %.4f", list.Items, want)
	}
	if list.MonthlySavingTotal == nil || abs(*list.MonthlySavingTotal-want) > 0.01 || list.PricesAsOf != cost.PricesAsOf {
		t.Errorf("total = %v as of %q, want %.4f as of %s", list.MonthlySavingTotal, list.PricesAsOf, want, cost.PricesAsOf)
	}
	if list.Items[0].ServiceCount != 1 || list.Items[0].Services == nil || len(list.Items[0].Services) != 0 {
		t.Errorf("list item = %+v, want a count and no service rows", list.Items[0])
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestOffHoursList_EC2ServiceNotPriced(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	var certRenewer model.ServiceKey
	err := env.st.Pool.QueryRow(t.Context(),
		`select account_id, region, cluster_name, service_name from services where service_name = 'cert-renewer'`).
		Scan(&certRenewer.AccountID, &certRenewer.Region, &certRenewer.Cluster, &certRenewer.Service)
	if err != nil {
		t.Fatal(err)
	}
	env.tagService(t, certRenewer, "team", "edge")
	created := env.createSchedule(t, c, scheduleBody("edge", "team", "edge"))

	var detail scheduleResp
	decode(t, c.get("/api/offhours/schedules/"+itoa(created.ID)), &detail)
	if len(detail.Services) != 1 || detail.Services[0].MonthlyAlwaysOn != nil {
		t.Fatalf("services = %+v, want one unpriced service", detail.Services)
	}
	if detail.MonthlySaving != nil || detail.UnpricedServices != 1 {
		t.Errorf("saving = %v unpriced = %d, want null and 1", detail.MonthlySaving, detail.UnpricedServices)
	}
}

func TestOffHoursTags_ListsAWSTagValues(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	var out struct {
		Items []struct {
			Key      string `json:"key"`
			Value    string `json:"value"`
			Services int    `json:"services"`
		} `json:"items"`
	}
	decode(t, env.admin(t).get("/api/offhours/tags"), &out)
	for _, item := range out.Items {
		if item.Key == "env" && item.Value == "prod" && item.Services == 4 {
			return
		}
	}
	t.Errorf("tags = %+v, want env=prod on 4 services", out.Items)
}

// enrolStagingWeb creates an env=staging schedule and an enrolment of staging-web/web, asleep in AWS (0/0).
func (e *offHoursEnv) enrolStagingWeb(t *testing.T, state, blockedReason string) store.OffHoursSchedule {
	t.Helper()
	ctx := context.Background()
	sc, err := e.st.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{Name: "staging nights", Enabled: true,
		TagKey: "env", TagValue: "staging", Timezone: "Europe/Berlin", Awake: offhours.WeekFromRows(weekdayGrid()).String()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.fk.Estate.Mu.Lock()
	e.fk.Estate.ScalingConfig[stagingWebKey] = &fake.ScalingSeed{Min: 0, Max: 0}
	e.fk.Estate.Mu.Unlock()
	one := 1
	err = e.st.InTx(ctx, func(tx store.Tx) error {
		return e.st.UpsertOffHoursEnrolment(ctx, tx, store.OffHoursEnrolment{Key: stagingWebKey, ScheduleID: &sc.ID,
			SavedMin: 1, SavedMax: 1, SavedDesired: &one, RegisteredTarget: true, AwakeTasks: 1, State: state, BlockedReason: blockedReason})
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func serviceOffHoursPath(key model.ServiceKey, suffix string) string {
	return "/api/services/" + key.ID() + "/offhours" + suffix
}

func TestServiceOffHours_WakeNeedsOperate(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	env.enrolStagingWeb(t, "active", "")

	resp := login(t, env.srv, "omer", testPassword).post(serviceOffHoursPath(stagingWebKey, "/wake"), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("omer wake = %d, want 403", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "You need Operate on Staging" {
		t.Errorf("message = %q", msg)
	}

	resp = login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, "/wake"), nil)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("oncall wake = %d %s %s", resp.StatusCode, code, msg)
	}
	var woken struct {
		WokenUntil time.Time `json:"woken_until"`
	}
	decode(t, resp, &woken)
	if !woken.WokenUntil.After(env.now) {
		t.Errorf("woken_until = %v, want after %v", woken.WokenUntil, env.now)
	}
	entries, err := env.st.ListAudit(t.Context(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, entry := range entries {
		audited = audited || (entry.Action == "offhours_wake" && entry.Target == "service:staging-web/web" && entry.ActorUsername == "oncall")
	}
	if !audited {
		t.Errorf("no offhours_wake audit row in %+v", entries)
	}
	var operations int
	if err := env.st.Pool.QueryRow(t.Context(),
		`select count(*) from operation_log where service_name = 'web' and cluster_name = 'staging-web' and details ->> 'action' = 'wake_for'`).Scan(&operations); err != nil || operations != 1 {
		t.Errorf("wake operations = %d (%v), want 1", operations, err)
	}

	var state struct {
		Enrolled bool   `json:"enrolled"`
		State    string `json:"state"`
	}
	decode(t, login(t, env.srv, "oncall", testPassword).get(serviceOffHoursPath(stagingWebKey, "")), &state)
	if !state.Enrolled || state.State != "woken" {
		t.Errorf("state after wake = %+v, want enrolled and woken", state)
	}

	resp = login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, "/sleep"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("sleep after wake = %d, want 200", resp.StatusCode)
	}
}

func TestServiceOffHours_WakeRefusedWhenAwake(t *testing.T) {
	env := newOffHoursEnv(t, mondayNoon(t))
	env.enrolStagingWeb(t, "active", "")
	resp := login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, "/wake"), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("wake while awake = %d, want 409", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "conflict" || !strings.Contains(msg, "not asleep") {
		t.Errorf("wake while awake = %s %q", code, msg)
	}
}

func TestServiceOffHours_NotEnrolledWakeIs404(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	resp := login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, "/wake"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("wake without enrolment = %d, want 404", resp.StatusCode)
	}
}

func TestServiceOffHours_BlockedExplainsReadOnly(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	env.enrolStagingWeb(t, "blocked", "access_denied")
	const want = "Can't apply: Harbormaster's role in staging-example can't change auto scaling. The access module is probably deployed with read_only = true."
	for _, action := range []string{"/wake", "/sleep"} {
		resp := login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, action), nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s on a blocked service = %d, want 409", action, resp.StatusCode)
		}
		if _, msg := errorCode(t, resp); msg != want {
			t.Errorf("%s message = %q", action, msg)
		}
	}
	var state struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	decode(t, login(t, env.srv, "oncall", testPassword).get(serviceOffHoursPath(stagingWebKey, "")), &state)
	if state.State != "blocked" || state.Reason != want {
		t.Errorf("state = %+v, want blocked with the read-only sentence", state)
	}
}

func TestServiceOffHours_AccessDeniedFromAWSMarksBlocked(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	env.enrolStagingWeb(t, "active", "")
	env.fk.FailNext("PutScheduledAction", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized"})
	resp := login(t, env.srv, "oncall", testPassword).post(serviceOffHoursPath(stagingWebKey, "/wake"), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("wake = %d, want 409", resp.StatusCode)
	}
	enrolment, err := env.st.GetOffHoursEnrolment(t.Context(), stagingWebKey)
	if err != nil {
		t.Fatal(err)
	}
	if enrolment.State != "blocked" || enrolment.BlockedReason != "access_denied" || enrolment.WokenUntil != nil {
		t.Errorf("enrolment = %+v, want blocked by access_denied and not woken", enrolment)
	}
}

func TestServiceOffHours_InvisibleIs404(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	env.enrolStagingWeb(t, "active", "")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.st.CreateUser(t.Context(), "nobody", hash, false, false); err != nil {
		t.Fatal(err)
	}
	c := login(t, env.srv, "nobody", testPassword)
	for _, call := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodPost, "/wake"}, {http.MethodPost, "/sleep"}} {
		resp := c.do(call.method, serviceOffHoursPath(stagingWebKey, call.suffix), nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", call.method, call.suffix, resp.StatusCode)
		}
	}
}
