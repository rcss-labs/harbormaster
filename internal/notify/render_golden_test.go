package notify

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden files")

// goldenFixtures are the message shapes whose bytes are pinned. The files are generated from the code as it was
// before the diagnosis work, never from the code under test.
func goldenFixtures() map[string]func(Event) Message {
	return map[string]func(Event) Message{
		"service": testMessage,
		"warning": func(e Event) Message {
			m := testMessage(e)
			m.Subject.Severity, m.Subject.Kind, m.Subject.Title = "warning", "service_degraded", "api: degraded <&>"
			return m
		},
		"empty": func(e Event) Message {
			m := testMessage(e)
			m.Subject.Text = ""
			return m
		},
		"account": func(e Event) Message {
			m := testMessage(e)
			m.Subject.Key = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1"}}
			m.Subject.Kind, m.Subject.Title, m.Subject.Text = "account_unreachable", "Account unreachable", "Access was denied."
			return m
		},
		"group": func(e Event) Message {
			m := testMessage(e)
			m.Subject.Scope, m.Subject.RouteID, m.Subject.Kind = "cluster", 5, KindClusterFailing
			m.Subject.Key = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}}
			m.Subject.Title, m.Subject.Text = "core: 2 of 4 services failing", "Several services are failing."
			m.Subject.Threshold, m.Subject.Failing, m.Subject.Total = 2, 2, 4
			m.Subject.Members = []store.IncidentMember{
				{Cluster: "core", Service: "api", Kind: "service_failing", Title: "api: failing", Critical: true},
				{Cluster: "core", Service: "web", Kind: "restart_loop", Title: "web: restarting"},
			}
			return m
		},
	}
}

// TestExistingEvents_RenderUnchanged pins the bytes of every destination's Opened, Repeat, Update, Resolved and
// Test messages, so the diagnosis work cannot change what already goes out.
func TestExistingEvents_RenderUnchanged(t *testing.T) {
	events := []Event{EventOpened, EventRepeat, EventUpdate, EventResolved, EventTest}
	render := map[string]func(Message) ([]byte, error){
		"slack":    SlackBody,
		"telegram": func(m Message) ([]byte, error) { return TelegramBody(m, "-100123", 7) },
		"email": func(m Message) ([]byte, error) {
			subject, body := Email(m, "hm@example.com", []string{"ops@example.com"})
			return append([]byte(subject+"\n"), body...), nil
		},
		"webhook":   WebhookBody,
		"pagerduty": func(m Message) ([]byte, error) { return PagerDutyBody(m, "fake-routing-key") },
	}
	for dest, fn := range render {
		for variant, build := range goldenFixtures() {
			for _, event := range events {
				name := dest + "_" + variant + "_" + string(event) + ".golden"
				if variant == "service" {
					name = dest + "_" + string(event) + ".golden" // the first fixtures keep their names
				}
				path := filepath.Join("testdata", "golden", name)
				got, err := fn(build(event))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s changed:\n got: %s\nwant: %s", name, got, want)
				}
			}
		}
	}
}
