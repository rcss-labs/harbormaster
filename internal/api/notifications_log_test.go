package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify/fakesink"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type deliveryList struct {
	Items []NotificationDeliveryDTO `json:"items"`
	Next  *string                   `json:"next"`
}

// seedDelivery records a finished row in the log (a test-kind row: no incident needed).
func (e *notifyEnv) seedDelivery(route *store.NotificationRoute, status string, age time.Duration, note string) store.NotificationDelivery {
	e.t.Helper()
	d := store.NotificationDelivery{
		RouteName: "route", Destination: "webhook", Target: "example.com", Severity: "warning", AlertTitle: "Test message",
		Key: nxKey, Kind: "service_failing", Status: status, Note: note, Attempts: 1, CreatedAt: time.Now().Add(-age),
	}
	if route != nil {
		id := route.ID
		d.RouteID, d.RouteName = &id, route.Name
	}
	row, err := e.st.InsertNotificationTest(e.t.Context(), d)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func (e *notifyEnv) deliveries(query string) deliveryList {
	e.t.Helper()
	var out deliveryList
	decode(e.t, e.admin.get("/api/notifications/deliveries"+query), &out)
	return out
}

func TestNotificationDeliveries_ListFilterAndPage(t *testing.T) {
	e := newNotifyEnv(t)
	route, err := e.st.CreateNotificationRoute(t.Context(), store.NotificationRoute{Name: "route", Destination: "email", MinSeverity: "warning", Kinds: []string{"service_failing"}, QuietTimezone: "UTC"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 7; i++ {
		status := "delivered"
		if i%3 == 0 {
			status = "failed"
		}
		ids = append(ids, e.seedDelivery(&route, status, time.Duration(10-i)*time.Minute, "n"+strconv.Itoa(i)).ID)
	}

	failed := e.deliveries("?status=failed")
	if len(failed.Items) != 3 {
		t.Fatalf("failed rows = %d, want 3", len(failed.Items))
	}
	for _, d := range failed.Items {
		if d.Status != "failed" || !d.CanRetry || d.ServiceID != nxKey.ID() || d.Service != "payments-api" || d.Cluster != "core-services" {
			t.Errorf("failed row = %+v", d)
		}
	}

	// Page through everything two at a time: no gaps, no repeats, newest first.
	var seen []int64
	query := "?limit=2"
	for pages := 0; pages < 10; pages++ {
		page := e.deliveries(query)
		for _, d := range page.Items {
			seen = append(seen, d.ID)
		}
		if page.Next == nil {
			break
		}
		query = "?limit=2&cursor=" + *page.Next
	}
	if len(seen) != 7 {
		t.Fatalf("paged %d rows, want 7: %v", len(seen), seen)
	}
	for i, id := range seen {
		if want := ids[len(ids)-1-i]; id != want {
			t.Fatalf("page order = %v, want newest first %v", seen, ids)
		}
	}
	if resp := e.admin.get("/api/notifications/deliveries?cursor=garbage"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a malformed cursor = %d, want 422", resp.StatusCode)
	}
	if resp := e.admin.get("/api/notifications/deliveries?status=bogus"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown status filter = %d, want 422", resp.StatusCode)
	}
}

func TestNotificationDeliveries_RetryResetsAFailedRow(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("hook"))
	stored, _ := e.st.GetNotificationRoute(t.Context(), route.ID)
	failed := e.seedDelivery(&stored, "failed", time.Minute, "HTTP 503 after 3 attempts")
	delivered := e.seedDelivery(&stored, "delivered", time.Minute, "Delivered in 5 ms")
	orphan := e.seedDelivery(nil, "failed", time.Minute, "HTTP 503")
	kicksBefore := e.kicks.Load()

	var retried NotificationDeliveryDTO
	decode(t, e.admin.post("/api/notifications/deliveries/"+strconv.FormatInt(failed.ID, 10)+"/retry", nil), &retried)
	if retried.Status != "pending" || retried.Attempts != 0 || retried.CanRetry {
		t.Fatalf("retried = %+v, want pending", retried)
	}
	if e.kicks.Load() != kicksBefore+1 {
		t.Error("a retry must wake the dispatcher")
	}
	rows, _ := e.st.ListAudit(t.Context(), 20, 0)
	found := false
	for _, row := range rows {
		found = found || row.Action == "notification_delivery_retry"
	}
	if !found {
		t.Error("the retry was not audited")
	}

	for name, tc := range map[string]struct {
		id      int64
		status  int
		message string
	}{
		"a delivered row":        {delivered.ID, http.StatusConflict, "Only failed deliveries can be retried."},
		"a row already retrying": {failed.ID, http.StatusConflict, "Only failed deliveries can be retried."},
		"an unknown id":          {999999, http.StatusNotFound, ""},
		"a deleted route":        {orphan.ID, http.StatusConflict, "The route was deleted."},
	} {
		resp := e.admin.post("/api/notifications/deliveries/"+strconv.FormatInt(tc.id, 10)+"/retry", nil)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.status)
			continue
		}
		if tc.message != "" {
			if _, msg := errorCode(t, resp); msg != tc.message {
				t.Errorf("%s: message %q", name, msg)
			}
		}
	}
}

func TestNotificationSummary_Counts(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("hook"))
	stored, _ := e.st.GetNotificationRoute(t.Context(), route.ID)
	for i := 0; i < 3; i++ {
		d := store.NotificationDelivery{
			RouteID: &stored.ID, RouteName: "hook", Destination: "webhook", Target: "x", Severity: "warning", AlertTitle: "t", Key: nxKey,
			Event: "opened", Status: "failed", CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
		// InsertNotificationTest forces the event to "test"; use the planner for real opened rows.
		incident := e.openIncident(nxKey.Service + strconv.Itoa(i))
		d.IncidentID, d.Seq = &incident, i
		if ok, err := e.st.PlanNotificationDelivery(t.Context(), d); err != nil || !ok {
			t.Fatalf("plan: %v %v", ok, err)
		}
	}
	e.seedDelivery(nil, "failed", 48*time.Hour, "old")
	var got NotificationSummaryDTO
	decode(t, e.admin.get("/api/notifications/summary"), &got)
	if got.Failed24h != 3 || got.FailingRoutes != 1 {
		t.Fatalf("summary = %+v, want 3 failures and 1 failing route", got)
	}
	var list NotificationRoutesDTO
	decode(t, e.admin.get("/api/notifications/routes"), &list)
	if !list.Items[0].Failing {
		t.Error("the route list must flag the failing route")
	}
}

// openIncident makes one open incident for a service and returns its id.
func (e *notifyEnv) openIncident(service string) int64 {
	e.t.Helper()
	var id int64
	err := e.st.Pool.QueryRow(e.t.Context(), `insert into notification_incidents (account_id, region, cluster_name, service_name, kind, severity, title, text, opened_at)
		values ($1, 'us-east-1', 'core-services', $2, 'service_failing', 'danger', 't', 't', now()) returning id`, homelabAccountID, service).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestNotificationDeliveries_NeverCarrySecrets(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("leaky", func(in map[string]any) {
		in["url"] = e.fake.URL + "/fake-notify/webhook/fail?token=DELIVERYTOKEN"
	}))
	resp := e.admin.post("/api/notifications/routes/"+strconv.FormatInt(route.ID, 10)+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test = %d", resp.StatusCode)
	}
	resp.Body.Close()
	for _, path := range []string{"/api/notifications/deliveries", "/api/notifications/deliveries?status=failed", "/api/notifications/summary", "/api/notifications/routes"} {
		body := readBody(t, e.admin.get(path))
		if strings.Contains(body, "DELIVERYTOKEN") || strings.Contains(body, "webhook/fail") {
			t.Errorf("%s leaks the destination:\n%s", path, body)
		}
	}
	if strings.Contains(e.logs.String(), "DELIVERYTOKEN") {
		t.Error("the server log leaks the token")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func TestNotifications_LogRoutesAreAdminOnly(t *testing.T) {
	e := newNotifyEnv(t)
	operator := login(t, e.srv, "omer", testPassword)
	anonymous := newAPIClient(t, e.srv)
	for _, call := range []struct{ method, path string }{
		{"GET", "/api/notifications/deliveries"},
		{"POST", "/api/notifications/deliveries/1/retry"},
		{"GET", "/api/notifications/summary"},
	} {
		if resp := operator.do(call.method, call.path, nil, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d, want 403", call.method, call.path, resp.StatusCode)
		}
		if resp := anonymous.do(call.method, call.path, nil, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", call.method, call.path, resp.StatusCode)
		}
	}
}

// The fake Telegram receiver's path carries the bot token, like the real Bot API URL. The request log must not.
func TestServer_RequestLogRedactsTelegramTokenInFakeMode(t *testing.T) {
	const token = "123456789:SECRETBOTTOKENSECRETBOTTOKENSECRET"
	srv, _, _, logs := newServerCustom(t, true, func(s *Server) { s.FakeNotify = fakesink.New().Handler() })
	resp, err := http.Post(srv.URL+"/fake-notify/telegram/bot"+token+"/sendMessage", "application/json", strings.NewReader(`{"chat_id":"-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "SECRETBOTTOKEN") {
		t.Fatalf("the request log contains the bot token:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "/fake-notify/telegram/bot***/sendMessage") {
		t.Errorf("the request log lacks the redacted path:\n%s", logs.String())
	}
}

func TestServer_FakeNotifyUnroutedByDefault(t *testing.T) {
	// The single-page app answers unknown paths with its shell, so "unrouted" means the receiver's own answers
	// (a JSON list of what it received) are absent.
	received := func(srv *httptest.Server) (status int, contentType string) {
		resp, err := http.Post(srv.URL+"/fake-notify/slack/x", "application/json", strings.NewReader(`{"hello":"world"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		resp, err = http.Get(srv.URL + "/fake-notify/received")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Content-Type")
	}
	plain, _, _ := newTestServer(t)
	if _, contentType := received(plain); strings.HasPrefix(contentType, "application/json") {
		t.Errorf("/fake-notify/ answers without the receiver (content type %q), want it unrouted", contentType)
	}
	withSink, _, _, _ := newServerCustom(t, true, func(s *Server) { s.FakeNotify = fakesink.New().Handler() })
	if status, contentType := received(withSink); status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("/fake-notify/received with the receiver = %d %q, want a JSON list", status, contentType)
	}
}

func TestNotificationDeliveries_AggregatedRowHasScopeAndClusterID(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("hook"))
	stored, _ := e.st.GetNotificationRoute(t.Context(), route.ID)
	incident := e.openIncident("payments-api")
	clusterKey := model.ServiceKey{ClusterKey: nxKey.ClusterKey}
	due := time.Now()
	group := store.NotificationDelivery{
		RouteID: &stored.ID, RouteName: "hook", Destination: "webhook", Target: "x", IncidentID: &incident, Event: "update", Seq: 1,
		Severity: "warning", AlertTitle: "core-services: 2 of 3 services failing", AlertText: "t", Key: clusterKey, Kind: "cluster_failing",
		Status: "pending", NextAttemptAt: &due, CreatedAt: due,
		Details: store.DeliveryDetails{Scope: "cluster", Threshold: 2, Failing: 2, Total: 3},
	}
	if ok, err := e.st.PlanNotificationDelivery(t.Context(), group); err != nil || !ok {
		t.Fatalf("plan: %v %v", ok, err)
	}
	secondIncident := e.openIncident("auth")
	alertRow := store.NotificationDelivery{
		RouteID: &stored.ID, RouteName: "hook", Destination: "webhook", Target: "x", IncidentID: &secondIncident, Event: "opened",
		Severity: "warning", AlertTitle: "payments-api: failing", AlertText: "t", Key: nxKey, Kind: "service_failing",
		Status: "delivered", NextAttemptAt: &due, CreatedAt: due,
	}
	if ok, err := e.st.PlanNotificationDelivery(t.Context(), alertRow); err != nil || !ok {
		t.Fatalf("plan: %v %v", ok, err)
	}
	e.seedDelivery(nil, "delivered", 2*time.Minute, "a test message")

	byID := map[int64]NotificationDeliveryDTO{}
	for _, d := range e.deliveries("").Items {
		byID[d.ID] = d
	}
	var agg, perAlert NotificationDeliveryDTO
	var testRow NotificationDeliveryDTO
	for _, d := range byID {
		switch d.Event {
		case "update":
			agg = d
		case "opened":
			perAlert = d
		case "test":
			testRow = d
		}
	}
	if testRow.Scope != "" {
		t.Errorf("a test message has scope %q, want none", testRow.Scope)
	}
	if agg.Scope != "cluster" || agg.ClusterID != nxKey.ClusterKey.ID() || agg.AccountID != homelabAccountID || agg.Region != "us-east-1" || agg.Service != "" || agg.ServiceID != "" || agg.Alert != group.AlertTitle {
		t.Errorf("aggregated row = %+v", agg)
	}
	if perAlert.Scope != "service" || perAlert.ServiceID != nxKey.ID() || perAlert.ClusterID != nxKey.ClusterKey.ID() {
		t.Errorf("per-alert row = %+v, want scope service with its ids", perAlert)
	}
}

// Retrying the opened message of an alert that has resolved since would announce something that is no longer open
// (and leave a PagerDuty incident open for good), so it is refused with a sentence that says why.
func TestNotificationDeliveries_RetryOfAResolvedAlertIsRefused(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("hook"))
	stored, _ := e.st.GetNotificationRoute(t.Context(), route.ID)
	e.seedOnlyAlert()
	open, err := e.st.ListOpenNotificationIncidents(t.Context())
	if err != nil || len(open) != 1 {
		t.Fatalf("open incidents = %+v err=%v", open, err)
	}
	routeID, incidentID := stored.ID, open[0].ID
	if _, err := e.st.PlanNotificationDelivery(t.Context(), store.NotificationDelivery{
		RouteID: &routeID, RouteName: stored.Name, Destination: "webhook", Target: "example.com", IncidentID: &incidentID, Event: "opened",
		Severity: "warning", AlertTitle: "x", Key: open[0].Key, Kind: open[0].Kind, Status: "failed", Attempts: 3, Note: "HTTP 503 after 3 attempts",
		CreatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	rows := e.deliveries("").Items
	if len(rows) != 1 || !rows[0].CanRetry {
		t.Fatalf("rows = %+v, want one failed row that can be retried while the alert is open", rows)
	}

	// The alert goes away and, after the grace, the incident resolves.
	if err := e.st.ReplaceAlerts(t.Context(), awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{time.Now().Add(-10 * time.Minute), time.Now()} {
		if _, _, err := e.st.SyncNotificationIncidents(t.Context(), at, 2*time.Minute, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	resp := e.admin.post("/api/notifications/deliveries/"+strconv.FormatInt(rows[0].ID, 10)+"/retry", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry of a resolved alert = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "That alert has resolved, so there is nothing left to send." {
		t.Errorf("message = %q", msg)
	}
	if got := e.deliveries("").Items[0]; got.Status != "failed" {
		t.Errorf("the refused retry changed the row: %+v", got)
	}
}
