package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	acctA = "111111111111"
	acctB = "222222222222"
)

type sentRecord struct {
	Route      string
	Event      Event
	DeliveryID int64
	Service    string
	Cluster    string
	Account    string
	Kind       string
	Severity   string
	Title      string
	Secret     Secret
}

// recordingSender stands in for Senders. reply decides each result; the default delivers.
type recordingSender struct {
	mu    sync.Mutex
	sent  []sentRecord
	reply func(route string, event Event, call int) Result
	calls map[string]int
}

func (r *recordingSender) Send(_ context.Context, route store.NotificationRoute, secret Secret, m Message) Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sentRecord{route.Name, m.Event, m.DeliveryID, m.Subject.Key.Service, m.Subject.Key.Cluster, m.Subject.Key.AccountID,
		m.Subject.Kind, m.Subject.Severity, m.Subject.Title, secret})
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[route.Name]++
	if r.reply != nil {
		return r.reply(route.Name, m.Event, r.calls[route.Name])
	}
	return Result{Delivered: true, StatusCode: 200, Duration: 5 * time.Millisecond, Note: "Delivered in 5 ms"}
}

func (r *recordingSender) records() []sentRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentRecord(nil), r.sent...)
}

// to lists "event:service" of what one route received, sorted.
func (r *recordingSender) to(route string) []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Route == route {
			out = append(out, string(rec.Event)+":"+rec.Service)
		}
	}
	sort.Strings(out)
	return out
}

func (r *recordingSender) count(route string, event Event) int {
	n := 0
	for _, rec := range r.records() {
		if rec.Route == route && rec.Event == event {
			n++
		}
	}
	return n
}

type env struct {
	t       *testing.T
	st      *store.Store
	box     *secretbox.Box
	sender  *recordingSender
	d       *Dispatcher
	clock   time.Time
	sleepMu sync.Mutex
	sleeps  []time.Duration
	targets map[awsx.Target][]model.Alert
}

func newEnv(t *testing.T) *env {
	t.Helper()
	box, err := secretbox.New("session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: storetest.Open(t), box: box, sender: &recordingSender{},
		clock: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC), targets: map[awsx.Target][]model.Alert{}}
	e.d = e.newDispatcher()
	for _, a := range []store.AWSAccount{
		{Name: "prod", AccountID: acctA, RoleARN: "arn:r1", ExternalID: "e1", Regions: []string{"us-east-1", "eu-west-1"}, Enabled: true},
		{Name: "staging", AccountID: acctB, RoleARN: "arn:r2", ExternalID: "e2", Regions: []string{"us-east-1"}, Enabled: true},
	} {
		if _, err := e.st.CreateAccount(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) newDispatcher() *Dispatcher {
	return &Dispatcher{
		Store: e.st, Senders: e.sender, Box: e.box, BaseURL: "https://hm.example.com", Interval: time.Hour,
		Now: func() time.Time { return e.clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			e.sleepMu.Lock()
			e.sleeps = append(e.sleeps, d)
			e.sleepMu.Unlock()
			return nil
		},
		Jitter: func() float64 { return 0 },
	}
}

func (e *env) advance(d time.Duration) { e.clock = e.clock.Add(d) }

func (e *env) cycle() {
	e.t.Helper()
	if err := e.d.Cycle(context.Background()); err != nil {
		e.t.Fatalf("cycle: %v", err)
	}
}

func key(account, region, cluster, service string) model.ServiceKey {
	return svc(account, region, cluster, service)
}

func alert(k model.ServiceKey, kind string, severity model.AlertSeverity) model.Alert {
	return model.Alert{Key: k, Kind: kind, Severity: severity, Title: k.Service + " " + kind, Text: "text of " + k.Service, At: time.Now(), Tab: "overview"}
}

// setAlerts makes exactly these alerts the estate's: every target seen before is rewritten.
func (e *env) setAlerts(alerts ...model.Alert) {
	e.t.Helper()
	next := map[awsx.Target][]model.Alert{}
	for target := range e.targets {
		next[target] = nil
	}
	for _, a := range alerts {
		target := awsx.Target{AccountID: a.Key.AccountID, Region: a.Key.Region}
		next[target] = append(next[target], a)
	}
	for target, list := range next {
		if err := e.st.ReplaceAlerts(context.Background(), target, list); err != nil {
			e.t.Fatal(err)
		}
	}
	e.targets = next
}

func (e *env) seal(secret Secret) []byte {
	raw, _ := json.Marshal(secret)
	sealed, err := e.box.Seal(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	return sealed
}

// route creates an enabled webhook route taking warnings and criticals of every kind from everywhere.
func (e *env) route(name string, mutate ...func(*store.NotificationRoute)) store.NotificationRoute {
	e.t.Helper()
	kinds := make([]string, len(Kinds))
	for i, k := range Kinds {
		kinds[i] = k.Kind
	}
	r := store.NotificationRoute{
		Name: name, Enabled: true, Destination: DestWebhook, Config: store.NotificationConfig{URLHost: "hooks.example"},
		SecretEnc: e.seal(Secret{URL: "https://hooks.example/" + name}), MinSeverity: "warning", Kinds: kinds,
		QuietTimezone: "UTC", NotifyResolved: true,
	}
	for _, m := range mutate {
		m(&r)
	}
	created, err := e.st.CreateNotificationRoute(context.Background(), r, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return created
}

func (e *env) update(r store.NotificationRoute, mutate func(*store.NotificationRoute)) store.NotificationRoute {
	e.t.Helper()
	mutate(&r)
	updated, err := e.st.UpdateNotificationRoute(context.Background(), r, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return updated
}

func (e *env) deliveries() []store.NotificationDelivery {
	e.t.Helper()
	rows, _, err := e.st.ListNotificationDeliveries(context.Background(), store.DeliveryFilter{Limit: 500})
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func scoped(selectors ...store.NotificationSelector) func(*store.NotificationRoute) {
	return func(r *store.NotificationRoute) { r.Scope = selectors }
}

func serviceSel(k model.ServiceKey) store.NotificationSelector {
	return store.NotificationSelector{Kind: "service", AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster, Service: k.Service}
}

func clusterSel(k model.ClusterKey) store.NotificationSelector {
	return store.NotificationSelector{Kind: "cluster", AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster}
}

var (
	svcX = key(acctA, "us-east-1", "core", "x")
	svcY = key(acctA, "us-east-1", "core", "y")
	svcZ = key(acctA, "us-east-1", "batch", "z")
	svcW = key(acctB, "us-east-1", "web", "w")
)

func TestDispatcher_OpenedIsSentOncePerRoute(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.route("two")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	for range 3 {
		e.cycle()
		e.advance(10 * time.Second)
	}
	if got := len(e.sender.records()); got != 2 {
		t.Fatalf("%d deliveries, want 2 (one per route): %v", got, e.sender.records())
	}
	if e.sender.count("one", EventOpened) != 1 || e.sender.count("two", EventOpened) != 1 {
		t.Errorf("sent = %+v", e.sender.records())
	}
	for _, d := range e.deliveries() {
		if d.Status != "delivered" || d.Attempts != 1 || d.Note != "Delivered in 5 ms" {
			t.Errorf("delivery = %+v", d)
		}
	}
}

func TestDispatcher_SendsTheOpenedSecretAndSnapshot(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	got := e.sender.records()
	if len(got) != 1 || got[0].Secret.URL != "https://hooks.example/one" || got[0].Service != "x" || got[0].Severity != "critical" || got[0].Title != "x service_failing" {
		t.Fatalf("sent = %+v", got)
	}
}

func TestDispatcher_SkipsDisabledAndNonMatchingRoutes(t *testing.T) {
	e := newEnv(t)
	e.route("disabled", func(r *store.NotificationRoute) { r.Enabled = false })
	e.route("critical-only", func(r *store.NotificationRoute) { r.MinSeverity = "critical" })
	e.route("other-kind", func(r *store.NotificationRoute) { r.Kinds = []string{"restart_loop"} })
	e.route("other-service", scoped(serviceSel(svcY)))
	e.route("takes-it")
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle()
	if got := e.sender.records(); len(got) != 1 || got[0].Route != "takes-it" {
		t.Fatalf("sent = %+v, want only takes-it", got)
	}
}

func TestDispatcher_SelectorsMatchAWSAndHarbormasterTags(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	qa, err := e.st.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpsertServices(ctx, []model.Service{
		{Key: svcX, Status: "ACTIVE", Raw: []byte(`{"Tags":[{"Key":"env","Value":"staging"}]}`), PolledAt: time.Now()},
		{Key: svcY, Status: "ACTIVE", Raw: []byte(`{"Tags":[]}`), PolledAt: time.Now()},
		{Key: svcZ, Status: "ACTIVE", Raw: []byte(`{"Tags":[]}`), PolledAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServiceTag(ctx, svcY, &qa.ID); err != nil {
		t.Fatal(err)
	}
	e.route("aws-tag", scoped(store.NotificationSelector{Kind: "aws", Key: "env", Value: "staging"}))
	e.route("hm-tag", scoped(store.NotificationSelector{Kind: "harbormaster", TagID: authz.TagID(qa.ID)}))
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger), alert(svcZ, "service_failing", model.AlertDanger))
	e.cycle()
	if got := e.sender.to("aws-tag"); strings.Join(got, ",") != "opened:x" {
		t.Errorf("aws-tag route got %v, want only x", got)
	}
	if got := e.sender.to("hm-tag"); strings.Join(got, ",") != "opened:y" {
		t.Errorf("hm-tag route got %v, want only y", got)
	}
}

func TestDispatcher_ScopedRoutesGetOnlyTheirAlerts(t *testing.T) {
	e := newEnv(t)
	accountAlert := alert(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: acctB, Region: "us-east-1"}}, "account_unreachable", model.AlertDanger)
	accountAlert.Title = "staging unreachable"
	e.route("svc-x", scoped(serviceSel(svcX)))
	e.route("cluster-core", scoped(clusterSel(svcX.ClusterKey)))
	e.route("account-b", scoped(store.NotificationSelector{Kind: "account", AccountID: acctB}))
	e.route("everything")
	e.setAlerts(
		alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger),
		alert(svcZ, "service_failing", model.AlertDanger), alert(svcW, "service_failing", model.AlertDanger), accountAlert)
	e.cycle()
	want := map[string]string{
		"svc-x":        "opened:x",
		"cluster-core": "opened:x,opened:y",
		"account-b":    "opened:,opened:w", // the account-level alert has no service
		"everything":   "opened:,opened:w,opened:x,opened:y,opened:z",
	}
	for route, expected := range want {
		if got := strings.Join(e.sender.to(route), ","); got != expected {
			t.Errorf("route %s got %q, want %q", route, got, expected)
		}
	}
}

func TestDispatcher_AccountRegionSelector(t *testing.T) {
	e := newEnv(t)
	us, eu := key(acctA, "us-east-1", "core", "us-svc"), key(acctA, "eu-west-1", "core", "eu-svc")
	e.route("eu-only", scoped(store.NotificationSelector{Kind: "account", AccountID: acctA, Region: "eu-west-1"}))
	e.setAlerts(alert(us, "service_failing", model.AlertDanger), alert(eu, "service_failing", model.AlertDanger))
	e.cycle()
	if got := strings.Join(e.sender.to("eu-only"), ","); got != "opened:eu-svc" {
		t.Fatalf("got %q, want only the eu service", got)
	}
}

func TestDispatcher_MissingTargetMatchesNothingThenMatchesAgain(t *testing.T) {
	e := newEnv(t)
	e.route("scoped", scoped(serviceSel(svcX)))
	e.setAlerts(alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("a route scoped to an absent service sent %+v", got)
	}
	e.advance(time.Minute)
	e.setAlerts(alert(svcY, "service_failing", model.AlertDanger), alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if got := strings.Join(e.sender.to("scoped"), ","); got != "opened:x" {
		t.Fatalf("got %q, want x once it exists", got)
	}
}

func TestDispatcher_FallbackOnlyWhenNoOtherRouteMatches(t *testing.T) {
	e := newEnv(t)
	e.route("A", scoped(serviceSel(svcX)), func(r *store.NotificationRoute) { r.MinSeverity = "critical" })
	e.route("B", func(r *store.NotificationRoute) { r.Fallback = true })
	e.setAlerts(
		alert(svcX, "service_failing", model.AlertDanger), // A covers it
		alert(svcY, "service_failing", model.AlertDanger), // nobody specific
	)
	e.cycle()
	if got := strings.Join(e.sender.to("A"), ","); got != "opened:x" {
		t.Errorf("A got %q", got)
	}
	if got := strings.Join(e.sender.to("B"), ","); got != "opened:y" {
		t.Errorf("B got %q, want only y", got)
	}

	// A warning on X: A is critical-only, so it does not match and B takes it.
	e.advance(time.Minute)
	e.setAlerts(
		alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger),
		alert(key(acctA, "us-east-1", "core", "x2"), "service_degraded", model.AlertWarning))
	e.route("A2", scoped(serviceSel(key(acctA, "us-east-1", "core", "x2"))), func(r *store.NotificationRoute) { r.MinSeverity = "critical" })
	e.cycle()
	if got := strings.Join(e.sender.to("B"), ","); got != "opened:x2,opened:y" {
		t.Errorf("B got %q, want y and the warning no critical-only route takes", got)
	}
}

func TestDispatcher_FallbackIgnoresDisabledRoutes(t *testing.T) {
	e := newEnv(t)
	e.route("A", scoped(serviceSel(svcX)), func(r *store.NotificationRoute) { r.Enabled = false })
	e.route("B", func(r *store.NotificationRoute) { r.Fallback = true })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if got := strings.Join(e.sender.to("B"), ","); got != "opened:x" {
		t.Fatalf("B got %q, want x because A is disabled", got)
	}
}

func TestDispatcher_FallbackKeepsRepeatingAfterASpecificRouteAppears(t *testing.T) {
	e := newEnv(t)
	e.route("B", func(r *store.NotificationRoute) { r.Fallback = true; r.RepeatSeconds = 1800 })
	e.setAlerts(alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()

	e.advance(time.Minute)
	e.route("C", scoped(serviceSel(svcY)))
	e.cycle()
	if got := strings.Join(e.sender.to("C"), ","); got != "opened:y" {
		t.Errorf("C got %q, want one opened", got)
	}
	if e.sender.count("B", EventOpened) != 1 {
		t.Errorf("B got a second opened: %v", e.sender.to("B"))
	}

	e.advance(31 * time.Minute)
	e.cycle()
	if e.sender.count("B", EventRepeat) != 1 {
		t.Errorf("B repeats = %d, want its repeat to continue", e.sender.count("B", EventRepeat))
	}
	e.setAlerts()
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.cycle()
	if e.sender.count("B", EventResolved) != 1 {
		t.Errorf("B resolved = %d, want 1", e.sender.count("B", EventResolved))
	}
}

func TestDispatcher_EnablingARouteNotifiesOpenAlertsOnce(t *testing.T) {
	e := newEnv(t)
	off := e.route("later", func(r *store.NotificationRoute) { r.Enabled = false })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_degraded", model.AlertWarning))
	e.cycle()
	if len(e.sender.records()) != 0 {
		t.Fatal("a disabled route sent")
	}
	e.update(off, func(r *store.NotificationRoute) { r.Enabled = true })
	for range 3 {
		e.advance(10 * time.Second)
		e.cycle()
	}
	if got := strings.Join(e.sender.to("later"), ","); got != "opened:x,opened:y" {
		t.Fatalf("got %q, want each open alert once", got)
	}
}

func TestDispatcher_RepeatsAtTheInterval(t *testing.T) {
	e := newEnv(t)
	e.route("repeating", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.route("quiet")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(29 * time.Minute)
	e.cycle()
	if e.sender.count("repeating", EventRepeat) != 0 {
		t.Fatal("repeated before 30 minutes")
	}
	e.advance(61 * time.Second) // 30 minutes and a second after the opened row was planned
	e.cycle()
	if e.sender.count("repeating", EventRepeat) != 1 {
		t.Fatalf("repeats at +30 min = %d, want 1", e.sender.count("repeating", EventRepeat))
	}
	e.advance(29 * time.Minute)
	e.cycle()
	e.advance(2 * time.Minute)
	e.cycle()
	if got := e.sender.count("repeating", EventRepeat); got != 2 {
		t.Fatalf("repeats at +60 min = %d, want 2", got)
	}
	if e.sender.count("quiet", EventRepeat) != 0 {
		t.Error("a route that does not repeat repeated")
	}
	seqs := map[int]bool{}
	for _, d := range e.deliveries() {
		if d.RouteName == "repeating" && d.Event == "repeat" {
			seqs[d.Seq] = true
		}
	}
	if !seqs[1] || !seqs[2] {
		t.Errorf("repeat sequence numbers = %v, want 1 and 2", seqs)
	}
}

func TestDispatcher_QuietHoursHoldWarningsNotCriticals(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.route("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	if got := strings.Join(e.sender.to("night"), ","); got != "opened:y" {
		t.Fatalf("at 23:00 got %q, want only the critical", got)
	}
	var held *store.NotificationDelivery
	for _, d := range e.deliveries() {
		if d.Status == "held" {
			d := d
			held = &d
		}
	}
	if held == nil || held.HeldUntil == nil || !held.HeldUntil.Equal(time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)) || held.Key.Service != "x" {
		t.Fatalf("held row = %+v, want x held until 08:00", held)
	}
	e.clock = time.Date(2026, 11, 21, 7, 59, 0, 0, time.UTC)
	e.cycle()
	if len(e.sender.records()) != 1 {
		t.Fatal("the held warning was sent before 08:00")
	}
	e.clock = time.Date(2026, 11, 21, 8, 0, 1, 0, time.UTC)
	e.cycle()
	if got := strings.Join(e.sender.to("night"), ","); got != "opened:x,opened:y" {
		t.Fatalf("at 08:00 got %q, want the held warning released", got)
	}
}

func TestDispatcher_HeldThenResolvedIsCancelled(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.route("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle()
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.clock = time.Date(2026, 11, 21, 8, 1, 0, 0, time.UTC)
	e.cycle()
	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("sent %+v, want nothing: not the warning and not a resolved message", got)
	}
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "cancelled" || rows[0].Note != "Resolved during quiet hours; not sent." {
		t.Fatalf("rows = %+v, want one cancelled row with the quiet hours note", rows)
	}
}

func TestDispatcher_ResolvedOnlyAfterOpenedWasDelivered(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(route string, _ Event, _ int) Result {
		if route == "failing" {
			return Result{StatusCode: 400, Note: "HTTP 400"}
		}
		return Result{Delivered: true, Note: "Delivered in 1 ms"}
	}
	e.route("works")
	e.route("failing")
	e.route("no-resolved", func(r *store.NotificationRoute) { r.NotifyResolved = false })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.setAlerts()
	e.advance(10 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.cycle()
	if e.sender.count("works", EventResolved) != 1 {
		t.Errorf("works: resolved = %d, want 1", e.sender.count("works", EventResolved))
	}
	if e.sender.count("failing", EventResolved) != 0 {
		t.Error("a route whose opened failed got a resolved message")
	}
	if e.sender.count("no-resolved", EventResolved) != 0 {
		t.Error("a route with Notify when resolved off got a resolved message")
	}
}

func TestDispatcher_FlapInsideGraceSendsNothingNew(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	a := alert(svcX, "service_failing", model.AlertDanger)
	e.setAlerts(a)
	e.cycle()
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.setAlerts(a)
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(10 * time.Minute)
	e.cycle()
	if got := strings.Join(e.sender.to("one"), ","); got != "opened:x" {
		t.Fatalf("got %q, want the one opened and no resolved or second opened", got)
	}
}

func TestDispatcher_RetriesThenFailsWithAttemptCount(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(route string, _ Event, _ int) Result {
		if route == "bad-request" {
			return Result{StatusCode: 400, Note: "HTTP 400"}
		}
		return Result{Retryable: true, StatusCode: 503, Note: "HTTP 503"}
	}
	e.route("flaky")
	e.route("bad-request")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if e.sender.calls["flaky"] != 1 {
		t.Fatalf("first attempt calls = %d", e.sender.calls["flaky"])
	}
	e.advance(29 * time.Second)
	e.cycle()
	if e.sender.calls["flaky"] != 1 {
		t.Fatal("retried before the 30 s backoff")
	}
	e.advance(2 * time.Second)
	e.cycle()
	if e.sender.calls["flaky"] != 2 {
		t.Fatalf("second attempt calls = %d, want 2", e.sender.calls["flaky"])
	}
	e.advance(2*time.Minute + time.Second)
	e.cycle()
	for _, d := range e.deliveries() {
		switch d.RouteName {
		case "flaky":
			if d.Status != "failed" || d.Attempts != 3 || d.Note != "HTTP 503 after 3 attempts" {
				t.Errorf("flaky = %+v", d)
			}
		case "bad-request":
			if d.Status != "failed" || d.Attempts != 1 || d.Note != "HTTP 400" {
				t.Errorf("bad-request = %+v", d)
			}
		}
	}
	e.advance(time.Hour)
	e.cycle()
	if e.sender.calls["flaky"] != 3 {
		t.Errorf("a failed delivery was attempted again: %d calls", e.sender.calls["flaky"])
	}
}

func TestDispatcher_RetryAfterIsHonouredWhenLarger(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(string, Event, int) Result {
		return Result{Retryable: true, StatusCode: 429, RetryAfter: 90 * time.Second, Note: "HTTP 429"}
	}
	e.route("limited")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(60 * time.Second)
	e.cycle()
	if e.sender.calls["limited"] != 1 {
		t.Fatal("retried before Retry-After")
	}
	e.advance(31 * time.Second)
	e.cycle()
	if e.sender.calls["limited"] != 2 {
		t.Fatalf("calls = %d, want a retry after 90 s", e.sender.calls["limited"])
	}
}

func TestDispatcher_ManualRetryNotesDeliveredOnRetry(t *testing.T) {
	e := newEnv(t)
	failing := true
	e.sender.reply = func(string, Event, int) Result {
		if failing {
			return Result{StatusCode: 400, Note: "HTTP 400"}
		}
		return Result{Delivered: true, Duration: 7 * time.Millisecond, Note: "Delivered in 7 ms"}
	}
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("rows = %+v", rows)
	}
	failing = false
	if _, err := e.st.RetryNotificationDelivery(context.Background(), rows[0].ID, e.clock); err != nil {
		t.Fatal(err)
	}
	e.cycle()
	if got := e.deliveries()[0]; got.Status != "delivered" || got.Note != "Delivered on retry in 7 ms" || got.Retries != 1 {
		t.Fatalf("after retry = %+v", got)
	}
}

func TestDispatcher_TwoDispatchersSendOnce(t *testing.T) {
	e := newEnv(t)
	e.route("one", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.route("two")
	var alerts []model.Alert
	for i := range 6 {
		alerts = append(alerts, alert(key(acctA, "us-east-1", "core", fmt.Sprintf("svc%d", i)), "service_failing", model.AlertDanger))
	}
	e.setAlerts(alerts...)
	second := e.newDispatcher()
	var wg sync.WaitGroup
	for range 10 {
		for _, d := range []*Dispatcher{e.d, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := d.Cycle(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	seen := map[int64]int{}
	for _, rec := range e.sender.records() {
		seen[rec.DeliveryID]++
	}
	if len(seen) != 12 {
		t.Fatalf("%d distinct deliveries sent, want 12", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("delivery %d sent %d times", id, n)
		}
	}
}

func TestDispatcher_NewDispatcherContinuesFromStoredState(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.route("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle() // plans and holds
	if len(e.sender.records()) != 0 {
		t.Fatal("sent during quiet hours")
	}
	e.d = e.newDispatcher() // the leader failed over: a fresh dispatcher, no memory
	e.clock = time.Date(2026, 11, 21, 8, 0, 5, 0, time.UTC)
	e.cycle()
	e.cycle()
	if got := strings.Join(e.sender.to("night"), ","); got != "opened:x" {
		t.Fatalf("got %q, want the held row sent once by the new dispatcher", got)
	}
	opened := 0
	for _, d := range e.deliveries() {
		if d.Event == "opened" {
			opened++
		}
	}
	if opened != 1 {
		t.Errorf("%d opened rows, want 1", opened)
	}
}

func TestDispatcher_UndecryptableSecretFailsWithFixedNote(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	other, _ := secretbox.New("a-different-key", secretbox.PurposeNotifySecret)
	e.d.Box = other
	e.cycle()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "failed" || rows[0].Note != "Enter the destination again." || rows[0].Attempts != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if len(e.sender.records()) != 0 {
		t.Error("a route with an unreadable secret was sent")
	}

	e2 := newEnv(t)
	e2.route("one")
	e2.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e2.d.Box = nil
	e2.cycle()
	if rows := e2.deliveries(); len(rows) != 1 || rows[0].Note != "Enter the destination again." {
		t.Fatalf("without a box: %+v", rows)
	}
}

func TestDispatcher_DeletedOrDisabledRouteFailsPendingDelivery(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(string, Event, int) Result { return Result{Retryable: true, Note: "HTTP 503", StatusCode: 503} }
	deleted := e.route("deleted")
	off := e.route("off")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle() // both attempted once, retry pending
	if err := e.st.DeleteNotificationRoute(context.Background(), deleted.ID); err != nil {
		t.Fatal(err)
	}
	e.update(off, func(r *store.NotificationRoute) { r.Enabled = false })
	e.advance(time.Minute)
	e.cycle()
	notes := map[string]string{}
	for _, d := range e.deliveries() {
		if d.Status != "failed" {
			t.Errorf("delivery %+v, want failed", d)
		}
		notes[d.RouteName] = d.Note
	}
	if notes["deleted"] != "The route was deleted." || notes["off"] != "The route is off." {
		t.Errorf("notes = %v", notes)
	}
}

func TestDispatcher_PacesSendsPerRoute(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger), alert(svcZ, "service_failing", model.AlertDanger))
	e.cycle()
	if len(e.sleeps) != 2 || e.sleeps[0] != time.Second || e.sleeps[1] != time.Second {
		t.Fatalf("sleeps = %v, want two of 1 s for three sends to one route", e.sleeps)
	}
	e2 := newEnv(t)
	e2.route("a")
	e2.route("b")
	e2.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e2.cycle()
	if len(e2.sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none: one send per route", e2.sleeps)
	}
}

func TestDispatcher_ScopeErrorSkipsPlanningNotSending(t *testing.T) {
	e := newEnv(t)
	e.route("pending-one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	// Plan x, but make the first send retry so a row is still due next cycle.
	calls := 0
	e.sender.reply = func(string, Event, int) Result {
		calls++
		if calls == 1 {
			return Result{Retryable: true, StatusCode: 503, Note: "HTTP 503"}
		}
		return Result{Delivered: true, Note: "Delivered in 1 ms"}
	}
	e.cycle()
	// A tag route whose lookup fails, and a catch-all that would take a new alert.
	e.route("tag-route", scoped(store.NotificationSelector{Kind: "aws", Key: "env", Value: "x"}))
	e.route("catch-all", func(r *store.NotificationRoute) { r.Fallback = true })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.d.Tags = failingTags{}
	e.advance(time.Minute)
	err := e.d.Cycle(context.Background())
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("err = %v, want the scope error returned", err)
	}
	if e.sender.count("pending-one", EventOpened) != 2 {
		t.Errorf("the due retry was not sent: %v", e.sender.records())
	}
	if e.sender.count("catch-all", EventOpened) != 0 {
		t.Error("planning ran with a partial picture and sent to the catch-all")
	}
	for _, d := range e.deliveries() {
		if d.RouteName == "catch-all" {
			t.Errorf("a delivery was planned for the catch-all: %+v", d)
		}
	}
}

type failingTags struct{}

func (failingTags) OffHoursMatches(context.Context, []store.OffHoursSelector) ([]store.OffHoursMatch, error) {
	return nil, fmt.Errorf("lookup failed")
}

func TestDispatcher_CountsOutcomes(t *testing.T) {
	e := newEnv(t)
	registry := telemetry.New()
	e.d.Telemetry = registry
	e.sender.reply = func(route string, _ Event, _ int) Result {
		switch route {
		case "bad":
			return Result{StatusCode: 400, Note: "HTTP 400"}
		case "flaky":
			return Result{Retryable: true, StatusCode: 503, Note: "HTTP 503"}
		}
		return Result{Delivered: true, Note: "Delivered in 1 ms"}
	}
	e.route("good")
	e.route("bad")
	e.route("flaky")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	var out strings.Builder
	if err := registry.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`harbormaster_notification_deliveries_total{destination="webhook",result="delivered"} 1`,
		`harbormaster_notification_deliveries_total{destination="webhook",result="failed"} 1`,
		`harbormaster_notification_deliveries_total{destination="webhook",result="retried"} 1`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("metrics lack %q:\n%s", want, out.String())
		}
	}
}
