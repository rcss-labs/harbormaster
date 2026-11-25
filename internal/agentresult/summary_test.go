package agentresult

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const copiedLog = "2026-11-24T10:15:02Z payments-api ERROR upstream connect error or disconnect/reset before headers, reset reason: connection failure"

func corpusOf(texts ...string) *Corpus {
	c := &Corpus{}
	for _, text := range texts {
		c.Add(text)
	}
	return c
}

func TestCheckSummary_ReplacesCopiedLogText(t *testing.T) {
	corpus := corpusOf(copiedLog)
	summary := "The service failed. It logged: " + copiedLog + " Restart it."
	got, replaced := CheckSummary(summary, corpus)
	if !replaced || got != ReplacementSummary {
		t.Fatalf("got %q, replaced %v", got, replaced)
	}
}

func TestCheckSummary_ReplacesCopiedEnvironmentValue(t *testing.T) {
	corpus := corpusOf(`{"env":{"DATABASE_HOST":"payments-db-primary.internal.example.net","LOG_LEVEL":"info"}}`)
	got, replaced := CheckSummary("It connects to payments-db-primary.internal.example.net which is down.", corpus)
	if !replaced || got != ReplacementSummary {
		t.Fatalf("got %q, replaced %v", got, replaced)
	}
}

func TestCheckSummary_ReplacesWhenOverLength(t *testing.T) {
	corpus := corpusOf("nothing relevant")
	ok := strings.Repeat("é", SummaryCap)
	if got, replaced := CheckSummary(ok, corpus); replaced || got != ok {
		t.Errorf("1200 runes: replaced %v", replaced)
	}
	if got, replaced := CheckSummary(ok+"é", corpus); !replaced || got != ReplacementSummary {
		t.Errorf("1201 runes: got %d bytes, replaced %v", len(got), replaced)
	}
}

func TestCheckSummary_ReplacesWhenTheScrubberWouldChangeIt(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	got, replaced := CheckSummary("The task used key "+secret+" to call the API.", corpusOf("x"))
	if !replaced || got != ReplacementSummary {
		t.Fatalf("got %q, replaced %v", got, replaced)
	}
}

func TestCheckSummary_ReplacesWhenTheCorpusIsMissingOrFull(t *testing.T) {
	const fine = "The checkout service was restarted by a deploy."
	if got, replaced := CheckSummary(fine, nil); !replaced || got != ReplacementSummary {
		t.Errorf("nil corpus: got %q, replaced %v", got, replaced)
	}
	full := &Corpus{}
	full.Add(strings.Repeat("x", CorpusCap+1))
	if got, replaced := CheckSummary(fine, full); !replaced || got != ReplacementSummary {
		t.Errorf("full corpus: got %q, replaced %v", got, replaced)
	}
	for _, empty := range []string{"", " \n\t "} {
		if got, replaced := CheckSummary(empty, corpusOf("x")); !replaced || got != ReplacementSummary {
			t.Errorf("empty %q: got %q, replaced %v", empty, got, replaced)
		}
	}
	for _, markup := range []string{"See ![chart](https://x.example/a.png) here", "See <IMG src=x> here", "<img\nsrc=x>"} {
		if got, replaced := CheckSummary(markup, corpusOf("x")); !replaced || got != ReplacementSummary {
			t.Errorf("image markup %q: got %q, replaced %v", markup, got, replaced)
		}
	}
}

func TestCheckSummary_KeepsAnOrdinaryConclusion(t *testing.T) {
	corpus := corpusOf(copiedLog, `{"status":"OOMKilled","exitCode":137}`)
	summary := "The checkout-api service was restarted several times. Its last container exit code was 137 with reason OOMKilled, so the memory limit is too low for current traffic."
	got, replaced := CheckSummary(summary, corpus)
	if replaced || got != summary {
		t.Fatalf("got %q, replaced %v", got, replaced)
	}
}

func TestCheckNote_ReplacesAndFlattens(t *testing.T) {
	corpus := corpusOf(copiedLog)
	if got := CheckNote("first line\nsecond line\r\nthird", corpus); got != "first line second line third" {
		t.Errorf("flatten: %q", got)
	}
	if got := CheckNote("copied "+copiedLog, corpus); got != ReplacementNote {
		t.Errorf("copy: %q", got)
	}
	if got := CheckNote(strings.Repeat("n", NoteCap+1), corpus); got != ReplacementNote {
		t.Errorf("over 300: %q", got)
	}
	if got := CheckNote(strings.Repeat("n", NoteCap), corpus); got != strings.Repeat("n", NoteCap) {
		t.Errorf("300 runes was refused")
	}
	if got := CheckNote("ok", nil); got != ReplacementNote {
		t.Errorf("nil corpus: %q", got)
	}
	if got := CheckNote(" \n ", corpus); got != ReplacementNote {
		t.Errorf("blank: %q", got)
	}
}

func TestRecheckSummary_CapAndScrubOnly(t *testing.T) {
	// No corpus: a copy-looking summary passes the repeat, which cannot see the logs.
	if got, replaced := RecheckSummary(copiedLog); replaced || got != copiedLog {
		t.Errorf("repeat refused text it has no corpus for: replaced %v", replaced)
	}
	if got, replaced := RecheckSummary(strings.Repeat("a", SummaryCap+1)); !replaced || got != ReplacementSummary {
		t.Errorf("over cap: replaced %v", replaced)
	}
	if got, replaced := RecheckSummary("key " + "AKIA" + "IOSFODNN7EXAMPLE"); !replaced || got != ReplacementSummary {
		t.Errorf("secret: got %q, replaced %v", got, replaced)
	}
	if got, replaced := RecheckSummary(" "); !replaced || got != ReplacementSummary {
		t.Errorf("empty: replaced %v", replaced)
	}
	if got := RecheckNote(strings.Repeat("n", NoteCap+1)); got != ReplacementNote {
		t.Errorf("note over cap: %q", got)
	}
	if got := RecheckNote("two\nlines"); got != "two lines" {
		t.Errorf("note flatten: %q", got)
	}
	if got := RecheckNote("key " + "AKIA" + "IOSFODNN7EXAMPLE"); got != ReplacementNote {
		t.Errorf("note secret: %q", got)
	}
}

func TestCutExcerpt_RuneBoundaryMarkerAndCap(t *testing.T) {
	short := "short excerpt"
	if got := CutExcerpt(short); got != short {
		t.Errorf("short text changed: %q", got)
	}
	exact := strings.Repeat("a", ExcerptCap)
	if got := CutExcerpt(exact); got != exact {
		t.Errorf("text at the cap was cut")
	}
	// Every shift of a three-byte rune across the cut point.
	for pad := 0; pad < 4; pad++ {
		text := strings.Repeat("a", ExcerptCap-len(CutMarker)-2+pad) + strings.Repeat("€", 20)
		got := CutExcerpt(text)
		if !utf8.ValidString(got) || len(got) > ExcerptCap || !strings.HasSuffix(got, CutMarker) {
			t.Errorf("pad %d: valid %v, %d bytes, marker %v", pad, utf8.ValidString(got), len(got), strings.HasSuffix(got, CutMarker))
		}
		if !strings.HasPrefix(text, strings.TrimSuffix(got, CutMarker)) {
			t.Errorf("pad %d: the kept part is not a prefix", pad)
		}
	}
	if got := CutExcerpt(strings.Repeat("€", 2000)); len(got) > ExcerptCap || !utf8.ValidString(got) {
		t.Errorf("multi-byte text: %d bytes", len(got))
	}
	if got := CutExcerpt(strings.Repeat("\xff", 2000)); len(got) > ExcerptCap || !utf8.ValidString(got) {
		t.Errorf("invalid bytes: %d bytes", len(got))
	}
}
