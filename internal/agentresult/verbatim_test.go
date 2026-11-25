package agentresult

import (
	"math/rand/v2"
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

// timeOf returns the best of three runs of f, so one slow run does not decide a ratio.
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

// words builds deterministic pseudo-text of about n bytes from a seed.
func words(seed uint64, n int) string {
	rng := rand.New(rand.NewPCG(seed, seed))
	var b strings.Builder
	for b.Len() < n {
		for i, l := 0, 2+rng.IntN(9); i < l; i++ {
			b.WriteByte(byte('a' + rng.IntN(26)))
		}
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

func TestHasRunIn_WallClockOnTheLargestInputs(t *testing.T) {
	var c Corpus
	c.Add(words(1, CorpusCap))
	if c.Full() {
		t.Fatal("2 MiB exactly is full")
	}
	candidate := words(2, SummaryCap)
	start := time.Now()
	hit := c.HasRunIn(candidate)
	if took := time.Since(start); took > time.Second {
		t.Errorf("2 MiB corpus against 1200 characters took %v", took)
	}
	if hit {
		t.Error("unrelated text was found in the corpus")
	}
	// A copy from the very end is still found, in the same time.
	if !c.HasRunIn(words(2, 300) + " " + words(1, CorpusCap)[CorpusCap-200:CorpusCap-100]) {
		t.Error("a copy from the end of the corpus was missed")
	}
}

// The time must not grow with the candidate: it is one roll over the corpus however long the summary is.
func TestHasRunIn_GrowingCandidateStaysFlat(t *testing.T) {
	var c Corpus
	c.Add(words(3, CorpusCap/2))
	short, long := words(4, 64), words(4, SummaryCap)
	ts := timeOf(func() { c.HasRunIn(short) })
	tl := timeOf(func() { c.HasRunIn(long) })
	if tl > 6*ts+20*time.Millisecond {
		t.Errorf("a 1200-character candidate took %v against %v for 64", tl, ts)
	}
}

func TestHasRunIn_FindsLateAndNonASCIICopies(t *testing.T) {
	line := "Ошибка соединения с базой данных: тайм-аут при чтении ответа сервера"
	var c Corpus
	c.Add(words(5, 2000) + "\n" + line + "\n" + words(6, 2000))
	late := words(7, 700) + " " + strings.ToUpper(line) + " " + words(8, 100)
	if !c.HasRunIn(late) {
		t.Error("a non-ASCII copy after character 400 was missed")
	}
	cjk := "接続がタイムアウトしました。データベースサーバーからの応答がありません。再試行してください"
	var d Corpus
	d.Add(`{"message":"` + cjk + `"}`)
	if !d.HasRunIn("理由: " + cjk) {
		t.Error("a CJK copy was missed")
	}
	if d.HasRunIn("理由: 接続がタイムアウトしました") {
		t.Error("a short CJK phrase was refused")
	}
}

func TestCorpus_RawBytesCountedAndAddAfterFull(t *testing.T) {
	// Nothing in this text survives normalisation, but its raw size still counts.
	var c Corpus
	c.Add(strings.Repeat("!", CorpusCap+1))
	if !c.Full() {
		t.Error("raw punctuation bytes were not counted")
	}
	c.Add(sharedRun)
	if !c.Full() || c.HasRunIn(sharedRun) {
		t.Error("a full corpus took more text")
	}
	// Multi-byte text counts bytes, not characters.
	var d Corpus
	d.Add(strings.Repeat("é", CorpusCap/2+1))
	if !d.Full() {
		t.Error("multi-byte text was counted in characters")
	}
}

func TestCorpus_DuplicateKeysAndEscapedText(t *testing.T) {
	first, second := "first copy of the duplicated key value 1111", "second copy of the duplicated key value 2222"
	var c Corpus
	c.Add(`{"k":"` + first + `","k":"` + second + `","n":null,"b":true,"x":{"k":["` + sharedRun + `"]}}`)
	for _, v := range []string{first, second, sharedRun} {
		if !c.HasRunIn(v) {
			t.Errorf("value %q was dropped", v)
		}
	}
	// A log line that carries JSON escapes is also compared with its unescaped form.
	var d Corpus
	d.Add(`{"level":"error"} line: connection\u0020reset\u0020by\u0020peer\u0020while\u0020reading\nheaders from upstream`)
	if !d.HasRunIn("The connection reset by peer while reading") {
		t.Error("an escaped \u0020 log line was not compared unescaped")
	}
	if !d.HasRunIn("while reading headers from upstream") {
		t.Error("an escaped backslash-n log line was not compared unescaped")
	}
	// Invalid JSON is kept whole.
	var e Corpus
	e.Add(`{"broken": "` + sharedRun)
	if !e.HasRunIn(sharedRun) {
		t.Error("invalid JSON was dropped")
	}
}

func TestCorpusAndNormalize_LinearOnLargeInput(t *testing.T) {
	big := func(n int) map[string]string {
		return map[string]string{
			"symbols":      strings.Repeat("!-é€ ", n/7),
			"one word":     strings.Repeat("a", n),
			"json strings": `["` + strings.Repeat(`ab\"cd","x`, n/10) + `"]`,
			"json keys":    "{" + strings.Repeat(`"k":"v",`, n/8) + `"z":1}`,
			"json nesting": strings.Repeat("[", n),
			"escapes":      strings.Repeat(` \n`, n/8),
		}
	}
	for name := range big(8) {
		small, large := big(25_000)[name], big(200_000)[name]
		var out string
		ts := timeOf(func() { out = Normalize(small); var c Corpus; c.Add(small) })
		tl := timeOf(func() { out = Normalize(large); var c Corpus; c.Add(large) })
		_ = out
		if tl > 30*ts+20*time.Millisecond || tl > 2*time.Second {
			t.Errorf("%s: 200 KB took %v against %v for 25 KB", name, tl, ts)
		}
	}
	if got := Normalize(strings.Repeat("a!", 100_000)); len(got) != 199_999 {
		t.Errorf("normalised length %d", len(got))
	}
}

// The model reads a JSON body as written, escapes included, and may copy it that way: the candidate is also compared
// with one level of escapes removed.
func TestHasRunIn_CopyOfAnEscapedJSONBodyAsShown(t *testing.T) {
	var c Corpus
	c.Add(`{"url":"https://a.io/s?q=alpha&region=eu-west&page=2&sort=latest-first","stack":"Err: boom\n\tat x.A.run(A.java:1)\n\tat x.B.call(B.java:2)\n\tat x.C.go(C.java:3)"}`)
	asShown := `It called https://a.io/s?q=alpha\u0026region=eu-west\u0026page=2\u0026sort=latest-first and failed`
	if !c.HasRunIn(asShown) {
		t.Error("a query string copied with its \\u0026 escapes passed")
	}
	trace := `Err: boom\n\tat x.A.run(A.java:1)\n\tat x.B.call(B.java:2)\n\tat x.C.go(C.java:3)`
	if !c.HasRunIn(trace) {
		t.Error("a stack trace copied with its backslash-n escapes passed")
	}
	if c.HasRunIn(`a harmless sentence about \n and \u0026 that shares nothing with the body at all, really`) {
		t.Error("an unrelated text with escape-looking characters was refused")
	}
}

func TestCorpus_LongSecondKeyIsNotAValue(t *testing.T) {
	longKey := "a_second_key_that_is_much_longer_than_thirty_two_characters"
	var c Corpus
	c.Add(`{"first":"value one","` + longKey + `":"` + sharedRun + `","third":["x"]}`)
	if c.HasRunIn(strings.ReplaceAll(longKey, "_", " ")) {
		t.Error("a long second key was kept as a value")
	}
	if !c.HasRunIn(sharedRun) {
		t.Error("the value after a long second key was dropped")
	}
}

const longServiceName = "payments-checkout-settlement-reconciliation-worker-eu" // 53 chars: over one run

func exemptCorpusOf(texts ...string) *Corpus {
	c := &Corpus{}
	c.ExemptNames()
	for _, text := range texts {
		c.Add(text)
	}
	return c
}

func TestCorpus_ExemptNamesLetsALongServiceNamePass(t *testing.T) {
	name := strings.Repeat("checkout-", 7)[:60]
	summary := "The " + name + " service restarted; " + name + " is healthy again."
	c := exemptCorpusOf(`{"service":"` + name + `","state":"ok"}`)
	if got, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); replaced || got != summary {
		t.Fatalf("service name refused: %q, replaced %v", got, replaced)
	}
	for _, member := range []string{"cluster", "name", "label"} {
		c = exemptCorpusOf(`{"` + member + `":"` + name + `"}`)
		if _, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); replaced {
			t.Errorf("%s: refused", member)
		}
	}
	// Names inside an array of objects, at depth.
	c = exemptCorpusOf(`{"items":[{"meta":{"service":"` + name + `"}}]}`)
	if _, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); replaced {
		t.Error("nested member refused")
	}
}

func TestCorpus_ExemptNamesStillCatchesTheSameTextElsewhere(t *testing.T) {
	name := strings.Repeat("checkout-", 7)[:60]
	summary := "The " + name + " service restarted."
	for _, body := range []string{
		`{"message":"` + name + `"}`,
		`{"service":{"message":"` + name + `"}}`,      // an object under the member is not exempt
		`{"service":["` + name + `"]}`,                // neither is an array
		`{"service":"short","detail":"` + name + `"}`, // a later member
		`{"servicex":"` + name + `"}`,
		`{"Service":"` + name + `"}`,
		`plain text ` + name,
		`[{"message":"` + name + `"}]`,
	} {
		c := exemptCorpusOf(body)
		if got, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: kept %q", body, got)
		}
	}
	// The value of a key is not mistaken for a key: the name sits under "note", after a member named "name".
	c := exemptCorpusOf(`{"name":"x","note":"` + name + `"}`)
	if _, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); !replaced {
		t.Error("value after an exempt member was exempt too")
	}
}

func TestCorpus_ExemptNamesStopsAt128Bytes(t *testing.T) {
	at := strings.Repeat("alpha-", 10) + "beta-gamma-delta " + strings.Repeat("omega-", 8) + "psi" // 128 bytes
	over := at + "c"                                                                               // 129 bytes
	atSummary := "It named " + at + " here."
	if _, replaced := CheckSummaryAs(atSummary, exemptCorpusOf(`{"name":"`+at+`"}`), ReplacementReviewSummary); replaced {
		t.Error("128 bytes was stored")
	}
	if _, replaced := CheckSummaryAs("It named "+over+" here.", exemptCorpusOf(`{"name":"`+over+`"}`), ReplacementReviewSummary); !replaced {
		t.Error("129 bytes was exempt")
	}
	// The bound is on bytes, not characters: 64 two-byte letters are 128 bytes, 65 are 130.
	two, three := strings.Repeat("é", 64), strings.Repeat("é", 65)
	if _, replaced := CheckSummaryAs("It named "+two+" here.", exemptCorpusOf(`{"name":"`+two+`"}`), ReplacementReviewSummary); replaced {
		t.Error("128 bytes of two-byte letters was stored")
	}
	if _, replaced := CheckSummaryAs("It named "+three+" here.", exemptCorpusOf(`{"name":"`+three+`"}`), ReplacementReviewSummary); !replaced {
		t.Error("130 bytes of two-byte letters was exempt")
	}
}

func TestCorpus_ExemptNamesStillScrubs(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	c := exemptCorpusOf(`{"name":"` + secret + `"}`)
	if got, replaced := CheckSummaryAs("The role used "+secret+" to start.", c, ReplacementReviewSummary); !replaced || got != ReplacementReviewSummary {
		t.Fatalf("got %q, replaced %v", got, replaced)
	}
}

func TestCorpus_WithoutExemptionNothingChanged(t *testing.T) {
	name := strings.Repeat("checkout-", 7)[:60]
	summary := "The " + name + " service restarted."
	for _, body := range []string{
		`{"service":"` + name + `"}`, `{"cluster":"` + name + `"}`, `{"name":"` + name + `"}`, `{"label":"` + name + `"}`,
		`{"a":{"service":"` + name + `"}}`,
	} {
		c := corpusOf(body)
		if got, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: kept %q", body, got)
		}
		if got, replaced := CheckSummary(summary, c); !replaced || got != ReplacementSummary {
			t.Errorf("%s: CheckSummary kept %q", body, got)
		}
	}
}

func TestCorpus_ExemptValuesCountTowardTheCap(t *testing.T) {
	c := exemptCorpusOf(`{"service":"` + strings.Repeat("a", 100) + `"}`)
	c.Add(strings.Repeat(" ", CorpusCap-200))
	if c.Full() {
		t.Fatal("full before the cap")
	}
	c.Add(`{"service":"` + strings.Repeat("b", 100) + `"}`)
	if !c.Full() {
		t.Fatal("exempt values were not counted toward the cap")
	}
	if _, replaced := CheckSummaryAs("fine summary", c, ReplacementReviewSummary); !replaced {
		t.Error("a full corpus vouched for a summary")
	}
}

func TestCorpus_ExemptNamesLinearOnLargeBodies(t *testing.T) {
	big := func(n int) string {
		return strings.Repeat(`{"service":"s","list":[{"name":"n","x":`, n/40) + "1" + strings.Repeat("}]}", n/40)
	}
	flat := func(n int) string {
		return "[" + strings.Repeat(`{"label":"l","message":"m"},`, n/27) + `{"label":"z"}]`
	}
	for name, gen := range map[string]func(int) string{"nested": big, "flat": flat} {
		small, large := gen(25_000), gen(CorpusCap*9/10)
		ts := timeOf(func() { c := exemptCorpusOf(small); _ = c.HasRunIn(longServiceName + longServiceName) })
		var c *Corpus
		tl := timeOf(func() { c = exemptCorpusOf(large); _ = c.HasRunIn(longServiceName + longServiceName) })
		if c.Full() {
			t.Fatalf("%s: input over the cap", name)
		}
		if tl > 100*ts+50*time.Millisecond || tl > 2*time.Second {
			t.Errorf("%s: about 2 MiB took %v against %v for 25 KB", name, tl, ts)
		}
	}
}

func TestCorpus_ExemptNamesAfterAddPanicsAndStaysStrict(t *testing.T) {
	name := strings.Repeat("checkout-", 7)[:60]
	c := &Corpus{}
	c.Add(`{"service":"` + name + `"}`)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a late ExemptNames did not panic under test")
			}
		}()
		c.ExemptNames()
	}()
	if !c.HasRunIn(name) {
		t.Error("the corpus was not left strict")
	}
}
