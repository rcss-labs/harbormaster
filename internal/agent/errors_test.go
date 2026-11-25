package agent

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

// The words users read for a provider failure come from one table; what the adapter wrote in Message never reaches them.
func TestProviderMessage_OurSentencePerKind(t *testing.T) {
	cases := map[string]string{
		"auth":             "The model provider rejected the agent's credentials. Ask an operator to check them.",
		"rate_limited":     "The model provider is busy. Try again shortly.",
		"unavailable":      "The model provider is unavailable. Try again shortly.",
		"context_too_long": "This conversation is too long for the model. Start a new one.",
		"invalid_request":  "The model could not answer this.",
	}
	for kind, want := range cases {
		if got := providerMessage(&provider.Error{Kind: kind, Message: "vendor words"}); got != want {
			t.Errorf("%s: %q, want %q", kind, got, want)
		}
	}
	if got := providerMessage(&provider.Error{Kind: "something_else"}); got != cases["unavailable"] {
		t.Errorf("an unknown kind reads as unavailable, got %q", got)
	}
	refusal := &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model declined to answer."}
	if got := providerMessage(refusal); got != "The model declined to answer." {
		t.Errorf("refusal: %q", got)
	}
}

func TestLoop_LogsOneLinePerFailedModelCall(t *testing.T) {
	var buf bytes.Buffer
	p := fake.New(fake.Fail(rateLimited()), fake.Fail(&provider.Error{Kind: "auth", Status: 401, VendorType: "authentication_error", Message: "VENDOR-WORDS-NOT-FOR-USERS"}))
	l := newLoop(p, &recordingRunner{})
	l.Log = slog.New(slog.NewTextHandler(&buf, nil))
	events, err := run(t, l, baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Type != "error" || last.Code != "auth" {
		t.Fatalf("last event = %+v", last)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want 2 (one per failed call):\n%s", len(lines), buf.String())
	}
	for i, want := range []string{"kind=rate_limited status=429 vendor_type=rate_limit_error", "kind=auth status=401 vendor_type=authentication_error"} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], "model="+testModel.ID) || !strings.Contains(lines[i], "provider=") {
			t.Errorf("line %d = %s, want %s with provider and model", i, lines[i], want)
		}
	}
	if strings.Contains(buf.String(), "VENDOR-WORDS") {
		t.Errorf("the vendor's words reached the log:\n%s", buf.String())
	}
}

func TestLoop_VendorTypeThatIsNotAnIdentifierIsLeftOut(t *testing.T) {
	var buf bytes.Buffer
	p := fake.New(fake.Fail(&provider.Error{Kind: "invalid_request", Status: 400, VendorType: "see account page for details"}))
	l := newLoop(p, &recordingRunner{})
	l.Log = slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := run(t, l, baseInput()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "vendor_type") || strings.Contains(buf.String(), "account page") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestLoop_RunThatRanOutOfTimeBeforeStartingSaysSo(t *testing.T) {
	p := fake.New(fake.Text("never", provider.Usage{}))
	events, err := run(t, newLoop(p, &recordingRunner{}, func(l *Limits) { l.Timeout = time.Nanosecond }), baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if got := joinedText(events); got != "The run ran out of time before it could start." {
		t.Errorf("text = %q, events %v", got, eventTypes(events))
	}
	if len(p.Requests()) != 0 {
		t.Error("a call was made")
	}
}
