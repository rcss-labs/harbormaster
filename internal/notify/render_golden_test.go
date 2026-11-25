package notify

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden files")

// TestExistingEvents_RenderUnchanged pins the bytes of every destination's Opened, Repeat, Update, Resolved and
// Test messages, so the diagnosis work cannot change what already goes out.
func TestExistingEvents_RenderUnchanged(t *testing.T) {
	events := []Event{EventOpened, EventRepeat, EventUpdate, EventResolved, EventTest}
	render := map[string]func(Message) []byte{
		"slack": func(m Message) []byte { b, _ := SlackBody(m); return b },
		"telegram": func(m Message) []byte {
			b, _ := TelegramBody(m, "-100123", 7)
			return b
		},
		"email": func(m Message) []byte {
			subject, body := Email(m, "hm@example.com", []string{"ops@example.com"})
			return append([]byte(subject+"\n"), body...)
		},
		"webhook":   func(m Message) []byte { b, _ := WebhookBody(m); return b },
		"pagerduty": func(m Message) []byte { b, _ := PagerDutyBody(m, "fake-routing-key"); return b },
	}
	for dest, fn := range render {
		for _, event := range events {
			name := dest + "_" + string(event) + ".golden"
			path := filepath.Join("testdata", "golden", name)
			got := fn(testMessage(event))
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
