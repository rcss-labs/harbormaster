package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

func TestLoop_TrimsOldestTurnsAndSaysSo(t *testing.T) {
	var history []provider.Message
	for i := 0; i < 8; i++ {
		history = append(history,
			provider.Message{Role: provider.RoleUser, Text: fmt.Sprintf("question %d %s", i, strings.Repeat("x", 20_000))},
			provider.Message{Role: provider.RoleAssistant, Text: fmt.Sprintf("answer %d", i)},
		)
	}
	history = append(history, provider.Message{Role: provider.RoleUser, Text: "latest question"})

	p := fake.New(
		fake.ToolCalls(provider.Usage{InputTokens: 100, OutputTokens: 5}, overviewCall("c1")),
		fake.Text("Done.", provider.Usage{InputTokens: 100, OutputTokens: 5}),
	)
	in := baseInput()
	in.Messages = history
	in.Model.ContextTokens = 40_000 // 80% is 32,000 tokens; the history alone is about 40,000
	events, err := run(t, newLoop(p, &recordingRunner{}), in)
	if err != nil {
		t.Fatal(err)
	}

	notices := 0
	for _, e := range events {
		if e.Type == "notice" && e.Notice == "context_trimmed" {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("%d context_trimmed notices, want 1 across both model calls", notices)
	}
	reqs := p.Requests()
	first := reqs[0].Messages
	if len(first) >= len(history) || first[len(first)-1].Text != "latest question" {
		t.Fatalf("first request has %d of %d messages, last %q", len(first), len(history), first[len(first)-1].Text)
	}
	if !strings.Contains(first[0].Text, "question") || strings.Contains(first[0].Text, "question 0 ") {
		t.Errorf("the oldest turn should go first, request starts with %.30q", first[0].Text)
	}
	if first[0].Role != provider.RoleUser {
		t.Errorf("a trimmed history must start at a user message, got %s", first[0].Role)
	}
	if n := len(reqs[1].Messages); n < len(first) {
		t.Errorf("second request has %d messages, fewer than the first's %d", n, len(first))
	}
}

func TestTrimTurns_KeepsTheLatestTurnWhateverItsSize(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "old"},
		{Role: provider.RoleAssistant, Text: "older answer"},
		{Role: provider.RoleUser, Text: strings.Repeat("y", 100_000)},
	}
	got, trimmed := trimTurns(msgs, func(m []provider.Message) int64 { return int64(len(m)) * 1_000_000 }, 10)
	if !trimmed || len(got) != 1 || got[0].Role != provider.RoleUser || len(got[0].Text) != 100_000 {
		t.Errorf("trimTurns = %d messages, trimmed %v", len(got), trimmed)
	}
}
