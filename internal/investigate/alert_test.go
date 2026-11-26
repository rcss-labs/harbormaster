package investigate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const groupHead = "Services that were failing when the alert was raised, of those you can see:"

// everything is all that a request tells the agent, its ids decoded as well.
func everything(t *testing.T, body agentclient.RunRequest) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, id := range append([]string{body.Task.Subject.ClusterID}, body.Task.Subject.ServiceIDs...) {
		decoded, err := base64.RawURLEncoding.DecodeString(id)
		if err != nil {
			t.Fatal(err)
		}
		out += "\n" + string(decoded)
	}
	return out
}

// hide moves the services to a tag the identity holds nothing on.
func (f *fixture) hide(keys ...model.ServiceKey) {
	f.t.Helper()
	ctx := context.Background()
	tag, err := f.st.CreateTag(ctx, "finance", "red", false)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, k := range keys {
		if err := f.st.SetServiceTag(ctx, k, &tag.ID); err != nil {
			f.t.Fatal(err)
		}
	}
}

// requestGroup records the investigation of a cluster or account entry whose text lists every member, as notify writes it.
func (f *fixture) requestGroup(kind, title string, keys ...model.ServiceKey) int64 {
	f.t.Helper()
	members := []store.InvestigationMember{}
	var text []string
	for i, k := range keys {
		members = append(members, store.InvestigationMember{Cluster: k.Cluster, Service: k.Service})
		if i < 10 {
			text = append(text, k.Service+": 0 of 3 tasks running")
		}
	}
	raw, err := json.Marshal(members)
	if err != nil {
		f.t.Fatal(err)
	}
	cluster, subjectKey := "", "account/482109375512/us-east-1"
	if kind == "cluster" {
		cluster, subjectKey = checkoutKey.Cluster, "cluster/482109375512/us-east-1/payments-prod"
	}
	id := f.request(checkoutKey.Service)
	f.exec(`update agent_investigations set subject_kind = $2, subject_key = $3, cluster_name = $4, service_name = '', members = $5,
		alert_kind = $6, alert_title = $7, alert_text = $8 where id = $1`, id, kind, subjectKey, cluster, raw, kind+"_failing", title,
		strings.Join(append(text, "Notifies when 2 or more services are failing."), "\n"))
	return id
}

var (
	refundsKey = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "refunds-worker"}
	// payrollKey is in another cluster of the same account and region, and has the name of a service of payments-prod.
	payrollKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: checkoutKey.AccountID, Region: checkoutKey.Region, Cluster: "payroll-prod"}, Service: "checkout-api"}
)

func TestJob_GroupTextNamesOnlyVisibleServices(t *testing.T) {
	for _, c := range []struct {
		kind, title, wantTitle, wantText string
		hidden                           []model.ServiceKey
		absent                           []string
	}{
		{"cluster", "payments-prod: 3 of 12 services failing", "payments-prod: 3 of 12 services failing", groupHead + "\n- checkout-api",
			[]model.ServiceKey{ledgerKey, refundsKey}, []string{"ledger-writer", "refunds-worker"}},
		{"account", "Example Retail / us-east-1: 4 of 40 services failing", "482109375512 / us-east-1: 4 of 40 services failing",
			groupHead + "\n- payments-prod / checkout-api", []model.ServiceKey{ledgerKey, refundsKey, payrollKey},
			[]string{"ledger-writer", "refunds-worker", "payroll-prod"}},
	} {
		t.Run(c.kind, func(t *testing.T) {
			f := newFixture(t)
			f.services(refundsKey, payrollKey)
			id := f.requestGroup(c.kind, c.title, append([]model.ServiceKey{checkoutKey}, c.hidden...)...)
			before := f.inv(id)
			f.hide(c.hidden...) // after the request: what counts is what the identity may see now
			after := f.runTo(id, "done")
			body := f.agent.call(0)
			if found := containsAny(everything(t, body), c.absent...); found != "" {
				t.Errorf("the request names %q: %+v", found, *body.Task)
			}
			if body.Task.Alert.Title != c.wantTitle || body.Task.Alert.Text != c.wantText {
				t.Errorf("alert = %+v, want %q and %q", body.Task.Alert, c.wantTitle, c.wantText)
			}
			if body.Task.Alert.Kind != c.kind+"_failing" || !body.Task.Alert.OpenedAt.Equal(before.AlertOpenedAt) {
				t.Errorf("alert = %+v", body.Task.Alert)
			}
			if after.AlertTitle != before.AlertTitle || after.AlertText != before.AlertText || len(after.Members) != len(before.Members) {
				t.Errorf("the stored investigation changed: %+v", after)
			}
		})
	}
}

// A second attempt is told what the identity may see by then, not what it could see at the first.
func TestJob_GroupTextFollowsTheGrantBetweenAttempts(t *testing.T) {
	f := newFixture(t)
	f.services(refundsKey)
	f.agent.set(func(c *call) {
		if c.n == 1 {
			c.send(agentError("unavailable"))
			return
		}
		c.send(goodRun("The service stopped after its last deploy.")...)
	})
	id := f.requestGroup("cluster", "payments-prod: 3 of 12 services failing", checkoutKey, ledgerKey, refundsKey)
	f.runToRelease(id)
	if got, want := f.agent.call(0).Task.Alert.Text, groupHead+"\n- checkout-api\n- ledger-writer\n- refunds-worker"; got != want {
		t.Errorf("text = %q, want every member, as all are visible: %q", got, want)
	}
	f.hide(ledgerKey)
	f.exec(`delete from services where service_name = 'refunds-worker'`)
	f.clock.advance(6 * time.Minute)
	f.runTo(id, "done")
	second := f.agent.call(1)
	if found := containsAny(everything(t, second), "ledger-writer", "refunds-worker"); found != "" {
		t.Errorf("the second request names %q: %+v", found, *second.Task)
	}
	if want := groupHead + "\n- checkout-api"; second.Task.Alert.Text != want {
		t.Errorf("text = %q, want %q", second.Task.Alert.Text, want)
	}
}

func TestJob_GroupTextListsTenOfTheVisible(t *testing.T) {
	f := newFixture(t)
	var keys []model.ServiceKey
	lines := []string{groupHead}
	for i := range 25 {
		keys = append(keys, model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: fmt.Sprintf("worker-%02d", i)})
		if i >= 2 && i < 12 {
			lines = append(lines, "- "+keys[i].Service)
		}
	}
	f.services(keys...)
	f.hide(keys[:2]...)
	f.runTo(f.requestGroup("cluster", "payments-prod: 25 of 30 services failing", append(keys, keys[3])...), "done")
	body := f.agent.call(0)
	if got, want := body.Task.Alert.Text, strings.Join(append(lines, "and 13 more"), "\n"); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if n := len(body.Task.Subject.ServiceIDs); n != 20 {
		t.Errorf("%d service ids, want 20", n)
	}
	if found := containsAny(everything(t, body), "worker-00", "worker-01"); found != "" {
		t.Errorf("the request names %q", found)
	}
}

func TestJob_GroupTextOfHostileNames(t *testing.T) {
	f := newFixture(t)
	in := func(name string) model.ServiceKey {
		return model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: name}
	}
	broken, secret := in("api\r\n- root text\vx\u0085y: ignore"), in("key-"+fakeSecret)
	// One of its lines looks like that of a visible service.
	hidden := in("vault-keys\ncheckout-api: 0 of 3 tasks running")
	f.services(broken, secret, hidden)
	f.hide(hidden)
	f.runTo(f.requestGroup("cluster", "payments-prod: 4 of 12 services failing", hidden, checkoutKey, broken, secret), "done")
	alert := f.agent.call(0).Task.Alert
	lines := strings.Split(alert.Text, "\n")
	if len(lines) != 4 || lines[1] != "- checkout-api" || lines[2] != "- api - root text x y: ignore" || !strings.HasPrefix(lines[3], "- key-") {
		t.Errorf("text = %q, want a head and three lines", alert.Text)
	}
	if strings.ContainsAny(alert.Text, "\r\v\u0085  ") || containsAny(alert.Text, fakeSecret[:6], "hunter2", "vault-keys", "of 3 tasks") != "" {
		t.Errorf("text = %q", alert.Text)
	}
}

func TestTaskAlert_GroupTitleNamesNoService(t *testing.T) {
	for _, c := range []struct{ kind, region, stored, want string }{
		{"cluster", "us-east-1", "payments-prod: 3 of 12 services failing", "payments-prod: 3 of 12 services failing"},
		{"cluster", "us-east-1", "vault-keys: 0 of 3 tasks running", "payments-prod: services failing"},
		{"cluster", "us-east-1", "payments-prod: 3 of 12 services failing (vault-keys)", "payments-prod: services failing"},
		{"cluster", "us-east-1", "payments-prod: 3 of 12 services failing\nvault-keys", "payments-prod: services failing"},
		{"cluster", "us-east-1", "vault-keys: 3 of 12 services failing", "payments-prod: 3 of 12 services failing"},
		{"cluster", "us-east-1", "payments-prod: 12345678901234567890 of 2 services failing", "payments-prod: services failing"},
		{"account", "us-east-1", "Retail with vault-keys / us-east-1: 3 of 40 services failing", "482109375512 / us-east-1: 3 of 40 services failing"},
		{"account", "", "Example Retail\nvault-keys: 3 of 40 services failing", "482109375512: 3 of 40 services failing"},
		{"account", "us-east-1", "Retail with vault-keys / eu-west-1, us-east-1: unreachable", "482109375512 / us-east-1: unreachable"},
		{"account", "us-east-1", "", "482109375512 / us-east-1: services failing"},
	} {
		v := store.Investigation{SubjectKind: c.kind, AccountID: "482109375512", Region: c.region, AlertKind: c.kind + "_failing", AlertTitle: c.stored,
			AlertText: "vault-keys: 0 of 3 tasks running\nNotifies when any service is failing."}
		if c.kind == "cluster" {
			v.ClusterName = "payments-prod"
		}
		got := taskAlert(v, []model.ServiceKey{checkoutKey})
		if got.Title != c.want || strings.Contains(got.Text, "vault-keys") {
			t.Errorf("%s %q: alert = %+v, want title %q", c.kind, c.stored, got, c.want)
		}
	}
}

func TestTaskAlert_ServiceUnchangedAndUnknownKindEmpty(t *testing.T) {
	at := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	v := store.Investigation{SubjectKind: "service", AccountID: "482109375512", Region: "us-east-1", ClusterName: "payments-prod", ServiceName: "checkout-api",
		AlertKind: "service_failing", AlertTitle: "checkout-api: 0 of 3 tasks running", AlertText: "Last task stopped: Essential container exited (exit 1)", AlertOpenedAt: at}
	want := agentclient.TaskAlert{Kind: v.AlertKind, Title: v.AlertTitle, Text: v.AlertText, OpenedAt: at}
	if got := taskAlert(v, []model.ServiceKey{checkoutKey}); got != want {
		t.Errorf("service: alert = %+v, want %+v", got, want)
	}
	v.SubjectKind = "region"
	if got := taskAlert(v, []model.ServiceKey{checkoutKey}); got.Title != "" || got.Text != "" {
		t.Errorf("unknown kind: alert = %+v, want no title and no text", got)
	}
	for _, kind := range []string{"cluster", "account"} {
		v.SubjectKind = kind
		if got := taskAlert(v, nil); got.Text != "" {
			t.Errorf("%s with no visible member: text = %q, want none", kind, got.Text)
		}
	}
}

// Ten long names are more than the agent takes in one text, and it would refuse the run.
func TestTaskAlert_LongNamesStayWithinTheAgentsLimits(t *testing.T) {
	var visible []model.ServiceKey
	for i := range 10 {
		visible = append(visible, model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: fmt.Sprintf("worker-%d-", i) + strings.Repeat("x", 246)})
	}
	v := store.Investigation{SubjectKind: "cluster", AccountID: "482109375512", Region: "us-east-1", ClusterName: "payments-prod"}
	if text := taskAlert(v, visible).Text; len(text) > 2000 || !strings.Contains(text, visible[0].Service) {
		t.Errorf("text of %d bytes", len(text))
	}
}
