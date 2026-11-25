package agentresult

import (
	"strings"
	"testing"
	"time"
)

func TestNormalize_Table(t *testing.T) {
	cases := map[string]string{
		"Connection   timed-out!":     "connection timed out",
		"connection timed out":        "connection timed out",
		"  --Leading and trailing-- ": "leading and trailing",
		"":                            "",
		"!!!":                         "",
		"ÉCOLE 42\tx\ny":              "école 42 x y",
		"a_b.c":                       "a b c",
		"bad\xffbyte":                 "bad byte",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

const sharedRun = "the upstream pool refused connection number one"

func TestCorpus_KeepsJSONValuesNotKeys(t *testing.T) {
	var c Corpus
	c.Add(`{"a_very_long_key_name_that_is_over_thirty_two_characters":"short","nested":{"list":["` + sharedRun + `"]},"n":5}`)
	if !c.HasRunIn("x " + sharedRun + " y") {
		t.Error("a string value inside nested JSON was not kept")
	}
	if c.HasRunIn("a very long key name that is over thirty two characters") {
		t.Error("a JSON key was kept as if it were a value")
	}
	// Two values never join into one run.
	var d Corpus
	d.Add(`["aaaaaaaaaaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbbbbbbbbbb"]`)
	if d.HasRunIn("aaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Error("two separate values were joined into one run")
	}
	// Anything that is not JSON is kept whole.
	var e Corpus
	e.Add("plain log line: " + sharedRun)
	if !e.HasRunIn(sharedRun) {
		t.Error("plain text was not kept")
	}
}

func TestCorpus_FullPastTheCap(t *testing.T) {
	var c Corpus
	c.Add(strings.Repeat("a", CorpusCap/2))
	c.Add(strings.Repeat("b", CorpusCap/2))
	if c.Full() {
		t.Fatal("full at exactly the cap")
	}
	c.Add("c")
	if !c.Full() {
		t.Fatal("not full past the cap")
	}
	var d Corpus
	d.Add(strings.Repeat("x", CorpusCap+1))
	if !d.Full() {
		t.Fatal("one oversized text did not fill the corpus")
	}
}

func TestHasRunIn_ExactBoundary(t *testing.T) {
	log := "abcdefghijklmnopqrstuvwxyz0123456789"
	var c Corpus
	c.Add(log)
	if c.HasRunIn("zz " + log[:VerbatimRun-1] + " zz") {
		t.Error("31 shared characters were refused")
	}
	if !c.HasRunIn("zz " + log[:VerbatimRun] + " zz") {
		t.Error("32 shared characters passed")
	}
	if !c.HasRunIn(log[4 : 4+VerbatimRun]) {
		t.Error("a run from the middle of the corpus passed")
	}
}

func TestHasRunIn_IgnoresCaseSpacingAndPunctuation(t *testing.T) {
	var c Corpus
	c.Add("ERROR: Connection   timed-out while reading from upstream-db-1 (code=57)")
	if !c.HasRunIn("It said connection timed out while reading from upstream db 1, code 57.") {
		t.Error("a re-cased, re-punctuated copy passed")
	}
}

// ratio runs f at one size and at factor times that size and returns how much longer the larger took, best of a few.
func timeOf(f func()) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < 3; i++ {
		start := time.Now()
		f()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

func TestHasRunIn_LinearOnLargeCorpus(t *testing.T) {
	// Candidate windows all contain a 'b', so no 32-run exists in a corpus of one repeated character: every window
	// of the corpus has to be compared and none may match.
	candidate := strings.Repeat(strings.Repeat("a", 31)+"b", SummaryCap/32)
	for _, unit := range []string{"a", "ab", "abcdefg hijk"} {
		small, big := &Corpus{}, &Corpus{}
		small.Add(strings.Repeat(unit, CorpusCap/8/len(unit)))
		big.Add(strings.Repeat(unit, CorpusCap/len(unit)))
		if big.Full() {
			t.Fatalf("corpus of %q full", unit)
		}
		var hit bool
		ts := timeOf(func() { hit = small.HasRunIn(candidate) })
		tb := timeOf(func() { hit = hit || big.HasRunIn(candidate) })
		if hit {
			t.Errorf("unit %q: a run was found that is not there", unit)
		}
		// Linear grows 8x; quadratic 64x.
		if tb > 30*ts+5*time.Millisecond {
			t.Errorf("unit %q: 8x the corpus took %v against %v", unit, tb, ts)
		}
	}
	// Match at the very end of a large corpus is still found.
	var c Corpus
	c.Add(strings.Repeat("a", CorpusCap-100))
	c.Add("\nneedle " + strings.Repeat("q", 40))
	if !c.HasRunIn(strings.Repeat("q", 40)) {
		t.Error("a run at the end of a large corpus was missed")
	}
	// A candidate that is all one character against such a corpus matches at once.
	var d Corpus
	d.Add(strings.Repeat("a", 5000))
	if !d.HasRunIn(strings.Repeat("a", SummaryCap)) {
		t.Error("a repeated-character copy passed")
	}
}
