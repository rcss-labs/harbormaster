package agentresult

import (
	"strings"
	"testing"
	"time"
	"unicode"
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

func TestCheck_RefusesInvisibleAndControlCharacters(t *testing.T) {
	tricks := map[string]string{
		"NUL": "a\x00b", "escape": "a\x1bb", "carriage return": "a\rb", "vertical tab": "a\vb", "form feed": "a\fb", "DEL": "a\x7fb",
		"C1 control": "a\u0085b", "zero width space": "a\u200bb", "zero width joiner": "a\u200db", "bidi override": "a\u202eb",
		"bidi isolate": "a\u2066b", "word joiner": "a\u2060b", "BOM": "a\ufeffb", "soft hyphen": "a\u00adb",
		"combining grapheme joiner": "a\u034fb", "hangul choseong filler": "a\u115fb", "hangul jungseong filler": "a\u1160b",
		"hangul filler": "a\u3164b", "halfwidth hangul filler": "a\uffa0b", "variation selector": "a\ufe0fb",
		"variation selector supplement": "a\U000e0100b", "tag character": "a\U000e0041b", "tag begin": "a\U000e0001b",
		"mongolian selector": "a\u180bb", "invalid UTF-8": "a\xffb", "truncated rune": "a\xe2\x82",
	}
	corpus := corpusOf("unrelated")
	for name, text := range tricks {
		if got, replaced := CheckSummary(text, corpus); !replaced || got != ReplacementSummary {
			t.Errorf("summary with %s: got %q, replaced %v", name, got, replaced)
		}
		if got, replaced := RecheckSummary(text); !replaced || got != ReplacementSummary {
			t.Errorf("recheck of a summary with %s: got %q, replaced %v", name, got, replaced)
		}
		if name == "vertical tab" || name == "form feed" || name == "C1 control" || name == "carriage return" {
			continue // line breaks in a note, flattened below
		}
		if got := CheckNote(text, corpus); got != ReplacementNote {
			t.Errorf("note with %s: got %q", name, got)
		}
		if got := RecheckNote(text); got != ReplacementNote {
			t.Errorf("recheck of a note with %s: got %q", name, got)
		}
	}
	// Line feed, tab, and the unusual line separators are ordinary.
	const plain = "line one\nline two\tcolumn\u2028next\u2029last"
	if got, replaced := CheckSummary(plain, corpus); replaced || got != plain {
		t.Errorf("ordinary whitespace refused: %q, replaced %v", got, replaced)
	}
}

func TestCheckNote_FlattensEveryLineBreak(t *testing.T) {
	in := "a\nb\rc\r\nd\ve\ff\u0085g\u2028h\u2029i"
	want := "a b c d e f g h i"
	if got := CheckNote(in, corpusOf("unrelated")); got != want {
		t.Errorf("CheckNote: %q", got)
	}
	if got := RecheckNote(in); got != want {
		t.Errorf("RecheckNote: %q", got)
	}
}

func TestCheckNote_RefusesFullCorpusAndImages(t *testing.T) {
	full := &Corpus{}
	full.Add(strings.Repeat("x", CorpusCap+1))
	if got := CheckNote("a harmless note", full); got != ReplacementNote {
		t.Errorf("full corpus: %q", got)
	}
	for _, note := range []string{"see ![x](y)", "<IMG src=x>", "<svg onload=x>", "<picture>", "<video src=x>", "<iframe src=x>", "<object data=x>", "<embed src=x>"} {
		if got := CheckNote(note, corpusOf("unrelated")); got != ReplacementNote {
			t.Errorf("note %q: %q", note, got)
		}
		if got, replaced := CheckSummary(note, corpusOf("unrelated")); !replaced || got != ReplacementSummary {
			t.Errorf("summary %q: %q", note, got)
		}
	}
	if got, replaced := CheckSummary("a < b and a <b> c", corpusOf("unrelated")); replaced {
		t.Errorf("ordinary angle brackets refused: %q", got)
	}
}

func TestCheckSummary_RefusesALateCopy(t *testing.T) {
	line := "Ошибка соединения с базой данных: тайм-аут при чтении ответа сервера"
	corpus := corpusOf("header\n" + line + "\nfooter")
	summary := words(9, 500) + " " + line
	if got, replaced := CheckSummary(summary, corpus); !replaced || got != ReplacementSummary {
		t.Errorf("a copy after character 400 passed: replaced %v", replaced)
	}
	if got, replaced := CheckSummary(words(9, 500), corpus); replaced {
		t.Errorf("unrelated text refused: %q", got)
	}
}

func TestCutExcerpt_ReplacesControlBidiAndTagCharactersWithSpaces(t *testing.T) {
	in := "a\u0000b\u001bc\u007fd\u202ee\u2067f\u200eg\u061ch\U000e0041i\U000e007fj\nk\tl"
	if got, want := CutExcerpt(in), "a b c d e f g h i j\nk\tl"; got != want {
		t.Errorf("CutExcerpt(%q) = %q, want %q", in, got, want)
	}
	// Nothing joins across a dropped character.
	if got := CutExcerpt("ev\u202eil"); got != "ev il" {
		t.Errorf("joined: %q", got)
	}
	// A replacement counts against the cap, so the result stays inside it.
	padded := strings.Repeat("a\u0000", ExcerptCap)
	if got := CutExcerpt(padded); len(got) > ExcerptCap || !strings.HasSuffix(got, CutMarker) || strings.ContainsRune(got, 0) {
		t.Errorf("padded: %d bytes", len(got))
	}
}

func TestCheck_RefusesEveryOtherDefaultIgnorableRune(t *testing.T) {
	corpus := corpusOf("unrelated")
	var runes []rune
	for r := rune(0xFFF0); r <= 0xFFF8; r++ {
		runes = append(runes, r)
	}
	runes = append(runes, 0x2065)
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) {
			runes = append(runes, r)
		}
	}
	for _, r := range runes {
		text := "a" + string(r) + "b"
		if _, replaced := CheckSummary(text, corpus); !replaced {
			t.Errorf("U+%04X passed the summary check", r)
		}
		if _, replaced := RecheckSummary(text); !replaced {
			t.Errorf("U+%04X passed the repeat", r)
		}
		if got := RecheckNote(text); got != ReplacementNote {
			t.Errorf("U+%04X passed the note repeat", r)
		}
	}
}

func TestChecksAndCut_LinearOnLargeInput(t *testing.T) {
	inputs := map[string]func(n int) string{
		"one word":     func(n int) string { return strings.Repeat("a", n) },
		"words":        func(n int) string { return words(11, n) },
		"controls":     func(n int) string { return strings.Repeat("a\x00\u202e", n/5) },
		"invalid":      func(n int) string { return strings.Repeat("\xff", n) },
		"multi-byte":   func(n int) string { return strings.Repeat("€", n/3) },
		"scrub-shaped": func(n int) string { return strings.Repeat("password=aB3-", n/13) },
		"line breaks":  func(n int) string { return strings.Repeat("a\r\n", n/3) },
		"image-ish":    func(n int) string { return strings.Repeat("<i", n/2) },
	}
	corpus := corpusOf(words(12, 300_000))
	for name, build := range inputs {
		small, big := build(25_000), build(200_000)
		run := func(text string) func() {
			return func() {
				CheckSummary(text, corpus)
				CheckNote(text, corpus)
				RecheckSummary(text)
				RecheckNote(text)
				CutExcerpt(text)
			}
		}
		ts, tb := timeOf(run(small)), timeOf(run(big))
		if tb > 30*ts+20*time.Millisecond || tb > 2*time.Second {
			t.Errorf("%s: 200 KB took %v against %v for 25 KB", name, tb, ts)
		}
		if got, replaced := CheckSummary(big, corpus); !replaced || got != ReplacementSummary {
			t.Errorf("%s: a 200 KB summary passed", name)
		}
		if got := CutExcerpt(big); len(got) > ExcerptCap || !utf8.ValidString(got) {
			t.Errorf("%s: excerpt %d bytes, valid %v", name, len(got), utf8.ValidString(got))
		}
	}
}
