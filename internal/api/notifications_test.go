package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/notify/fakesink"
	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// notifyEnv is an API server whose notification senders post to an in-process fake receiver, like fake mode.
type notifyEnv struct {
	t       *testing.T
	srv     *httptest.Server
	api     *Server
	st      *store.Store
	logs    *syncBuffer
	sink    *fakesink.Sink
	fake    *httptest.Server
	kicks   *atomic.Int32
	admin   *apiClient
	hookURL string // a URL under the fake receiver that always answers 200
}

type notifyEnvOptions struct {
	noSessionKey bool
	smtp         bool
	settle       time.Duration
}

func newNotifyEnv(t *testing.T, opts ...notifyEnvOptions) *notifyEnv {
	t.Helper()
	var opt notifyEnvOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	e := &notifyEnv{t: t, sink: fakesink.New(), kicks: &atomic.Int32{}}
	e.fake = httptest.NewServer(e.sink.Handler())
	t.Cleanup(e.fake.Close)
	e.hookURL = e.fake.URL + "/fake-notify/webhook/ok"
	senders := &notify.Senders{
		BaseURL: "https://hm.example.com", Version: "test", Timeout: 3 * time.Second,
		Guard:    safedial.Policy{AllowLoopback: true},
		FakeBase: e.fake.URL + "/fake-notify", TelegramBase: e.fake.URL + "/fake-notify/telegram",
	}
	if opt.smtp {
		senders.FakeMail = e.sink.Mail
	}
	e.srv, e.api, _, e.st, e.logs = newServerCustomAPI(t, true, func(s *Server) {
		s.Notify = senders
		s.KickNotify = func() { e.kicks.Add(1) }
		if opt.noSessionKey {
			s.Cfg.SessionKey = ""
		}
		s.Cfg.Notify.Settle = opt.settle
	})
	seedAccounts(t, e.st)
	e.admin = login(t, e.srv, "sam", testPassword)
	return e
}

func (e *notifyEnv) webhookInput(name string, mutate ...func(map[string]any)) map[string]any {
	in := map[string]any{
		"name": name, "enabled": false, "destination": "webhook", "url": e.hookURL,
		"min_severity": "warning", "kinds": []string{"service_failing", "restart_loop"}, "repeat_seconds": 0,
		"quiet_enabled": false, "quiet_timezone": "Europe/Berlin", "notify_resolved": true, "scope": []any{}, "fallback": false,
	}
	for _, m := range mutate {
		m(in)
	}
	return in
}

func (e *notifyEnv) create(in map[string]any) NotificationRouteDTO {
	e.t.Helper()
	resp := e.admin.post("/api/notifications/routes", in)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(e.t, resp)
		e.t.Fatalf("create = %d %s %s", resp.StatusCode, code, msg)
	}
	var out NotificationRouteDTO
	decode(e.t, resp, &out)
	return out
}

func (e *notifyEnv) list() NotificationRoutesDTO {
	e.t.Helper()
	var out NotificationRoutesDTO
	decode(e.t, e.admin.get("/api/notifications/routes"), &out)
	return out
}

func (e *notifyEnv) put(id int64, in map[string]any) *http.Response {
	return e.admin.do(http.MethodPut, "/api/notifications/routes/"+strconv.FormatInt(id, 10), in, nil)
}

func (e *notifyEnv) invalidFor(in map[string]any, wantMessage string) {
	e.t.Helper()
	resp := e.admin.post("/api/notifications/routes", in)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		e.t.Fatalf("create = %d %s, want 422", resp.StatusCode, body)
	}
	if _, msg := errorCode(e.t, resp); !strings.Contains(msg, wantMessage) {
		e.t.Fatalf("message = %q, want it to contain %q", msg, wantMessage)
	}
}

func (e *notifyEnv) received() string {
	e.t.Helper()
	resp, err := http.Get(e.fake.URL + "/fake-notify/received")
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

var (
	nxKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	nyKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "auth"}
	nzKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "background-jobs"}, Service: "image-resizer"}
	nwKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: stagingAccountID, Region: "us-east-1", Cluster: "staging-web"}, Service: "web"}
)

func (e *notifyEnv) seedAlerts() { e.seedAlertsAged(0) }

// seedAlertsAged seeds the alerts and syncs them into incidents as if they had been open for age, which is what
// the preview reads.
func (e *notifyEnv) seedAlertsAged(age time.Duration) {
	e.t.Helper()
	at := time.Now()
	mk := func(k model.ServiceKey, kind string, sev model.AlertSeverity, title string) model.Alert {
		return model.Alert{Key: k, Kind: kind, Severity: sev, Title: title, Text: "text of " + k.Service, At: at, Tab: "overview"}
	}
	home := []model.Alert{
		mk(nxKey, "service_failing", model.AlertDanger, "payments-api: failing"),
		mk(nyKey, "service_degraded", model.AlertWarning, "auth: degraded"),
		mk(nzKey, "restart_loop", model.AlertWarning, "image-resizer: restart loop"),
		mk(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1"}}, "account_unreachable", model.AlertDanger, "homelab unreachable"),
		mk(nxKey, "rollout_in_progress", model.AlertInfo, "payments-api: rollout"),
	}
	staging := []model.Alert{mk(nwKey, "service_failing", model.AlertDanger, "web: failing")}
	if err := e.st.ReplaceAlerts(e.t.Context(), awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"}, home); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ReplaceAlerts(e.t.Context(), awsx.Target{AccountID: stagingAccountID, Region: "us-east-1"}, staging); err != nil {
		e.t.Fatal(err)
	}
	e.syncIncidents(age)
}

func (e *notifyEnv) syncIncidents(age time.Duration) {
	e.t.Helper()
	if _, _, err := e.st.SyncNotificationIncidents(e.t.Context(), time.Now().Add(-age), 2*time.Minute, time.Hour); err != nil {
		e.t.Fatal(err)
	}
}

// seedOnlyAlert leaves exactly one open alert: payments-api failing in core-services (a restart loop, so no settle).
func (e *notifyEnv) seedOnlyAlert() {
	e.t.Helper()
	alert := model.Alert{Key: nxKey, Kind: "restart_loop", Severity: model.AlertWarning, Title: "payments-api: restart loop", Text: "t", At: time.Now(), Tab: "overview"}
	if err := e.st.ReplaceAlerts(e.t.Context(), awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"}, []model.Alert{alert}); err != nil {
		e.t.Fatal(err)
	}
	e.syncIncidents(0)
}

// seedActiveServices puts the cluster's services in the snapshot, so a group message can say "of M".
func (e *notifyEnv) seedActiveServices(keys ...model.ServiceKey) {
	e.t.Helper()
	var rows []model.Service
	for _, k := range keys {
		rows = append(rows, model.Service{Key: k, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(e.st).UpsertServices(e.t.Context(), rows); err != nil {
		e.t.Fatal(err)
	}
}

var nExtraKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "ledger"}

func (e *notifyEnv) preview(body map[string]any) NotificationPreviewDTO {
	e.t.Helper()
	resp := e.admin.post("/api/notifications/preview", body)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(e.t, resp)
		e.t.Fatalf("preview = %d %s %s", resp.StatusCode, code, msg)
	}
	var out NotificationPreviewDTO
	decode(e.t, resp, &out)
	return out
}

func serviceScope(k model.ServiceKey) map[string]any {
	return map[string]any{"kind": "service", "service_id": k.ID()}
}

func clusterScope(k model.ClusterKey, threshold ...int) map[string]any {
	entry := map[string]any{"kind": "cluster", "cluster_id": k.ID()}
	if len(threshold) > 0 {
		entry["threshold"] = threshold[0]
	}
	return entry
}

func accountScope(id string, threshold ...int) map[string]any {
	entry := map[string]any{"kind": "account", "account_id": id}
	if len(threshold) > 0 {
		entry["threshold"] = threshold[0]
	}
	return entry
}

func matchTitles(p NotificationPreviewDTO) string {
	var titles []string
	for _, m := range p.Matches {
		titles = append(titles, m.Title)
	}
	return strings.Join(titles, " | ")
}

// ---- access ----

func TestNotifications_AllRoutesAreAdminOnly(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("exists"))
	id := strconv.FormatInt(route.ID, 10)
	calls := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/notifications/routes", nil},
		{"POST", "/api/notifications/routes", e.webhookInput("other")},
		{"PUT", "/api/notifications/routes/" + id, e.webhookInput("exists")},
		{"DELETE", "/api/notifications/routes/" + id, nil},
		{"POST", "/api/notifications/preview", e.webhookInput("draft")},
		{"POST", "/api/notifications/routes/" + id + "/test", nil},
		{"GET", "/api/notifications/targets", nil},
	}
	operator := login(t, e.srv, "omer", testPassword)
	anonymous := newAPIClient(t, e.srv)
	for _, call := range calls {
		if resp := operator.do(call.method, call.path, call.body, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d, want 403", call.method, call.path, resp.StatusCode)
		}
		if resp := anonymous.do(call.method, call.path, call.body, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", call.method, call.path, resp.StatusCode)
		}
		if call.method != "GET" {
			if resp := e.admin.do(call.method, call.path, call.body, map[string]string{"X-CSRF-Token": ""}); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s without a CSRF token = %d, want 403", call.method, call.path, resp.StatusCode)
			}
		}
	}
	if len(e.list().Items) != 1 {
		t.Error("a refused call changed the routes")
	}
}

// ---- CRUD ----

func TestNotificationRoutes_CreateListUpdateDelete(t *testing.T) {
	e := newNotifyEnv(t)
	created := e.create(e.webhookInput("Prod hook", func(in map[string]any) { in["repeat_seconds"] = 1800 }))
	if created.ID == 0 || created.Version != 1 || created.Enabled || created.Destination != "webhook" || created.RepeatSeconds != 1800 || !created.SecretSet || created.SecretUnreadable {
		t.Fatalf("created = %+v", created)
	}
	if created.Target != "127.0.0.1" {
		t.Errorf("target = %q, want the URL's host only", created.Target)
	}
	list := e.list()
	if len(list.Items) != 1 || list.Items[0].Name != "Prod hook" || len(list.Kinds) != 7 || !list.SecretsAvailable || list.EmailAvailable {
		t.Fatalf("list = %+v", list)
	}

	update := e.webhookInput("Prod hook 2", func(in map[string]any) {
		in["enabled"] = true
		in["min_severity"] = "critical"
		in["version"] = created.Version
		delete(in, "url")
	})
	resp := e.put(created.ID, update)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("update = %d %s %s", resp.StatusCode, code, msg)
	}
	var updated NotificationRouteDTO
	decode(t, resp, &updated)
	if updated.Version != 2 || updated.Name != "Prod hook 2" || !updated.Enabled || updated.MinSeverity != "critical" || !updated.SecretSet {
		t.Fatalf("updated = %+v", updated)
	}

	resp = e.admin.do(http.MethodDelete, "/api/notifications/routes/"+strconv.FormatInt(created.ID, 10), nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if len(e.list().Items) != 0 {
		t.Error("the route survived its delete")
	}
	if resp := e.admin.do(http.MethodDelete, "/api/notifications/routes/"+strconv.FormatInt(created.ID, 10), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", resp.StatusCode)
	}
}

func TestNotificationRoutes_WritesKick(t *testing.T) {
	e := newNotifyEnv(t)
	created := e.create(e.webhookInput("one"))
	if e.kicks.Load() != 1 {
		t.Fatalf("kicks after create = %d", e.kicks.Load())
	}
	update := e.webhookInput("one")
	update["version"] = created.Version
	e.put(created.ID, update)
	e.admin.do(http.MethodDelete, "/api/notifications/routes/"+strconv.FormatInt(created.ID, 10), nil, nil)
	if e.kicks.Load() != 3 {
		t.Fatalf("kicks after create, update and delete = %d, want 3", e.kicks.Load())
	}
}

func TestNotificationRoutes_StaleVersionIs409(t *testing.T) {
	e := newNotifyEnv(t)
	created := e.create(e.webhookInput("one"))
	update := e.webhookInput("one")
	update["version"] = created.Version
	if resp := e.put(created.ID, update); resp.StatusCode != http.StatusOK {
		t.Fatalf("first update = %d", resp.StatusCode)
	}
	resp := e.put(created.ID, update) // still carries version 1
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale update = %d, want 409", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "conflict" || msg != "This route was changed by someone else. Reload and try again." {
		t.Errorf("error = %s %q", code, msg)
	}
	missing := e.webhookInput("one")
	delete(missing, "version")
	if resp := e.put(created.ID, missing); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("update without a version = %d, want 422", resp.StatusCode)
	}
}

func TestNotificationRoutes_DuplicateNameIs409(t *testing.T) {
	e := newNotifyEnv(t)
	e.create(e.webhookInput("Prod"))
	resp := e.admin.post("/api/notifications/routes", e.webhookInput("prod"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "A route with this name already exists." {
		t.Errorf("message = %q", msg)
	}
}

func TestNotificationRoutes_LimitIs409(t *testing.T) {
	e := newNotifyEnv(t)
	for i := 0; i < store.MaxNotificationRoutes; i++ {
		_, err := e.st.CreateNotificationRoute(t.Context(), store.NotificationRoute{
			Name: "route " + strconv.Itoa(i), Destination: "email", MinSeverity: "warning", Kinds: []string{"service_failing"}, QuietTimezone: "UTC",
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	resp := e.admin.post("/api/notifications/routes", e.webhookInput("one too many"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("51st route = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "You can have up to 50 routes. Delete one first." {
		t.Errorf("message = %q", msg)
	}
}

// ---- validation ----

func TestNotificationRoutes_Validation(t *testing.T) {
	e := newNotifyEnv(t)
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		message string
	}{
		{"empty name", func(in map[string]any) { in["name"] = "   " }, "Enter a route name."},
		{"long name", func(in map[string]any) { in["name"] = strings.Repeat("a", 81) }, "Enter a route name."},
		{"unknown destination", func(in map[string]any) { in["destination"] = "pager" }, "destination"},
		{"unknown severity", func(in map[string]any) { in["min_severity"] = "info" }, "severity"},
		{"no kinds", func(in map[string]any) { in["kinds"] = []string{} }, "Choose at least one alert type."},
		{"unknown kind", func(in map[string]any) { in["kinds"] = []string{"rollout_in_progress"} }, "Choose at least one alert type."},
		{"bad repeat", func(in map[string]any) { in["repeat_seconds"] = 60 }, "repeat"},
		{"bad zone", func(in map[string]any) { in["quiet_timezone"] = "Mars/Base" }, "Choose a valid time zone."},
		{"missing url", func(in map[string]any) { delete(in, "url") }, "Enter an https URL."},
		{"telegram needs a token", func(in map[string]any) {
			in["destination"], in["chat_id"] = "telegram", "-100123"
			delete(in, "url")
		}, "bot token"},
		{"pagerduty key", func(in map[string]any) {
			in["destination"], in["pagerduty_region"], in["routing_key"] = "pagerduty", "us", "short"
			delete(in, "url")
		}, "32-character"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { e.invalidFor(e.webhookInput("v", tc.mutate), tc.message) })
	}
	if len(e.list().Items) != 0 {
		t.Error("a rejected route was stored")
	}
}

func TestNotificationRoutes_RejectsNonHTTPSAndCredentialsInURL(t *testing.T) {
	e := newNotifyEnv(t)
	for name, tc := range map[string]struct {
		destination, url, message string
	}{
		"http webhook":          {"webhook", "http://example.com/hook", "Enter an https URL."},
		"credentials":           {"webhook", "https://user:pw@example.com/hook", "username or password"},
		"fragment":              {"webhook", "https://example.com/hook#x", "Enter an https URL."},
		"slack other host":      {"slack", "https://evil.example/services/T/B/x", "Enter a Slack incoming webhook URL"},
		"slack http":            {"slack", "http://hooks.slack.com/services/T/B/x", "Enter a Slack incoming webhook URL"},
		"slack credentials":     {"slack", "https://u:p@hooks.slack.com/services/x", "Enter a Slack incoming webhook URL"},
		"loopback outside fake": {"webhook", "http://127.0.0.1:9/hook", "Enter an https URL."},
	} {
		in := e.webhookInput("r", func(in map[string]any) { in["destination"], in["url"] = tc.destination, tc.url })
		if name == "loopback outside fake" {
			// Without FakeBase the same URL is not accepted; the test environment has it, so check the real rule directly.
			if err := (&notify.Senders{}).ValidateDestination("webhook", store.NotificationConfig{}, notify.Secret{URL: tc.url}); err == nil {
				t.Errorf("%s: accepted outside fake mode", name)
			}
			continue
		}
		e.invalidFor(in, tc.message)
	}
}

func TestNotificationRoutes_EmailNeedsSMTP(t *testing.T) {
	email := func(e *notifyEnv) map[string]any {
		return e.webhookInput("mail", func(in map[string]any) {
			in["destination"], in["recipients"] = "email", []string{"ops@example.com"}
			delete(in, "url")
		})
	}
	e := newNotifyEnv(t)
	e.invalidFor(email(e), "Set HM_SMTP_HOST to send email.")
	if e.list().EmailAvailable {
		t.Error("email_available without SMTP")
	}
	withMail := newNotifyEnv(t, notifyEnvOptions{smtp: true})
	created := withMail.create(email(withMail))
	if created.Destination != "email" || created.SecretSet || created.Target != "ops@example.com" || len(created.Recipients) != 1 {
		t.Fatalf("email route = %+v", created)
	}
	if !withMail.list().EmailAvailable {
		t.Error("email_available is false although email can be sent")
	}
}

func TestNotificationRoutes_NeedSessionKey(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{noSessionKey: true})
	e.invalidFor(e.webhookInput("no key"), "Set HM_SESSION_KEY before adding notification routes.")
	if e.list().SecretsAvailable {
		t.Error("secrets_available without a session key")
	}
}

func TestNotificationRoutes_UnreadableSecretIsFlagged(t *testing.T) {
	e := newNotifyEnv(t)
	other, err := secretbox.New("another-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := other.Seal([]byte(`{"url":"https://example.com/old"}`))
	route, err := e.st.CreateNotificationRoute(t.Context(), store.NotificationRoute{
		Name: "old", Destination: "webhook", Config: store.NotificationConfig{URLHost: "example.com"}, SecretEnc: sealed,
		MinSeverity: "warning", Kinds: []string{"service_failing"}, QuietTimezone: "UTC",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	list := e.list()
	if len(list.Items) != 1 || !list.Items[0].SecretUnreadable || !list.Items[0].SecretSet {
		t.Fatalf("list = %+v, want secret_unreadable", list.Items)
	}
	// Saving without retyping the destination cannot work: there is nothing to keep.
	update := e.webhookInput("old", func(in map[string]any) { delete(in, "url"); in["version"] = route.Version })
	resp := e.put(route.ID, update)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("update without a destination = %d, want 422", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Enter the destination again." {
		t.Errorf("message = %q", msg)
	}
	update["url"] = e.hookURL
	if resp := e.put(route.ID, update); resp.StatusCode != http.StatusOK {
		t.Fatalf("update with a new destination = %d", resp.StatusCode)
	}
	if e.list().Items[0].SecretUnreadable {
		t.Error("the route is still unreadable after a new destination")
	}
}

// ---- secrets ----

func TestNotificationRoutes_SecretsAreWriteOnly(t *testing.T) {
	e := newNotifyEnv(t)
	const signing, token = "SIGNING-SECRET-VALUE", "123456789:SECRETTOKENSECRETTOKENSECRETTOKEN"
	hook := e.fake.URL + "/fake-notify/webhook/ok?token=URLSECRET"
	secrets := []string{signing, token, "URLSECRET"}

	webhook := e.create(e.webhookInput("hook", func(in map[string]any) { in["url"], in["signing_secret"] = hook, signing }))
	telegram := e.create(e.webhookInput("tg", func(in map[string]any) {
		in["destination"], in["chat_id"], in["thread_id"], in["bot_token"] = "telegram", "-1001234567890", 7, token
		delete(in, "url")
	}))
	if !webhook.SecretSet || !webhook.Signed || !telegram.SecretSet {
		t.Fatalf("flags: webhook %+v telegram %+v", webhook, telegram)
	}

	var bodies []string
	read := func(resp *http.Response) {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodies = append(bodies, string(body))
	}
	read(e.admin.get("/api/notifications/routes"))
	// A PUT without a url keeps delivering to the same URL; with a new one it switches.
	keep := e.webhookInput("hook", func(in map[string]any) { delete(in, "url"); in["version"] = webhook.Version })
	resp := e.put(webhook.ID, keep)
	read(resp)
	read(e.admin.post("/api/notifications/preview", e.webhookInput("hook", func(in map[string]any) { in["url"], in["signing_secret"] = hook, signing })))
	for _, id := range []int64{webhook.ID, telegram.ID} {
		read(e.admin.post("/api/notifications/routes/"+strconv.FormatInt(id, 10)+"/test", nil))
	}
	for _, body := range bodies {
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("a response contains the secret %q:\n%s", secret, body)
			}
		}
	}
	for _, secret := range secrets {
		if strings.Contains(e.logs.String(), secret) {
			t.Errorf("the server log contains %q", secret)
		}
	}
	audit, err := e.st.ListAudit(t.Context(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(audit)
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Errorf("an audit row contains %q", secret)
		}
	}
	// The kept URL still receives the test: the first test went to hook?token=URLSECRET (path /webhook/ok).
	if !strings.Contains(e.received(), `"event":"test"`) {
		t.Errorf("no test event reached the receiver:\n%s", e.received())
	}

	// A new URL switches where deliveries go.
	switched := e.webhookInput("hook", func(in map[string]any) {
		in["url"] = e.fake.URL + "/fake-notify/webhook/fail"
		in["version"] = webhook.Version + 1
	})
	if resp := e.put(webhook.ID, switched); resp.StatusCode != http.StatusOK {
		t.Fatalf("switch = %d", resp.StatusCode)
	}
	var test NotificationDeliveryDTO
	decode(t, e.admin.post("/api/notifications/routes/"+strconv.FormatInt(webhook.ID, 10)+"/test", nil), &test)
	if test.Status != "failed" {
		t.Errorf("after switching to the failing URL the test = %+v, want failed", test)
	}
}

func TestNotificationRoutes_AuditHasFieldNamesOnly(t *testing.T) {
	e := newNotifyEnv(t)
	const secretURL = "SECRETPATHTOKEN"
	created := e.create(e.webhookInput("audited", func(in map[string]any) { in["url"] = e.fake.URL + "/fake-notify/webhook/ok?x=" + secretURL }))
	update := e.webhookInput("audited", func(in map[string]any) {
		in["version"], in["enabled"] = created.Version, true
		delete(in, "url")
	})
	e.put(created.ID, update)
	update["version"] = 2
	update["url"] = e.fake.URL + "/fake-notify/webhook/ok?x=NEWSECRET"
	e.put(created.ID, update)
	e.admin.do(http.MethodDelete, "/api/notifications/routes/"+strconv.FormatInt(created.ID, 10), nil, nil)

	rows, err := e.st.ListAudit(t.Context(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	secretChanged := map[bool]int{}
	for _, row := range rows {
		if !strings.HasPrefix(row.Action, "notification_route_") {
			continue
		}
		actions[row.Action]++
		raw, _ := json.Marshal(row.Details)
		for _, secret := range []string{secretURL, "NEWSECRET", "127.0.0.1", "fake-notify"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s audit row contains %q: %s", row.Action, secret, raw)
			}
		}
		if row.Target != "audited" {
			t.Errorf("audit target = %q, want the route name", row.Target)
		}
		if v, ok := row.Details["secret_changed"].(bool); ok {
			secretChanged[v]++
		}
	}
	for _, action := range []string{"notification_route_create", "notification_route_update", "notification_route_delete"} {
		if want := map[string]int{"notification_route_update": 2}[action]; (want == 0 && actions[action] != 1) || (want != 0 && actions[action] != want) {
			t.Errorf("%s rows = %d (all: %v)", action, actions[action], actions)
		}
	}
	if secretChanged[true] != 2 || secretChanged[false] != 1 {
		t.Errorf("secret_changed values = %v, want create and the URL switch true, the plain update false", secretChanged)
	}
}

func TestNotificationRoutes_TelegramFields(t *testing.T) {
	e := newNotifyEnv(t)
	token := "123456789:" + strings.Repeat("Ab-_", 9)
	created := e.create(e.webhookInput("tg", func(in map[string]any) {
		in["destination"], in["chat_id"], in["thread_id"], in["bot_token"], in["label"] = "telegram", "-1001234567890", 42, token, "Ops chat"
		delete(in, "url")
	}))
	if created.ChatID != "-1001234567890" || created.ThreadID == nil || *created.ThreadID != 42 || created.Target != "Ops chat" || !created.SecretSet {
		t.Fatalf("created = %+v", created)
	}
	noLabel := e.create(e.webhookInput("tg2", func(in map[string]any) {
		in["destination"], in["chat_id"], in["bot_token"] = "telegram", "@ops_alerts", token
		delete(in, "url")
	}))
	if noLabel.Target != "@ops_alerts" || noLabel.ThreadID != nil {
		t.Errorf("no-label route = %+v, want the chat id as its target and no topic", noLabel)
	}
	e.invalidFor(e.webhookInput("tg3", func(in map[string]any) {
		in["destination"], in["chat_id"], in["bot_token"] = "telegram", "not a chat", token
		delete(in, "url")
	}), "chat ID")
}

// ---- scope ----

func TestNotificationRoutes_ScopeRoundTripsWithNamesAndMissing(t *testing.T) {
	e := newNotifyEnv(t)
	qa, err := e.st.CreateTag(t.Context(), "QA", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.Seed(e.st).UpsertClusters(t.Context(), []model.Cluster{{Key: nxKey.ClusterKey, PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.Seed(e.st).UpsertServices(t.Context(), []model.Service{{Key: nxKey, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	gone := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "deleted-svc"}
	goneCluster := model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "retired"}
	scope := []any{
		accountScope(homelabAccountID, 2),
		map[string]any{"kind": "account", "account_id": stagingAccountID, "region": "us-east-1", "threshold": 1},
		clusterScope(nxKey.ClusterKey, 3),
		clusterScope(goneCluster, 4),
		serviceScope(nxKey),
		serviceScope(gone),
		map[string]any{"kind": "harbormaster", "tag_id": int64(qa.ID)},
		map[string]any{"kind": "aws", "key": "env", "value": "staging"},
		serviceScope(nxKey), // a repeat of a service entry is dropped
	}
	created := e.create(e.webhookInput("scoped", func(in map[string]any) { in["scope"] = scope }))
	if len(created.Scope) != 8 || created.MatchesNothing {
		t.Fatalf("scope = %+v (matches_nothing %v), want 8 selectors", created.Scope, created.MatchesNothing)
	}
	kinds := make([]string, len(created.Scope))
	for i, sel := range created.Scope {
		kinds[i] = sel.Kind
	}
	if got := strings.Join(kinds, ","); got != "account,account,cluster,cluster,service,service,harbormaster,aws" {
		t.Fatalf("selector order = %s", got)
	}
	threshold := func(sel NotificationSelectorDTO) int {
		if sel.Threshold == nil {
			return -1
		}
		return *sel.Threshold
	}
	if a, b := created.Scope[0], created.Scope[1]; threshold(a) != 2 || threshold(b) != 1 || a.AccountName != "homelab-admin" || a.Missing {
		t.Errorf("account entries = %+v %+v", a, b)
	}
	present, missing := created.Scope[2], created.Scope[3]
	if present.ClusterID != nxKey.ClusterKey.ID() || present.Cluster != "core-services" || threshold(present) != 3 || present.Missing || present.AccountName != "homelab-admin" || present.Region != "us-east-1" {
		t.Errorf("cluster = %+v", present)
	}
	if !missing.Missing || threshold(missing) != 4 || missing.Cluster != "retired" {
		t.Errorf("a missing cluster must keep its threshold: %+v", missing)
	}
	byKind := map[string][]NotificationSelectorDTO{}
	for _, sel := range created.Scope {
		byKind[sel.Kind] = append(byKind[sel.Kind], sel)
	}
	for _, sel := range append(append(byKind["service"], byKind["harbormaster"]...), byKind["aws"]...) {
		if sel.Threshold != nil {
			t.Errorf("a %s entry has a threshold: %+v", sel.Kind, sel)
		}
	}
	var presentService, absentService NotificationSelectorDTO
	for _, sel := range byKind["service"] {
		if sel.Service == "payments-api" {
			presentService = sel
		} else {
			absentService = sel
		}
	}
	if presentService.ServiceID != nxKey.ID() || presentService.Missing || presentService.Cluster != "core-services" {
		t.Errorf("present service = %+v", presentService)
	}
	if !absentService.Missing || absentService.Service != "deleted-svc" || absentService.ServiceID != gone.ID() {
		t.Errorf("missing service = %+v, want it kept, flagged and named from its key", absentService)
	}
	if tag := byKind["harbormaster"][0]; tag.TagID != int64(qa.ID) || tag.TagName != "QA" {
		t.Errorf("tag = %+v", tag)
	}
	if tag := byKind["aws"][0]; tag.Key != "env" || tag.Value != "staging" {
		t.Errorf("aws tag = %+v", tag)
	}

	only := e.create(e.webhookInput("only missing", func(in map[string]any) { in["scope"] = []any{serviceScope(gone), clusterScope(goneCluster, 2)} }))
	if !only.MatchesNothing || len(only.Scope) != 2 || !only.Scope[0].Missing || !only.Scope[1].Missing {
		t.Fatalf("only-missing route = %+v, want matches_nothing", only)
	}

	// A cluster or account entry without a threshold defaults to 1.
	defaulted := e.create(e.webhookInput("default threshold", func(in map[string]any) { in["scope"] = []any{clusterScope(nxKey.ClusterKey)} }))
	if len(defaulted.Scope) != 1 || threshold(defaulted.Scope[0]) != 1 {
		t.Errorf("default threshold = %+v", defaulted.Scope)
	}
}

func TestNotificationRoutes_ScopeValidation(t *testing.T) {
	e := newNotifyEnv(t)
	tooMany := make([]any, 101)
	for i := range tooMany {
		tooMany[i] = map[string]any{"kind": "aws", "key": "k" + strconv.Itoa(i), "value": "v"}
	}
	for name, tc := range map[string]struct {
		scope   []any
		message string // "" = any 422
	}{
		"101 selectors":          {tooMany, ""},
		"bad cluster id":         {[]any{map[string]any{"kind": "cluster", "cluster_id": "not-a-cluster-id", "threshold": 1}}, "One of the scope entries is not valid."},
		"bad service id":         {[]any{map[string]any{"kind": "service", "service_id": "%%%"}}, "One of the scope entries is not valid."},
		"unknown tag":            {[]any{map[string]any{"kind": "harbormaster", "tag_id": 987654}}, "One of the scope entries is not valid."},
		"aws prefix":             {[]any{map[string]any{"kind": "aws", "key": "aws:cloudformation:stack-name", "value": "x"}}, "One of the scope entries is not valid."},
		"short account id":       {[]any{map[string]any{"kind": "account", "account_id": "1234", "threshold": 1}}, "One of the scope entries is not valid."},
		"unknown kind":           {[]any{map[string]any{"kind": "region", "account_id": "123456789012"}}, "One of the scope entries is not valid."},
		"bad region":             {[]any{map[string]any{"kind": "account", "account_id": "123456789012", "region": "not a region!", "threshold": 1}}, "One of the scope entries is not valid."},
		"threshold 0":            {[]any{clusterScope(nxKey.ClusterKey, 0)}, "A threshold must be between 1 and 500."},
		"threshold 501":          {[]any{accountScope(homelabAccountID, 501)}, "A threshold must be between 1 and 500."},
		"negative threshold":     {[]any{clusterScope(nxKey.ClusterKey, -3)}, "A threshold must be between 1 and 500."},
		"threshold on a service": {[]any{map[string]any{"kind": "service", "service_id": nxKey.ID(), "threshold": 2}}, "One of the scope entries is not valid."},
		"threshold on a tag":     {[]any{map[string]any{"kind": "aws", "key": "env", "value": "x", "threshold": 2}}, "One of the scope entries is not valid."},
		"same cluster twice":     {[]any{clusterScope(nxKey.ClusterKey, 1), clusterScope(nxKey.ClusterKey, 2)}, "A cluster or account can appear once per route."},
		"same account twice":     {[]any{accountScope(homelabAccountID, 1), accountScope(homelabAccountID, 3)}, "A cluster or account can appear once per route."},
	} {
		in := e.webhookInput("scope", func(in map[string]any) { in["scope"] = tc.scope })
		resp := e.admin.post("/api/notifications/routes", in)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
			continue
		}
		if _, msg := errorCode(t, resp); tc.message != "" && msg != tc.message {
			t.Errorf("%s: message %q, want %q", name, msg, tc.message)
		}
	}
	// The same account as all regions and as one region are two entries, and may both be saved.
	both := []any{accountScope(homelabAccountID, 1), map[string]any{"kind": "account", "account_id": homelabAccountID, "region": "eu-west-1", "threshold": 2}}
	if resp := e.admin.post("/api/notifications/routes", e.webhookInput("regions", func(in map[string]any) { in["scope"] = both })); resp.StatusCode != http.StatusOK {
		t.Errorf("all regions plus one region = %d, want 200", resp.StatusCode)
	}
}

func TestNotificationTargets_ListsAccountsClustersAndServices(t *testing.T) {
	e := newNotifyEnv(t)
	now := time.Now()
	if _, err := storetest.Seed(e.st).UpsertClusters(t.Context(), []model.Cluster{{Key: nxKey.ClusterKey, PolledAt: now}, {Key: nwKey.ClusterKey, PolledAt: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.Seed(e.st).UpsertServices(t.Context(), []model.Service{
		{Key: nxKey, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: now},
		{Key: nyKey, Status: "INACTIVE", Raw: []byte(`{}`), PolledAt: now},
		{Key: nwKey, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	var got NotificationTargetsDTO
	decode(t, e.admin.get("/api/notifications/targets"), &got)
	if len(got.Accounts) != 2 || got.Accounts[0].Name != "homelab-admin" || len(got.Accounts[0].Regions) != 3 || got.Accounts[0].AccountID != homelabAccountID {
		t.Fatalf("accounts = %+v", got.Accounts)
	}
	if len(got.Clusters) != 2 || got.Clusters[0].ID != nxKey.ClusterKey.ID() || got.Clusters[0].Name != "core-services" || got.Clusters[0].AccountName != "homelab-admin" || got.Clusters[0].Region != "us-east-1" {
		t.Fatalf("clusters = %+v", got.Clusters)
	}
	if len(got.Services) != 2 || got.Services[0].ID != nxKey.ID() || got.Services[0].Name != "payments-api" || got.Services[0].ClusterID != nxKey.ClusterKey.ID() || got.Services[0].Cluster != "core-services" {
		t.Fatalf("services = %+v, want the ACTIVE ones only", got.Services)
	}
}

// ---- preview ----

func TestNotificationPreview_CountsOpenAlertsAndRenders(t *testing.T) {
	e := newNotifyEnv(t)
	e.seedAlerts()
	all := e.preview(e.webhookInput("draft"))
	// kinds service_failing and restart_loop, warning and critical: payments-api, image-resizer, web.
	if all.MatchCount != 3 || len(all.Matches) != 3 || all.Sample {
		t.Fatalf("preview = %+v (%s)", all, matchTitles(all))
	}
	if all.Matches[0].Severity != "critical" {
		t.Errorf("matches = %s, want critical first", matchTitles(all))
	}
	if all.Destination != "webhook" || !strings.Contains(all.JSON, `"event": "alert.raised"`) {
		t.Errorf("rendered = %+v", all)
	}

	critical := e.preview(e.webhookInput("draft", func(in map[string]any) { in["min_severity"] = "critical" }))
	if critical.MatchCount != 2 {
		t.Errorf("a critical-only draft matched %d, want the 2 danger alerts: %s", critical.MatchCount, matchTitles(critical))
	}
	everyKind := e.preview(e.webhookInput("draft", func(in map[string]any) {
		in["kinds"] = []string{"service_failing", "service_degraded", "restart_loop", "account_unreachable"}
	}))
	if everyKind.MatchCount != 5 || !strings.Contains(matchTitles(everyKind), "homelab unreachable") {
		t.Errorf("an empty scope must include the account alert: %s", matchTitles(everyKind))
	}

	none := e.preview(e.webhookInput("draft", func(in map[string]any) { in["scope"] = []any{serviceScope(nyKey)} }))
	if none.MatchCount != 0 || !none.Sample || len(none.Matches) != 0 || !strings.Contains(none.JSON, "image-resizer: restart loop") {
		t.Errorf("zero matches = %+v, want the built-in sample", none)
	}
	for _, p := range []NotificationPreviewDTO{all, critical, none} {
		if strings.Contains(p.JSON, "URLSECRET") || strings.Contains(p.Body, e.hookURL) {
			t.Error("the preview carries the destination")
		}
	}

	slack := e.preview(e.webhookInput("draft", func(in map[string]any) { in["destination"] = "slack"; delete(in, "url") }))
	if slack.Title == "" || slack.JSON != "" {
		t.Errorf("slack preview = %+v, want a title and body but no JSON", slack)
	}
}

func TestNotificationPreview_ScopeIsExact(t *testing.T) {
	e := newNotifyEnv(t)
	e.seedAlerts()
	e.seedActiveServices(nxKey, nyKey, nExtraKey, nzKey, nwKey)
	draft := func(scope ...any) NotificationPreviewDTO {
		return e.preview(e.webhookInput("draft", func(in map[string]any) {
			in["scope"] = scope
			in["kinds"] = []string{"service_failing", "service_degraded", "restart_loop", "account_unreachable"}
		}))
	}
	if got := draft(serviceScope(nxKey)); matchTitles(got) != "payments-api: failing" || got.MatchCount != 1 || len(got.Groups) != 0 || got.NotificationCount != 1 {
		t.Errorf("service scope = %q groups %d, want only that service per alert", matchTitles(got), len(got.Groups))
	}

	cluster := draft(clusterScope(nxKey.ClusterKey, 1))
	if cluster.MatchCount != 0 || len(cluster.Matches) != 0 || len(cluster.Groups) != 1 || cluster.NotificationCount != 1 {
		t.Fatalf("cluster scope = %+v, want no per-alert match and one aggregated notification", cluster)
	}
	g := cluster.Groups[0]
	if g.Kind != "cluster" || g.Label != "core-services" || g.Failing != 2 || g.Total != 3 || g.Threshold != 1 || !g.Open || g.Unreachable || g.ClusterID != nxKey.ClusterKey.ID() ||
		strings.Join(g.Services, ",") != "payments-api,auth" {
		t.Errorf("cluster group = %+v", g)
	}
	if !strings.Contains(cluster.JSON, `"scope": "cluster"`) || !strings.Contains(cluster.JSON, `"failing": 2`) {
		t.Errorf("the rendered message must be the aggregated one: %s", cluster.JSON)
	}

	account := draft(accountScope(homelabAccountID, 1))
	if len(account.Groups) != 1 || account.MatchCount != 0 || !account.Groups[0].Unreachable || account.Groups[0].Kind != "account" || account.Groups[0].Label != "homelab-admin" {
		t.Errorf("account scope = %+v, want one group opened by the unreachable account", account.Groups)
	}
	if got := draft(map[string]any{"kind": "account", "account_id": homelabAccountID, "region": "eu-west-1", "threshold": 1}); got.NotificationCount != 0 || len(got.Groups) != 0 {
		t.Errorf("another region's account scope = %+v, want nothing", got.Groups)
	}
	// A cluster entry never receives the account-level alert per alert.
	if got := draft(clusterScope(nxKey.ClusterKey, 1)); strings.Contains(matchTitles(got), "unreachable") {
		t.Errorf("a cluster entry listed the account alert: %q", matchTitles(got))
	}
}

func TestNotificationPreview_FallbackCountsOnlyUncoveredAlerts(t *testing.T) {
	e := newNotifyEnv(t)
	e.seedAlerts()
	e.seedActiveServices(nxKey, nyKey, nExtraKey, nzKey, nwKey)
	specific := e.create(e.webhookInput("payments", func(in map[string]any) { in["enabled"] = true; in["scope"] = []any{clusterScope(nxKey.ClusterKey, 1)} }))
	draft := func(routeID *int64) NotificationPreviewDTO {
		return e.preview(e.webhookInput("catch-all", func(in map[string]any) {
			in["fallback"] = true
			if routeID != nil {
				in["route_id"] = *routeID
			}
		}))
	}
	// The cluster route notifies about payments-api (1 of 3 at N=1), so the catch-all counts only the rest.
	if got := draft(nil); got.MatchCount != 2 || strings.Contains(matchTitles(got), "payments-api") {
		t.Errorf("fallback preview = %q, want what the cluster route does not cover", matchTitles(got))
	}
	// Editing the specific route itself: it no longer counts as covering.
	if got := draft(&specific.ID); got.MatchCount != 3 {
		t.Errorf("preview excluding the edited route = %d, want all 3", got.MatchCount)
	}
	// Raise the threshold past the failing count: a cluster below its threshold covers nothing (D41).
	update := e.webhookInput("payments", func(in map[string]any) {
		in["enabled"], in["scope"], in["version"] = true, []any{clusterScope(nxKey.ClusterKey, 2)}, specific.Version
	})
	if resp := e.put(specific.ID, update); resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	if got := draft(nil); got.MatchCount != 3 {
		t.Errorf("preview with the cluster route below its threshold = %d, want 3", got.MatchCount)
	}
	// A disabled route does not cover.
	update["enabled"], update["version"] = false, specific.Version+1
	e.put(specific.ID, update)
	if got := draft(nil); got.MatchCount != 3 {
		t.Errorf("preview with the specific route disabled = %d, want 3", got.MatchCount)
	}
}

func TestNotificationPreview_OwnerExample(t *testing.T) {
	e := newNotifyEnv(t)
	e.seedOnlyAlert()
	e.seedActiveServices(nxKey, nyKey, nExtraKey)
	clusterDraft := func(n int) NotificationPreviewDTO {
		return e.preview(e.webhookInput("draft", func(in map[string]any) {
			in["scope"] = []any{clusterScope(nxKey.ClusterKey, n)}
			in["kinds"] = []string{"restart_loop"}
		}))
	}
	one := clusterDraft(1)
	if one.NotificationCount != 1 || len(one.Groups) != 1 || !one.Groups[0].Open || one.Groups[0].Failing != 1 || one.Groups[0].Total != 3 {
		t.Errorf("N=1: %+v", one)
	}
	two := clusterDraft(2)
	if two.NotificationCount != 0 || len(two.Groups) != 1 || two.Groups[0].Open || two.Groups[0].Failing != 1 || two.Groups[0].Threshold != 2 {
		t.Errorf("N=2: %+v, want the entry listed below its threshold and nothing sent", two)
	}
	for _, n := range []int{1, 2} {
		service := e.preview(e.webhookInput("draft", func(in map[string]any) {
			in["scope"] = []any{serviceScope(nxKey), clusterScope(nxKey.ClusterKey, n)}
			in["kinds"] = []string{"restart_loop"}
		}))
		if service.MatchCount != 1 {
			t.Errorf("service entry beside a cluster entry at N=%d: %d matches, want 1", n, service.MatchCount)
		}
	}
}

func TestNotificationPreview_PendingCountsUnsettledAlerts(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{settle: 3 * time.Minute})
	e.seedAlertsAged(90 * time.Second) // service_failing is 90 s old; restart_loop does not wait
	e.seedActiveServices(nxKey, nyKey, nzKey, nwKey)
	got := e.preview(e.webhookInput("draft"))
	if got.MatchCount != 1 || !strings.Contains(matchTitles(got), "image-resizer") {
		t.Fatalf("matches = %q, want only the restart loop", matchTitles(got))
	}
	if got.Pending != 2 {
		t.Errorf("pending = %d, want the two service_failing alerts still inside the settle time", got.Pending)
	}
	cluster := e.preview(e.webhookInput("draft", func(in map[string]any) { in["scope"] = []any{clusterScope(nxKey.ClusterKey, 1)} }))
	if cluster.NotificationCount != 0 || cluster.Pending != 1 {
		t.Errorf("cluster draft: count %d pending %d, want 0 and 1 (payments-api is unsettled)", cluster.NotificationCount, cluster.Pending)
	}
}

// The preview and the dispatcher run the same planner; this pins that they agree on one dataset.
func TestNotificationPreview_MatchesWhatTheDispatcherSends(t *testing.T) {
	e := newNotifyEnv(t)
	e.seedAlerts()
	e.seedActiveServices(nxKey, nyKey, nExtraKey, nzKey, nwKey)
	body := e.webhookInput("both modes", func(in map[string]any) {
		in["enabled"] = true
		in["kinds"] = []string{"service_failing", "service_degraded", "restart_loop", "account_unreachable"}
		in["scope"] = []any{clusterScope(nxKey.ClusterKey, 2), serviceScope(nzKey), accountScope(stagingAccountID, 1)}
	})
	route := e.create(body)
	body["route_id"] = route.ID
	preview := e.preview(body)

	dispatcher := &notify.Dispatcher{Store: e.st, Senders: e.api.Notify, Box: e.api.NotifyBox, BaseURL: "https://hm.example.com", Now: time.Now, Log: e.api.Log}
	if err := dispatcher.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	dispatcher.Wait()
	rows, _, err := e.st.ListNotificationDeliveries(t.Context(), store.DeliveryFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != preview.NotificationCount || preview.NotificationCount != 3 {
		t.Fatalf("dispatcher planned %d notifications, preview said %d (want 3: the cluster aggregate, image-resizer and the staging account)", len(rows), preview.NotificationCount)
	}
}

func TestNotificationTest_SendsOnceAndLogs(t *testing.T) {
	e := newNotifyEnv(t)
	good := e.create(e.webhookInput("good"))
	var delivered NotificationDeliveryDTO
	decode(t, e.admin.post("/api/notifications/routes/"+strconv.FormatInt(good.ID, 10)+"/test", nil), &delivered)
	if delivered.Status != "delivered" || delivered.Event != "test" || delivered.Alert != "Test message" || delivered.Route != "good" || delivered.Destination != "webhook" ||
		!strings.HasPrefix(delivered.Note, "Delivered in ") || delivered.Attempts != 1 || delivered.RouteID == nil || *delivered.RouteID != good.ID || delivered.ID == 0 {
		t.Fatalf("delivery = %+v", delivered)
	}
	if n := strings.Count(e.received(), `"event":"test"`); n != 1 {
		t.Fatalf("the receiver got %d test events, want exactly 1", n)
	}

	bad := e.create(e.webhookInput("bad", func(in map[string]any) { in["url"] = e.fake.URL + "/fake-notify/webhook/fail" }))
	resp := e.admin.post("/api/notifications/routes/"+strconv.FormatInt(bad.ID, 10)+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a failing receiver = %d, want 200 with status failed", resp.StatusCode)
	}
	var failed NotificationDeliveryDTO
	decode(t, resp, &failed)
	if failed.Status != "failed" || failed.Note != "HTTP 503" || !failed.CanRetry {
		t.Fatalf("failed test = %+v", failed)
	}
	if n := strings.Count(e.received(), "/fake-notify/webhook/fail"); n != 1 {
		t.Errorf("the failing receiver was called %d times, want once (no retry)", n)
	}

	// A disabled route can still be tested; an unknown route is a 404.
	if good.Enabled {
		t.Fatal("a new route should start off")
	}
	if resp := e.admin.post("/api/notifications/routes/999999/test", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", resp.StatusCode)
	}
	rows, _, err := e.st.ListNotificationDeliveries(context.Background(), store.DeliveryFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("log rows = %d err=%v, want the two tests", len(rows), err)
	}
}

func TestNotificationTest_RateLimited(t *testing.T) {
	e := newNotifyEnv(t)
	route := e.create(e.webhookInput("limited"))
	path := "/api/notifications/routes/" + strconv.FormatInt(route.ID, 10) + "/test"
	for i := 0; i < 5; i++ {
		if resp := e.admin.post(path, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("test %d = %d", i+1, resp.StatusCode)
		}
	}
	resp := e.admin.post(path, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sixth test = %d, want 429", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "rate_limited" {
		t.Errorf("code = %q", code)
	}
}

func TestNotificationTest_UnreadableSecretIs422(t *testing.T) {
	e := newNotifyEnv(t)
	other, _ := secretbox.New("another-session-key", secretbox.PurposeNotifySecret)
	sealed, _ := other.Seal([]byte(`{"url":"https://example.com/old"}`))
	route, err := e.st.CreateNotificationRoute(t.Context(), store.NotificationRoute{
		Name: "old", Destination: "webhook", SecretEnc: sealed, MinSeverity: "warning", Kinds: []string{"service_failing"}, QuietTimezone: "UTC",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp := e.admin.post("/api/notifications/routes/"+strconv.FormatInt(route.ID, 10)+"/test", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Enter the destination again." {
		t.Errorf("message = %q", msg)
	}
}

var _ = authz.TagID(0)

func TestNotificationRoutes_ReportSettleSeconds(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{settle: 3 * time.Minute})
	if got := e.list().SettleSeconds; got != 180 {
		t.Errorf("settle_seconds = %d, want 180", got)
	}
	off := newNotifyEnv(t)
	if got := off.list().SettleSeconds; got != 0 {
		t.Errorf("settle_seconds = %d, want 0 when the wait is off", got)
	}
}

// A route keeps the selector of a Harbormaster tag that was deleted (it is reported as missing). Saving the route
// again must not fail on it, the same as for a deleted cluster or service; a tag id the route never had is refused.
func TestNotificationRoutes_DeletedTagSelectorIsKeptOnSave(t *testing.T) {
	e := newNotifyEnv(t)
	qa, err := e.st.CreateTag(t.Context(), "QA", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	tagScope := map[string]any{"kind": "harbormaster", "tag_id": int64(qa.ID)}
	created := e.create(e.webhookInput("tagged", func(in map[string]any) { in["scope"] = []any{tagScope} }))
	if err := e.st.DeleteTag(t.Context(), qa.ID); err != nil {
		t.Fatal(err)
	}

	stored := e.list().Items[0]
	if len(stored.Scope) != 1 || !stored.Scope[0].Missing || !stored.MatchesNothing {
		t.Fatalf("route after its tag was deleted = %+v, want the selector kept, missing, and the route matching nothing", stored)
	}
	resp := e.put(created.ID, e.webhookInput("tagged renamed", func(in map[string]any) {
		in["scope"] = []any{tagScope}
		delete(in, "url")
		in["version"] = created.Version
	}))
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("saving a route that keeps its deleted tag = %d %s %s, want 200", resp.StatusCode, code, msg)
	}

	unknown := map[string]any{"kind": "harbormaster", "tag_id": int64(qa.ID) + 1000}
	e.invalidFor(e.webhookInput("new", func(in map[string]any) { in["scope"] = []any{unknown} }), "scope entries is not valid")
	resp = e.put(created.ID, e.webhookInput("tagged renamed", func(in map[string]any) {
		in["scope"] = []any{tagScope, unknown}
		delete(in, "url")
		in["version"] = created.Version + 1
	}))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("adding a tag id the route never had = %d, want 422", resp.StatusCode)
	}
}

// The editor previews the draft of a route that still holds a deleted tag; that must work as saving it does.
func TestNotificationPreview_DraftKeepsADeletedTagOfTheRoute(t *testing.T) {
	e := newNotifyEnv(t)
	qa, err := e.st.CreateTag(t.Context(), "QA", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	tagScope := map[string]any{"kind": "harbormaster", "tag_id": int64(qa.ID)}
	created := e.create(e.webhookInput("tagged", func(in map[string]any) { in["scope"] = []any{tagScope} }))
	if err := e.st.DeleteTag(t.Context(), qa.ID); err != nil {
		t.Fatal(err)
	}
	body := e.webhookInput("tagged", func(in map[string]any) { in["scope"] = []any{tagScope} })
	body["route_id"] = created.ID
	resp := e.admin.post("/api/notifications/preview", body)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("preview = %d %s %s, want 200", resp.StatusCode, code, msg)
	}
}
