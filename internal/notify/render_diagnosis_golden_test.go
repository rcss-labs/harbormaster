package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiagnosis_RendersUnchanged pins the bytes of the diagnosis message per destination. The files were generated
// from the code as it was before the review message shared its builder.
func TestDiagnosis_RendersUnchanged(t *testing.T) {
	summaries := map[string]string{
		"plain":   "The task ran out of memory.\nRestart it.",
		"hostile": "See https://evil.example/x and https://hm.example.com/investigations/77 <!channel> & </pre> @here",
		"long":    strings.Repeat("故障 ", 700),
	}
	render := map[string]func(Message) ([]byte, error){
		"slack":    SlackBody,
		"telegram": func(m Message) ([]byte, error) { return TelegramBody(m, "-100123", 7) },
		"email": func(m Message) ([]byte, error) {
			subject, body := Email(m, "hm@example.com", []string{"ops@example.com"})
			return append([]byte(subject+"\n"), body...), nil
		},
		"webhook": WebhookBody,
	}
	for dest, fn := range render {
		for variant, summary := range summaries {
			name := "diagnosis_" + dest + "_" + variant + ".golden"
			path := filepath.Join("testdata", "golden", name)
			got, err := fn(diagnosisMessage(summary))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if *updateGolden {
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
