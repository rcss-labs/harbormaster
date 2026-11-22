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
	// Group messages (cluster or account entries).
	Scope                     string
	Text                      string
	Threshold, Failing, Total int
	Members                   []store.IncidentMember
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
		m.Subject.Kind, m.Subject.Severity, m.Subject.Title, secret,
		m.Subject.Scope, m.Subject.Text, m.Subject.Threshold, m.Subject.Failing, m.Subject.Total, m.Subject.Members})
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
	clockMu sync.Mutex // guards clock for the tests whose send workers outlive a cycle
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
		Now: func() time.Time {
			e.clockMu.Lock()
			defer e.clockMu.Unlock()
			return e.clock
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			e.sleepMu.Lock()
			e.sleeps = append(e.sleeps, d)
			e.sleepMu.Unlock()
			return nil
		},
		Jitter: func() float64 { return 0 },
	}
}

func (e *env) advance(d time.Duration) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	e.clock = e.clock.Add(d)
}

func (e *env) cycle() {
	e.t.Helper()
	if err := e.d.Cycle(context.Background()); err != nil {
		e.t.Fatalf("cycle: %v", err)
	}
	e.d.Wait()
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

func clusterSel(k model.ClusterKey, threshold int) store.NotificationSelector {
	return store.NotificationSelector{Kind: "cluster", AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster, Threshold: threshold}
}

func accountSel(account, region string, threshold int) store.NotificationSelector {
	return store.NotificationSelector{Kind: "account", AccountID: account, Region: region, Threshold: threshold}
}

// seedServices makes the services ACTIVE in the snapshot, so a group message can say "of M".
func (e *env) seedServices(keys ...model.ServiceKey) {
	e.t.Helper()
	var rows []model.Service
	for _, k := range keys {
		rows = append(rows, model.Service{Key: k, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(e.st).UpsertServices(context.Background(), rows); err != nil {
		e.t.Fatal(err)
	}
}

// groups returns what one route received as group messages, "event:scope:failing/total".
func (r *recordingSender) groups(route string) []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Route == route && rec.Scope != "" {
			out = append(out, fmt.Sprintf("%s:%s:%d/%d", rec.Event, rec.Scope, rec.Failing, rec.Total))
		}
	}
	return out
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
	qa, err := e.st.CreateTag(ctx, "QA", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.Seed(e.st).UpsertServices(ctx, []model.Service{
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
	e.seedServices(svcX, svcY, svcZ, svcW)
	accountAlert := alert(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: acctB, Region: "us-east-1"}}, "account_unreachable", model.AlertDanger)
	accountAlert.Title = "staging unreachable"
	e.route("svc-x", scoped(serviceSel(svcX)))
	e.route("cluster-core", scoped(clusterSel(svcX.ClusterKey, 1)))
	e.route("account-b", scoped(accountSel(acctB, "", 1)))
	e.route("everything")
	e.setAlerts(
		alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger),
		alert(svcZ, "service_failing", model.AlertDanger), alert(svcW, "service_failing", model.AlertDanger), accountAlert)
	e.cycle()
	// Service entries and the empty scope take alerts one by one; cluster and account entries send one aggregate.
	if got := strings.Join(e.sender.to("svc-x"), ","); got != "opened:x" {
		t.Errorf("svc-x got %q, want only x (never y)", got)
	}
	if got := strings.Join(e.sender.to("everything"), ","); got != "opened:,opened:w,opened:x,opened:y,opened:z" {
		t.Errorf("everything got %q, want every alert one by one", got)
	}
	if got := e.sender.groups("cluster-core"); len(got) != 1 || got[0] != "opened:cluster:2/2" {
		t.Errorf("cluster-core got %v, want one aggregate of its two failing services (never z or w)", got)
	}
	if got := e.sender.groups("account-b"); len(got) != 1 || got[0] != "opened:account:1/1" {
		t.Errorf("account-b got %v, want one aggregate for the staging account", got)
	}
	if n := len(e.sender.to("cluster-core")); n != 1 {
		t.Errorf("cluster-core sent %d messages, want 1", n)
	}
}

func TestDispatcher_AccountRegionSelector(t *testing.T) {
	e := newEnv(t)
	us, eu := key(acctA, "us-east-1", "core", "us-svc"), key(acctA, "eu-west-1", "core", "eu-svc")
	e.seedServices(us, eu)
	e.route("eu-only", scoped(accountSel(acctA, "eu-west-1", 1)))
	e.setAlerts(alert(us, "service_failing", model.AlertDanger), alert(eu, "service_failing", model.AlertDanger))
	e.cycle()
	got := e.sender.records()
	if len(got) != 1 || got[0].Scope != "account" || got[0].Failing != 1 || got[0].Total != 1 || len(got[0].Members) != 1 || got[0].Members[0].Service != "eu-svc" {
		t.Fatalf("sent = %+v, want one aggregate holding only the eu service", got)
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
	e.d.Wait()
	second.Wait()
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

func TestDispatcher_DeletedOrDisabledRouteCancelsPendingDelivery(t *testing.T) {
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
		if d.Status != "cancelled" {
			t.Errorf("delivery %+v, want cancelled (a switched-off or deleted route is not a failure)", d)
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
	e.d.Wait()
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

// ---- round 2: thresholds, aggregated incidents, settle ----

var (
	dispCluster = model.ClusterKey{AccountID: acctA, Region: "us-east-1", Cluster: "A"}
	a1          = model.ServiceKey{ClusterKey: dispCluster, Service: "a1"}
	a2          = model.ServiceKey{ClusterKey: dispCluster, Service: "a2"}
	a3          = model.ServiceKey{ClusterKey: dispCluster, Service: "a3"}
)

func TestDispatcher_OwnerExample_ClusterOneAndServiceRouteSendTwo(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.route("service", scoped(serviceSel(a2)))
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	records := e.sender.records()
	if len(records) != 2 {
		t.Fatalf("sent %d messages, want exactly two: %+v", len(records), records)
	}
	if got := e.sender.groups("cluster"); len(got) != 1 || got[0] != "opened:cluster:1/3" {
		t.Errorf("cluster route got %v, want one aggregate, 1 of 3", got)
	}
	for _, rec := range records {
		switch rec.Route {
		case "cluster":
			if len(rec.Members) != 1 || rec.Members[0].Service != "a2" || !strings.Contains(rec.Title, "1 of 3 services failing") {
				t.Errorf("cluster message = %+v", rec)
			}
		case "service":
			if rec.Scope != "" || rec.Service != "a2" || rec.Title != "a2 restart_loop" {
				t.Errorf("service message = %+v, want the per-alert message for a2", rec)
			}
		}
	}
}

func TestDispatcher_OwnerExample_ClusterTwoAndServiceRouteSendOne(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 2)))
	e.route("service", scoped(serviceSel(a2)))
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	records := e.sender.records()
	if len(records) != 1 || records[0].Route != "service" || records[0].Scope != "" {
		t.Fatalf("sent = %+v, want exactly one message, the service route's", records)
	}
	// A second service fails: now the cluster route opens with 2 of 3.
	e.advance(time.Minute)
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning), alert(a3, "restart_loop", model.AlertWarning))
	e.cycle()
	if got := e.sender.groups("cluster"); len(got) != 1 || got[0] != "opened:cluster:2/3" {
		t.Fatalf("cluster route got %v, want one opened at 2 of 3", got)
	}
}

func TestDispatcher_GroupUpdateOnGrowthSilentOnShrink(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 2)), func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning), alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	if got := e.sender.groups("cluster"); len(got) != 1 || got[0] != "opened:cluster:2/3" {
		t.Fatalf("after two failing: %v", got)
	}

	e.advance(time.Minute)
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning), alert(a2, "restart_loop", model.AlertWarning), alert(a3, "restart_loop", model.AlertWarning))
	e.cycle()
	got := e.sender.groups("cluster")
	if len(got) != 2 || got[1] != "update:cluster:3/3" {
		t.Fatalf("a third failing must send one update: %v", got)
	}
	last := e.sender.records()[len(e.sender.records())-1]
	if !strings.Contains(last.Text, "a3 restart_loop") {
		t.Errorf("the update lists all three: %q", last.Text)
	}
	e.cycle()
	if len(e.sender.groups("cluster")) != 2 {
		t.Fatal("an update was repeated within the interval")
	}

	// One recovers: nothing is sent, even after its grace passes.
	e.advance(time.Minute)
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning), alert(a3, "restart_loop", model.AlertWarning))
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	if n := len(e.sender.groups("cluster")); n != 2 {
		t.Fatalf("a recovery that stays at or above the threshold sent something: %v", e.sender.groups("cluster"))
	}

	// The next repeat carries the current list: two.
	e.advance(31 * time.Minute)
	e.cycle()
	got = e.sender.groups("cluster")
	if len(got) != 3 || got[2] != "repeat:cluster:2/3" {
		t.Fatalf("the repeat must list the current two: %v", got)
	}
}

func TestDispatcher_GroupResolvesBelowThresholdAfterGrace(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 2)))
	both := []model.Alert{alert(a1, "restart_loop", model.AlertWarning), alert(a2, "restart_loop", model.AlertWarning)}
	e.setAlerts(both...)
	e.cycle()
	var firstID int64
	for _, d := range e.deliveries() {
		if d.Event == "opened" {
			firstID = *d.IncidentID
		}
	}

	// A dip shorter than the grace sends nothing and keeps the incident.
	e.advance(10 * time.Second)
	e.setAlerts(both[0])
	e.cycle()
	e.advance(60 * time.Second)
	e.setAlerts(both...)
	e.cycle()
	e.advance(10 * time.Minute)
	e.cycle()
	if got := e.sender.groups("cluster"); len(got) != 1 {
		t.Fatalf("a short dip sent %v, want only the opened", got)
	}
	for _, d := range e.deliveries() {
		if *d.IncidentID != firstID {
			t.Errorf("the dip created a new incident: %+v", d)
		}
	}

	// A real drop: nothing for the grace, then a resolved.
	e.setAlerts(both[0])
	e.cycle()
	e.advance(90 * time.Second)
	e.cycle()
	if len(e.sender.groups("cluster")) != 1 {
		t.Fatal("resolved before the grace")
	}
	e.advance(60 * time.Second)
	e.cycle()
	e.cycle()
	got := e.sender.groups("cluster")
	if len(got) != 2 || !strings.HasPrefix(got[1], "resolved:cluster") {
		t.Fatalf("got %v, want a resolved after the grace", got)
	}
}

func TestDispatcher_GroupIsOneIncidentHoweverManyAlerts(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 2)))
	e.setAlerts(
		alert(a1, "restart_loop", model.AlertWarning), alert(a1, "unhealthy_task", model.AlertWarning),
		alert(a2, "restart_loop", model.AlertWarning), alert(a2, "cannot_place", model.AlertDanger))
	for range 3 {
		e.cycle()
		e.advance(10 * time.Second)
	}
	if got := e.sender.groups("cluster"); len(got) != 1 || got[0] != "opened:cluster:2/3" {
		t.Fatalf("got %v, want one message for two services however many alerts", got)
	}
}

func TestDispatcher_ScaleUpThatSettlesSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.d.Settle = 3 * time.Minute
	e.seedServices(a1, a2, a3)
	e.route("service", scoped(serviceSel(a2)))
	e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.route("catch-all", func(r *store.NotificationRoute) { r.Fallback = true })
	e.setAlerts(alert(a2, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(90 * time.Second)
	e.cycle()
	e.setAlerts() // the tasks came up
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(5 * time.Minute)
	e.cycle()
	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("a scale-up that settled notified: %+v", got)
	}
	if rows := e.deliveries(); len(rows) != 0 {
		t.Fatalf("deliveries were planned for a scale-up: %+v", rows)
	}
}

func TestDispatcher_ServiceStillFailingAfterSettleIsSent(t *testing.T) {
	e := newEnv(t)
	e.d.Settle = 3 * time.Minute
	e.seedServices(a1, a2, a3)
	e.route("service", scoped(serviceSel(a2)))
	e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.setAlerts(alert(a2, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(2*time.Minute + 59*time.Second)
	e.cycle()
	if len(e.sender.records()) != 0 {
		t.Fatal("notified before the settle time")
	}
	e.advance(2 * time.Second)
	e.cycle()
	e.cycle()
	if len(e.sender.to("service")) != 1 || len(e.sender.groups("cluster")) != 1 {
		t.Fatalf("after the settle time each route sends once: %+v", e.sender.records())
	}

	// A catch-all alone gets it too.
	only := newEnv(t)
	only.d.Settle = 3 * time.Minute
	only.route("catch-all", func(r *store.NotificationRoute) { r.Fallback = true })
	only.setAlerts(alert(a2, "service_failing", model.AlertDanger))
	only.cycle()
	only.advance(3*time.Minute + time.Second)
	only.cycle()
	if got := only.sender.to("catch-all"); len(got) != 1 {
		t.Fatalf("catch-all got %v", got)
	}
	// Other kinds do not wait.
	quick := newEnv(t)
	quick.d.Settle = 3 * time.Minute
	quick.route("everything")
	quick.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
	quick.cycle()
	if len(quick.sender.to("everything")) != 1 {
		t.Fatal("a restart loop must notify at once")
	}
}

func TestDispatcher_UnreachableAccountOpensTheAccountRoute(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("account", scoped(accountSel(acctA, "", 5)))
	unreachable := alert(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: acctA, Region: "us-east-1"}}, "account_unreachable", model.AlertDanger)
	unreachable.Text = "The role cannot be assumed."
	e.setAlerts(unreachable)
	e.cycle()
	records := e.sender.records()
	if len(records) != 1 || records[0].Scope != "account" || !strings.Contains(records[0].Title, "unreachable") || !strings.Contains(records[0].Text, "The role cannot be assumed.") {
		t.Fatalf("sent = %+v", records)
	}
}

func TestDispatcher_RemovedEntryResolvesSilently(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	route := e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	if len(e.sender.groups("cluster")) != 1 {
		t.Fatal("the group did not open")
	}
	e.update(route, func(r *store.NotificationRoute) { r.Scope = []store.NotificationSelector{serviceSel(a1)} })
	e.advance(10 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.cycle()
	if got := e.sender.groups("cluster"); len(got) != 1 {
		t.Fatalf("got %v, want no resolved message for an entry that no longer exists", got)
	}
}

func TestDispatcher_ThresholdRaisedWhileOpenResolves(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	route := e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	e.update(route, func(r *store.NotificationRoute) { r.Scope = []store.NotificationSelector{clusterSel(dispCluster, 2)} })
	e.advance(10 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.cycle()
	got := e.sender.groups("cluster")
	if len(got) != 2 || !strings.HasPrefix(got[1], "resolved:cluster") {
		t.Fatalf("got %v, want the group resolved after the threshold was raised past it", got)
	}
}

func TestDispatcher_CatchAllAndThreshold(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 2)))
	e.route("catch-all", func(r *store.NotificationRoute) { r.Fallback = true })
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning))
	e.cycle()
	if got := e.sender.to("catch-all"); len(got) != 1 {
		t.Fatalf("the first failing service goes to the catch-all: %v", got)
	}
	e.advance(time.Minute)
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning), alert(a2, "restart_loop", model.AlertWarning))
	e.cycle()
	if got := e.sender.groups("cluster"); len(got) != 1 {
		t.Fatalf("the second opens the cluster route: %v", got)
	}
	if got := e.sender.to("catch-all"); len(got) != 1 {
		t.Fatalf("the second must not also go to the catch-all: %v", got)
	}
}

func TestDispatcher_TwoDispatchersOpenOneGroup(t *testing.T) {
	e := newEnv(t)
	e.seedServices(a1, a2, a3)
	e.route("cluster", scoped(clusterSel(dispCluster, 1)))
	e.setAlerts(alert(a2, "restart_loop", model.AlertWarning))
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
	e.d.Wait()
	second.Wait()
	var open int
	if err := e.st.Pool.QueryRow(context.Background(), `select count(*) from notification_incidents where group_route_id <> 0 and resolved_at is null`).Scan(&open); err != nil || open != 1 {
		t.Fatalf("open group incidents = %d err=%v, want 1", open, err)
	}
	if got := e.sender.groups("cluster"); len(got) != 1 {
		t.Fatalf("got %v, want exactly one opened", got)
	}
}

// Deleting the only Harbormaster tag of a route must not turn it into "everything": the selector stays, its tag is
// gone, and the route matches nothing (D27).
func TestDispatcher_RouteWhoseTagWasDeletedMatchesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	qa, err := e.st.CreateTag(ctx, "QA", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.Seed(e.st).UpsertServices(ctx, []model.Service{
		{Key: svcX, Status: "ACTIVE", Raw: []byte(`{"Tags":[]}`), PolledAt: time.Now()},
		{Key: svcY, Status: "ACTIVE", Raw: []byte(`{"Tags":[]}`), PolledAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServiceTag(ctx, svcY, &qa.ID); err != nil {
		t.Fatal(err)
	}
	e.route("hm-tag", scoped(store.NotificationSelector{Kind: "harbormaster", TagID: qa.ID}))
	if err := e.st.DeleteTag(ctx, qa.ID); err != nil {
		t.Fatal(err)
	}
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning), alert(svcY, "restart_loop", model.AlertWarning))
	e.cycle()
	e.cycle()
	if got := e.sender.to("hm-tag"); len(got) != 0 {
		t.Fatalf("a route whose only tag was deleted widened to everything: %v", got)
	}
}

// Repeated short scale-outs must not add up to a notification: the settle time counts from when the alert came back,
// not from the first time it opened.
func TestDispatcher_RepeatedShortScaleOutsNeverSettle(t *testing.T) {
	e := newEnv(t)
	e.d.Settle = 3 * time.Minute
	e.route("service", scoped(serviceSel(a2)))
	e.setAlerts(alert(a2, "service_degraded", model.AlertWarning))
	e.cycle()
	e.advance(60 * time.Second)
	e.setAlerts() // the tasks came up
	e.cycle()
	e.advance(100 * time.Second) // inside the resolve grace, so it is still one incident
	e.setAlerts(alert(a2, "service_degraded", model.AlertWarning))
	e.cycle()
	e.advance(25 * time.Second) // degraded for 25 s this time; 185 s after the first scale-out began
	e.cycle()
	if got := e.sender.records(); len(got) != 0 {
		t.Fatalf("repeated short scale-outs notified: %+v", got)
	}
	e.advance(3 * time.Minute) // and one that stays degraded still notifies
	e.cycle()
	if got := e.sender.to("service"); len(got) != 1 {
		t.Fatalf("a scale-out that stays degraded past the settle time must notify once, got %v", got)
	}
}

// Retention must not prune the deliveries of an incident that is still open: the planner reads them to know the
// incident was announced, and without them it would announce it again every retention window.
func TestDispatcher_RetentionDoesNotReAnnounceAnOpenIncident(t *testing.T) {
	e := newEnv(t)
	e.route("all", func(r *store.NotificationRoute) { r.NotifyResolved = true })
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	e.cycle()
	e.advance(31 * 24 * time.Hour)
	if _, err := e.st.RunRetention(context.Background(), e.clock, store.Retention{NotificationLog: 30 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	e.cycle()
	if got := e.sender.to("all"); len(got) != 1 {
		t.Fatalf("an incident open longer than the retention window was announced again: %v", got)
	}
	e.setAlerts()
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	if got := e.sender.to("all"); len(got) != 2 || got[1] != "resolved:x" {
		t.Fatalf("resolve must still work after retention, got %v", got)
	}
	// A resolved incident's old deliveries are pruned as before.
	e.advance(31 * 24 * time.Hour)
	if _, err := e.st.RunRetention(context.Background(), e.clock, store.Retention{NotificationLog: 30 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if rows := e.deliveries(); len(rows) != 0 {
		t.Fatalf("deliveries of a resolved incident must be pruned: %+v", rows)
	}
}

// ctxSender blocks the routes named in hang until the send's context ends, as a real HTTP client does when the
// destination never answers, and delegates the rest.
type ctxSender struct {
	inner   Sender
	hang    map[string]bool
	started chan string
	release chan struct{}
}

func (c *ctxSender) Send(ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result {
	if c.hang[r.Name] {
		select {
		case c.started <- r.Name:
		default:
		}
		select {
		case <-ctx.Done():
		case <-c.release:
		}
		return Result{Retryable: true, Note: "Network error"}
	}
	return c.inner.Send(ctx, r, secret, m)
}

func (e *env) hangingSender(routes ...string) *ctxSender {
	c := &ctxSender{inner: e.sender, hang: map[string]bool{}, started: make(chan string, 16), release: make(chan struct{})}
	for _, r := range routes {
		c.hang[r] = true
	}
	e.t.Cleanup(func() { close(c.release) })
	e.d.Senders = c
	return c
}

// cycleReturns runs one cycle and fails when it does not return within wait.
func (e *env) cycleReturns(wait time.Duration) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.d.Cycle(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			e.t.Fatalf("cycle: %v", err)
		}
	case <-time.After(wait):
		e.t.Fatalf("a cycle did not return within %v: one hanging destination stalls everything", wait)
	}
}

func TestDispatcher_AHangingDestinationDoesNotStallOtherRoutes(t *testing.T) {
	e := newEnv(t)
	e.d.Lease = 2 * time.Second // the send budget is then 1.5 s
	hang := e.hangingSender("slow")
	e.route("slow")
	e.route("fast")
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	e.cycleReturns(time.Second)
	e.eventually(time.Second, "the fast route gets its message while the slow one hangs", func() bool { return len(e.sender.to("fast")) == 1 })
	select {
	case <-hang.started:
	case <-time.After(time.Second):
		t.Fatal("the slow route was never tried")
	}

	// Planning and the other routes go on while the slow send is still running.
	e.advance(10 * time.Second)
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning), alert(svcY, "restart_loop", model.AlertWarning))
	e.cycleReturns(time.Second)
	e.eventually(time.Second, "the fast route gets both alerts", func() bool { return len(e.sender.to("fast")) == 2 })
	if e.sender.calls["slow"] != 0 {
		t.Fatalf("the slow route's backlog was sent while the first send was still running")
	}
	planned := 0
	for _, d := range e.deliveries() {
		if d.RouteName == "slow" {
			planned++
		}
	}
	if planned != 2 {
		t.Fatalf("%d rows planned for the slow route, want 2 (planning must not wait for its send)", planned)
	}

	// The send is cut off by its budget, well inside the lease, and its row is given back without an attempt.
	finished := make(chan struct{})
	go func() { e.d.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the slow send outlived its lease")
	}
	for _, d := range e.deliveries() {
		if d.RouteName == "slow" && (d.Status != "pending" || d.Attempts != 0 || d.ClaimedUntil != nil) {
			t.Fatalf("slow row after the budget = %+v, want pending, unleased, no attempt counted", d)
		}
	}
}

func TestDispatcher_AtMostAFewRowsPerRouteEachCycle(t *testing.T) {
	e := newEnv(t)
	e.route("one")
	var alerts []model.Alert
	for i := range 12 {
		alerts = append(alerts, alert(svc(acctA, "us-east-1", "core", "s"+string(rune('a'+i))), "restart_loop", model.AlertWarning))
	}
	e.setAlerts(alerts...)
	e.cycle()
	if got := len(e.sender.to("one")); got != routeCycleLimit {
		t.Fatalf("one cycle sent %d to one route, want %d", got, routeCycleLimit)
	}
	e.cycle()
	e.cycle()
	if got := len(e.sender.to("one")); got != 12 {
		t.Fatalf("after three cycles %d were sent, want all 12", got)
	}
}

// A shutdown in the middle of a send is not the destination's fault: no attempt is counted and the row is claimable
// at once.
func TestDispatcher_ACancelledSendIsNotAnAttempt(t *testing.T) {
	e := newEnv(t)
	hang := e.hangingSender("one")
	e.route("one")
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.d.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hang.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the send never started")
	}
	cancel()
	e.d.Wait()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "pending" || rows[0].Attempts != 0 || rows[0].ClaimedUntil != nil || rows[0].Note != "" {
		t.Fatalf("row after a shutdown = %+v, want pending, no attempt, no lease, no failure note", rows)
	}
}

// A route that was switched off is not a failing route: its leftover rows are cancelled, not failed.
func TestDispatcher_RouteOffIsCancelledNotFailed(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(string, Event, int) Result { return Result{Retryable: true, StatusCode: 503, Note: "HTTP 503"} }
	route := e.route("one")
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	e.cycle() // fails retryably: the row is pending for a retry
	e.update(route, func(r *store.NotificationRoute) { r.Enabled = false })
	e.advance(5 * time.Minute)
	e.cycle()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "cancelled" || rows[0].Note != "The route is off." {
		t.Fatalf("rows = %+v, want one cancelled row", rows)
	}
	sum, err := e.st.NotificationSummary(context.Background(), e.clock)
	if err != nil || sum.Failed24h != 0 || len(sum.FailingRouteIDs) != 0 {
		t.Fatalf("summary = %+v err=%v, want no failures", sum, err)
	}
}

// A manual retry of an alert that has resolved since must not send "opened" for something that is no longer open
// (a PagerDuty incident would stay open for good).
func TestDispatcher_RetryOfAResolvedAlertIsNotSent(t *testing.T) {
	e := newEnv(t)
	e.sender.reply = func(string, Event, int) Result { return Result{StatusCode: 400, Note: "HTTP 400"} }
	e.route("one")
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	e.cycle() // opened fails
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.advance(2 * time.Hour) // far outside the window in which planning cancels unsent rows
	e.cycle()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("rows = %+v, want the one failed opened row", rows)
	}
	if _, err := e.st.RetryNotificationDelivery(context.Background(), rows[0].ID, e.clock); err != nil {
		t.Fatal(err)
	}
	before := len(e.sender.records())
	e.cycle()
	if len(e.sender.records()) != before {
		t.Fatal("a retry sent opened for an alert that has resolved")
	}
	if got := e.deliveries()[0]; got.Status != "cancelled" || got.Note != "Resolved before it was sent." {
		t.Fatalf("retried row = %+v, want cancelled with the resolved note", got)
	}
}

// A held aggregate is rendered when it is sent, not from the plan-time snapshot, and an update that the first
// message already covers is not sent after it.
func TestDispatcher_HeldAggregateRendersCurrentStateAndSkipsCoveredUpdate(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.seedServices(a1, a2, a3)
	e.route("night", scoped(clusterSel(dispCluster, 1)), func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning))
	e.cycle()
	e.advance(time.Minute)
	e.setAlerts(alert(a1, "restart_loop", model.AlertWarning), alert(a2, "restart_loop", model.AlertWarning))
	e.cycle() // the group got worse: an update is planned, also held
	if len(e.sender.records()) != 0 {
		t.Fatalf("sent during quiet hours: %+v", e.sender.records())
	}
	e.clock = time.Date(2026, 11, 21, 8, 1, 0, 0, time.UTC)
	e.cycle()
	if got := e.sender.groups("night"); len(got) != 1 || got[0] != "opened:cluster:2/3" {
		t.Fatalf("got %v, want one opened message with the current 2 of 3 (not the planned 1 of 3, and no update after it)", got)
	}
	for _, d := range e.deliveries() {
		if d.Event == "update" && d.Status != "cancelled" {
			t.Errorf("update row = %+v, want cancelled as covered by the opened message", d)
		}
	}
}

// A test row whose API task died between the insert and the finish does not stay pending for good.
func TestDispatcher_FailsAnAbandonedTestRow(t *testing.T) {
	e := newEnv(t)
	route := e.route("one")
	lease := e.clock.Add(-time.Minute)
	if _, err := e.st.InsertNotificationTest(context.Background(), store.NotificationDelivery{
		RouteID: &route.ID, RouteName: route.Name, Destination: route.Destination, Target: "hooks.example", Severity: "warning",
		AlertTitle: "test", Status: "pending", ClaimedUntil: &lease, CreatedAt: e.clock.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	e.cycle()
	rows := e.deliveries()
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("rows = %+v, want the abandoned test failed", rows)
	}
}

func (e *env) eventually(wait time.Duration, what string, ok func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An alert that is still open must be announced on a route that was switched off while its opened row waited and
// then switched on again: the cancelled row is brought back, not blocked by its unique key. Repeats work after it.
func TestDispatcher_ReenabledRouteAnnouncesAnAlertThatIsStillOpen(t *testing.T) {
	e := newEnv(t)
	fail := true
	e.sender.reply = func(string, Event, int) Result {
		if fail {
			return Result{Retryable: true, StatusCode: 503, Note: "HTTP 503"}
		}
		return Result{Delivered: true, StatusCode: 200, Note: "Delivered in 5 ms"}
	}
	route := e.route("r", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.setAlerts(alert(svcX, "restart_loop", model.AlertWarning))
	e.cycle() // attempt 1 fails; a retry is pending
	route = e.update(route, func(r *store.NotificationRoute) { r.Enabled = false })
	e.advance(40 * time.Second)
	e.cycle() // the retry meets a route that is off: cancelled
	fail = false
	e.update(route, func(r *store.NotificationRoute) { r.Enabled = true })
	e.advance(20 * time.Second)
	e.cycle()
	e.cycle()
	delivered := func(event string) int {
		n := 0
		for _, d := range e.deliveries() {
			if d.Event == event && d.Status == "delivered" {
				n++
			}
		}
		return n
	}
	if got := delivered("opened"); got != 1 {
		t.Fatalf("%d opened messages delivered after the route was switched on again, want exactly 1: %+v", got, e.deliveries())
	}
	e.advance(31 * time.Minute)
	e.cycle()
	if got := delivered("repeat"); got != 1 {
		t.Fatalf("%d repeats delivered, want 1: repeats must work after the revived opened", got)
	}
}

// The send budget ends before the earliest lease the batch holds, whatever time passed between the claim and the
// worker's start.
func TestDispatcher_SendBudgetEndsBeforeTheLeaseNotWhenTheWorkerStarted(t *testing.T) {
	e := newEnv(t)
	at := func(d time.Duration) *time.Time { v := e.clock.Add(d); return &v }
	fresh := store.NotificationDelivery{ClaimedUntil: at(leaseDuration)}
	if got := e.d.budgetFor([]store.NotificationDelivery{fresh}); got != leaseDuration-20*time.Second {
		t.Fatalf("a fresh claim has budget %v, want %v", got, leaseDuration-20*time.Second)
	}
	late := store.NotificationDelivery{ClaimedUntil: at(30 * time.Second)} // claimed 90 s ago
	if got := e.d.budgetFor([]store.NotificationDelivery{fresh, late}); got != 10*time.Second {
		t.Fatalf("a claim with 30 s of lease left has budget %v, want 10s (the earliest lease decides)", got)
	}
	gone := store.NotificationDelivery{ClaimedUntil: at(-time.Second)}
	if got := e.d.budgetFor([]store.NotificationDelivery{gone}); got != 0 {
		t.Fatalf("an expired lease has budget %v, want 0", got)
	}
}
