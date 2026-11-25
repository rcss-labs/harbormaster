package agentresult

import (
	"math/rand/v2"
	"strconv"
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

const longServiceName = "payments-checkout-settlement-reconciliation-worker-eu" // over one run

// nameCorpus holds a body that carries the service name under several members.
func nameCorpus(allow ...string) *Corpus {
	c := corpusOf(`{"service":"` + longServiceName + `","cluster":"` + longServiceName + `","message":"` + copiedSentence + `"}`)
	c.AllowNames(allow...)
	return c
}

const copiedSentence = "connection to the settlement ledger replica was refused after three retries"

func TestAllowNames_ALongServiceNamePassesOnlyWhenAllowed(t *testing.T) {
	summary := "The " + longServiceName + " service restarted; " + longServiceName + " is healthy again."
	if got, replaced := CheckSummaryAs(summary, nameCorpus(), ReplacementReviewSummary); !replaced || got != ReplacementReviewSummary {
		t.Errorf("not allowed: kept %q", got)
	}
	if got, replaced := CheckSummaryAs(summary, nameCorpus(longServiceName), ReplacementReviewSummary); replaced || got != summary {
		t.Errorf("allowed: got %q, replaced %v", got, replaced)
	}
	if got, replaced := CheckSummary(summary, nameCorpus(longServiceName)); replaced || got != summary {
		t.Errorf("allowed, investigation sentence: got %q, replaced %v", got, replaced)
	}
}

func TestAllowNames_CopiedTextAroundAnAllowedNameIsStillCaught(t *testing.T) {
	c := nameCorpus(longServiceName, "other-service-with-a-long-name-for-tests")
	for name, summary := range map[string]string{
		"after":   longServiceName + " said: " + copiedSentence,
		"before":  copiedSentence + " in " + longServiceName,
		"between": "other-service-with-a-long-name-for-tests " + copiedSentence + " " + longServiceName,
		"inside":  "The " + longServiceName + " " + copiedSentence + " and more.",
		"alone":   copiedSentence,
	} {
		if got, replaced := CheckSummaryAs(summary, c, ReplacementReviewSummary); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: kept %q", name, got)
		}
	}
	// Two allowed names next to each other, with nothing copied, pass.
	if _, replaced := CheckSummaryAs(longServiceName+" and "+longServiceName+" "+longServiceName, c, ReplacementReviewSummary); replaced {
		t.Error("adjacent names refused")
	}
}

func TestAllowNames_TheSameSentenceUnderAnyMemberIsCaught(t *testing.T) {
	for _, member := range []string{"message", "definition", "env", "dockerLabels", "service", "name"} {
		c := corpusOf(`{"` + member + `":"` + copiedSentence + ` on ` + longServiceName + `"}`)
		c.AllowNames(longServiceName)
		got, replaced := CheckSummaryAs("It failed: "+copiedSentence+" on "+longServiceName, c, ReplacementReviewSummary)
		if !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: kept %q", member, got)
		}
	}
}

func TestAllowNames_OnlyBareNamesCanBeAllowed(t *testing.T) {
	forbidden := []string{
		"two words that make a long enough run for a copy",
		"dotted.service.name.that.is.long.enough.here",
		"path/to/some/long/service/name/for/the/check",
		"someone.with.a.long.address@example.com",
		"name with\ttab and a long enough length here",
		"ünïcode-service-name-that-is-long-enough-here",
		"", strings.Repeat("a", 129),
	}
	for _, name := range forbidden {
		c := corpusOf(name)
		c.AllowNames(name)
		if len(name) >= VerbatimRun && !c.HasRunIn(name) {
			t.Fatalf("setup: %q not in the corpus", name)
		}
		if _, replaced := CheckSummaryAs("About "+name+" here.", c, ReplacementReviewSummary); !replaced && len(Normalize(name)) >= VerbatimRun {
			t.Errorf("%q was allowed", name)
		}
	}
	// 128 characters is the longest name, 129 is ignored.
	long128, long129 := strings.Repeat("abcd-efgh-", 12)+"abcd-efg", strings.Repeat("abcd-efgh-", 12)+"abcd-efgh"
	if len(long128) != 128 || len(long129) != 129 {
		t.Fatal("setup lengths")
	}
	for name, want := range map[string]bool{long128: false, long129: true} {
		c := corpusOf(name)
		c.AllowNames(name)
		if _, replaced := CheckSummary("Name "+name+".", c); replaced != want {
			t.Errorf("%d characters: replaced %v, want %v", len(name), replaced, want)
		}
	}
}

func TestAllowNames_CaseAndPrefixes(t *testing.T) {
	c := nameCorpus("Payments-Checkout-Settlement-Reconciliation-Worker-EU")
	if _, replaced := CheckSummary("See "+strings.ToUpper(longServiceName)+" and "+longServiceName+".", c); replaced {
		t.Error("case differences were not folded")
	}
	// A name that is a prefix of another does not allow the longer one.
	prefix := "payments-checkout-settlement-reconciliation"
	c = corpusOf(`{"service":"` + longServiceName + `"}`)
	c.AllowNames(prefix)
	if _, replaced := CheckSummary("Look at "+longServiceName+".", c); !replaced {
		t.Error("a prefix allowed the longer name")
	}
	// The longer name does not allow its prefix either.
	c = corpusOf(`{"service":"` + prefix + `"}`)
	c.AllowNames(longServiceName)
	if _, replaced := CheckSummary("Look at "+prefix+".", c); !replaced {
		t.Error("a name allowed its own prefix")
	}
	// A token inside a longer token is not masked.
	c = corpusOf(`{"service":"` + longServiceName + `"}`)
	c.AllowNames(longServiceName)
	if _, replaced := CheckSummary("Look at x"+longServiceName+"y.", c); !replaced {
		t.Error("a name inside a longer token was masked")
	}
}

func TestAllowNames_NotesStayStrictAndNothingChangesWithoutIt(t *testing.T) {
	c := nameCorpus(longServiceName)
	if got := CheckNote("The "+longServiceName+" restarted.", c); got != ReplacementNote {
		t.Errorf("note kept %q", got)
	}
	if !c.HasRunIn(longServiceName) {
		t.Error("HasRunIn was changed by AllowNames")
	}
	// No names, only invalid names: the same as a corpus without the call.
	d := nameCorpus("", "two words", strings.Repeat("a", 129))
	if _, replaced := CheckSummary("The "+longServiceName+" restarted.", d); !replaced {
		t.Error("invalid names allowed something")
	}
	e := nameCorpus()
	for _, text := range []string{copiedSentence, "It logged: " + copiedSentence, "The " + longServiceName + " restarted."} {
		a, ra := CheckSummary(text, e)
		b, rb := CheckSummary(text, nameCorpus())
		if a != b || ra != rb || !ra {
			t.Errorf("%q: %q %v / %q %v", text, a, ra, b, rb)
		}
	}
}

func TestAllowNames_ListCapAndLateCalls(t *testing.T) {
	c := corpusOf(`{"service":"` + longServiceName + `"}`)
	names := make([]string, 0, 10_001)
	for i := 0; i < 10_000; i++ {
		names = append(names, "svc-"+strconv.Itoa(i))
	}
	c.AllowNames(names...)
	c.AllowNames(longServiceName) // past 10,000: not added
	if _, replaced := CheckSummary("About "+longServiceName+".", c); !replaced {
		t.Error("the 10,001st name was added")
	}
	// Callable at any time: after Add, and twice.
	d := corpusOf(`{"service":"` + longServiceName + `"}`)
	d.AllowNames(longServiceName)
	d.Add(`{"other":"x"}`)
	d.AllowNames("svc-1")
	if _, replaced := CheckSummary("About "+longServiceName+".", d); replaced {
		t.Error("an allowed name was refused after a later Add")
	}
}

func TestAllowNames_LinearWithTenThousandNamesAndAHostileCandidate(t *testing.T) {
	build := func(n int) (*Corpus, string) {
		var body strings.Builder
		body.WriteString(`["`)
		body.WriteString(longServiceName)
		for body.Len() < n {
			body.WriteString(`","a b c d e f g h `)
		}
		body.WriteString(`"]`)
		c := corpusOf(body.String())
		names := make([]string, 0, 10_000)
		for i := 0; i < 10_000; i++ {
			names = append(names, "service-number-"+strconv.Itoa(i))
		}
		c.AllowNames(names...)
		c.AllowNames(longServiceName)
		// Every character of the candidate is part of an allowed-looking token, or a near miss of one.
		hostile := strings.Repeat("service-number-1 service-number-2x "+longServiceName+" "+strings.Repeat("a", 31)+"b ", 1200/100)
		return c, hostile
	}
	cs, hs := build(100_000)
	cl, hl := build(CorpusCap - 1000)
	if cl.Full() {
		t.Fatal("corpus over the cap")
	}
	ts := timeOf(func() { _ = cs.hasRunAllowing(hs) })
	tl := timeOf(func() { _ = cl.hasRunAllowing(hl) })
	if tl > 40*ts+20*time.Millisecond || tl > 2*time.Second {
		t.Errorf("2 MiB corpus took %v against %v for 100 KB", tl, ts)
	}
	// A candidate built only of tokens, at the summary cap, over a large corpus.
	big := strings.Repeat("service-number-77 ", 1200/18)
	if tb := timeOf(func() { _ = cl.hasRunAllowing(big) }); tb > 2*time.Second {
		t.Errorf("token-only candidate took %v", tb)
	}
	if cl.hasRunAllowing(big) {
		t.Error("allowed tokens counted as a copy")
	}
}
