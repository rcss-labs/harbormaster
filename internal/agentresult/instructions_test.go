package agentresult

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestCleanInstructions_LineEndsAndEdgesOnly(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"crlf", "a\r\nb", "a\nb"},
		{"lone cr", "a\rb", "a\nb"},
		{"edges trimmed", " \t\n a b \r\n\t ", "a b"},
		{"inner spacing, tabs and blank lines kept", "a  b\t\tc\n\n\n  d  \ne", "a  b\t\tc\n\n\n  d  \ne"},
		{"mixed ends", "x\r\r\ny\n\rz", "x\n\ny\n\nz"},
	}
	for _, c := range cases {
		if got := CleanInstructions(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCleanInstructions_Idempotent(t *testing.T) {
	for _, in := range []string{"", " a\r\nb ", "\r\n\r\n", "x\r\r\ny\n\rz", "  a \t\n b  "} {
		once := CleanInstructions(in)
		if twice := CleanInstructions(once); twice != once {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestCheckInstructions_Table(t *testing.T) {
	fakeKey := "AKIA" + "IOSFODNN7EXAMPLE"
	credURL := "https://admin:" + "s3cretpass99@example.com/path"
	cases := []struct {
		name string
		in   string
		want InstructionProblem
	}{
		{"plain prose", "Focus on idle databases and say which ones to stop.", InstructionsOK},
		{"list with line breaks and tabs", "Check:\n\t- databases\n\t- queues\n", InstructionsOK},
		{"plain url", "See https://example.com/runbook for context.", InstructionsOK},
		{"angle brackets", "Compare <prod> with <staging>.", InstructionsOK},
		{"unit reference at line start", "[e1] is the style to follow.", InstructionsOK},
		{"2000 characters", strings.Repeat("abcd ", 400), InstructionsOK},
		{"2001 characters", strings.Repeat("abcd ", 400) + "x", InstructionsTooLong},
		{"8000 bytes", strings.Repeat("a", 8000), InstructionsTooLong},
		{"8001 bytes", strings.Repeat("a", 8001), InstructionsTooLong},
		{"2000 four-byte characters, exactly 8000 bytes", strings.Repeat("\U0001F600", 2000), InstructionsOK},
		{"2001 four-byte characters, 8004 bytes", strings.Repeat("\U0001F600", 2001), InstructionsTooLong},
		{"empty", "", InstructionsRequired},
		{"white space only", " \n\t ", InstructionsRequired},
		{"lone NUL", "a\x00b", InstructionsNotText},
		{"invalid utf-8", "a\xffb", InstructionsNotText},
		{"zero-width space", "a​b", InstructionsHidden},
		{"bidi override", "a‮b", InstructionsHidden},
		{"tag character", "a\U000E0041b", InstructionsHidden},
		{"variation selector", "a️b", InstructionsHidden},
		{"escape control", "a\x1bb", InstructionsHidden},
		{"token-shaped value", "Use the key " + fakeKey + " to log in.", InstructionsSecret},
		{"url with credentials", "Open " + credURL + " first.", InstructionsSecret},
	}
	for _, c := range cases {
		if got := CheckInstructions(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCheckInstructions_OrderOfProblems(t *testing.T) {
	fakeKey := "AKIA" + "IOSFODNN7EXAMPLE"
	cases := []struct {
		name string
		in   string
		want InstructionProblem
	}{
		{"bytes before not_text", strings.Repeat("a", 8001) + "\x00", InstructionsTooLong},
		{"not_text before too_long by characters", strings.Repeat("a", 2001) + "\x00", InstructionsNotText},
		{"too_long before hidden", strings.Repeat("a", 2001) + "​", InstructionsTooLong},
		{"hidden before secret", "​ " + fakeKey, InstructionsHidden},
		{"not_text before required is moot, NUL is not empty", "\x00", InstructionsNotText},
	}
	for _, c := range cases {
		if got := CheckInstructions(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInstructionsHash_StableAndOfTheCleanText(t *testing.T) {
	clean := "Look at idle databases.\nName each one."
	sum := sha256.Sum256([]byte(clean))
	want := hex.EncodeToString(sum[:])
	got := InstructionsHash(clean)
	if got != want {
		t.Fatalf("hash %q, want %q", got, want)
	}
	if again := InstructionsHash(clean); again != got {
		t.Errorf("hash not stable: %q then %q", got, again)
	}
	if other := InstructionsHash(clean + "."); other == got {
		t.Error("different texts share a hash")
	}
	if got != strings.ToLower(got) || len(got) != 64 {
		t.Errorf("hash is not 64 lowercase hex characters: %q", got)
	}
}

const echoSource = "Review the services with the most stopped tasks in the last 7 days, and say which of them " +
	"run a single task with no autoscaling. Flag secrets as plain environment variables, and name the owner of each finding."

// echoRun returns n normalised characters of text, starting at its start.
func echoRun(t *testing.T, text string, n int) string {
	t.Helper()
	norm := Normalize(text)
	if len(norm) < n {
		t.Fatalf("test setup: %d normalised characters, need %d", len(norm), n)
	}
	return norm[:n]
}

func TestEchoes_EightyCharactersInARow(t *testing.T) {
	run80 := echoRun(t, echoSource, EchoRun)
	if len(run80) != 80 {
		t.Fatalf("test setup: run is %d characters", len(run80))
	}
	if !Echoes("Intro: "+run80+" and more", echoSource) {
		t.Error("80 characters in a row not reported as an echo")
	}
	if Echoes("9"+run80[:79]+"9", echoSource) {
		t.Error("79 characters in a row reported as an echo")
	}
	if !Echoes(strings.ToUpper(strings.ReplaceAll(run80, " ", " \t\n,; ")), echoSource) {
		t.Error("same words in other case and spacing not reported as an echo")
	}
	if !Echoes(strings.ReplaceAll("x "+run80, "\n", "\\n"), echoSource) {
		t.Error("a copy with JSON escapes not reported as an echo")
	}
}

func TestEchoes_JoinedCopyIsAnEcho(t *testing.T) {
	request := "Review the services with the most stopped tasks in the last 7 days and say which of them run a single task with no autoscaling"
	cases := map[string]string{
		"hyphens":     strings.ReplaceAll(request, " ", "-"),
		"underscores": strings.ReplaceAll(request, " ", "_"),
	}
	for name, candidate := range cases {
		if !Echoes(candidate, request) {
			t.Errorf("%s: the whole request joined into one token not reported as an echo", name)
		}
	}
	cjk := "本番環境のデータベースのうち使われていないものを探して停止してよいかどうかを理由と一緒に説明してください" +
		"特に費用が大きいものと所有者が決まっていないものを先に書いてください"
	if len(Normalize(cjk)) < EchoRun {
		t.Fatalf("test setup: clause is %d normalised characters", len(Normalize(cjk)))
	}
	if !Echoes("概要: "+cjk, "管理者の依頼: "+cjk) {
		t.Error("an unspaced clause copied exactly not reported as an echo")
	}
}

func TestEchoes_JSONEscapesBothWays(t *testing.T) {
	plain := "Review the services with the most stopped tasks\nin the last 7 days and say which of them\nrun a single task with no autoscaling."
	esc := strings.ReplaceAll(plain, "\n", `\n`)
	if esc == plain {
		t.Fatal("test setup: the escaped text equals the plain one")
	}
	if !Echoes(plain, esc) {
		t.Error("a copy with real line breaks of a source with escapes not reported as an echo")
	}
	if !Echoes(esc, plain) {
		t.Error("a copy with escapes of a source with real line breaks not reported as an echo")
	}
}

func TestEchoes_NameIsNeverAnEcho(t *testing.T) {
	name := "payments-settlement-reconciliation-worker-eu-central-prod-blue-green-canary-shard_07"
	if len(Normalize(name)) < EchoRun {
		t.Fatalf("test setup: name is %d normalised characters", len(Normalize(name)))
	}
	source := "Look first at " + name + " and its siblings."
	for _, candidate := range []string{name, "The service " + name + " is idle.", strings.ToUpper(name), name + ", " + name} {
		if Echoes(candidate, source) {
			t.Errorf("a name alone reported as an echo: %q", candidate)
		}
	}
}

func TestEchoes_NameWithTextBesideItIsAnEcho(t *testing.T) {
	name := "payments-settlement-reconciliation-worker-eu-central-prod-blue-green-canary-shard_07"
	source := "Please look first at " + name + " and its siblings, then report."
	// 80 normalised characters that run across the edge of the name and into the text beside it.
	if !Echoes("Please look first at "+name+" and its siblings", source) {
		t.Error("name plus text on both sides not reported as an echo")
	}
	if !Echoes(name+" and its siblings, then report", source) {
		t.Error("name plus text after it not reported as an echo")
	}
	if !Echoes("Please look first at payments settlement reconciliation worker eu central prod blue", source) {
		t.Error("the start of a name plus text before it not reported as an echo")
	}
}

func TestEchoes_OwnWordsAreNotAnEcho(t *testing.T) {
	pairs := [][2]string{
		{"Look at the services with the most stopped tasks in the last 7 days.", "Three services stopped tasks often in the last 7 days; checkout stopped the most."},
		{"Tell me which services run a single task with no autoscaling.", "Two services run one task and have no autoscaling: checkout and search."},
		{"Find secrets as plain environment variables.", "The worker keeps its settings as plain environment variables; no secret values are shown."},
		{"Focus on idle databases and say which ones to stop.", "Four databases were idle for the whole period. Stopping them would save money each month."},
		{"Compare the cost of this week with last week.", "Spending went up this week against last week, mostly in data transfer."},
		{"Check that every service has an owner tag.", "Five services have no owner tag, so nobody is named to act on them."},
		{"List the queues with old messages and name each owner.", "Two queues hold old messages. The payments team owns one and the search team the other."},
		{"Look for load balancers with no healthy targets.", "One load balancer has no healthy targets, and it has had none since Monday."},
		{"Report services that deploy more than twice a day.", "Only checkout deploys more than twice a day; the rest deploy weekly."},
		{"Say which tasks use more than 90 percent of their memory.", "The report task uses 93 percent of its memory and was stopped twice for it."},
		{"Review public buckets and say who made them public.", "Two buckets are public. Their audit entries show the same person made both public."},
		{"Find services without health checks.", "Three services have no health check, so a stuck task is not replaced for a long time."},
		{"Summarize the largest cost changes by service.", "The largest rise is in search, followed by checkout; the other services moved little."},
		{"Point out anything that changed since the last review.", "Since the last review one new service appeared and two tasks were stopped more often."},
	}
	for _, p := range pairs {
		if Echoes(p[1], p[0]) || Echoes(p[0], p[1]) {
			t.Errorf("own words reported as an echo: %q", p[1])
		}
	}
}

func TestEchoes_EmptyOrShortSourceIsFalse(t *testing.T) {
	for _, source := range []string{"", "   ", "short text of 20 chars", strings.Repeat("a ", 39) + "a"} {
		if Echoes(source+" "+source, source) {
			t.Errorf("source %q: reported as an echo", source)
		}
	}
	if Echoes("", echoSource) {
		t.Error("empty candidate reported as an echo")
	}
	exact := echoRun(t, echoSource, EchoRun)
	if !Echoes(exact, exact) {
		t.Error("exactly 80 normalised characters in both not reported")
	}
}

func adversarialText(n int) string {
	return strings.Repeat("a b\\u00", n/6+1)[:n]
}

func TestCheckInstructions_LinearOn200KB(t *testing.T) {
	inputs := []string{
		strings.Repeat("a", 200<<10),
		strings.Repeat("​", 200<<10),
		strings.Repeat("AKIA", 50<<10),
		adversarialText(200 << 10),
	}
	for i, in := range inputs {
		start := time.Now()
		_ = CheckInstructions(in)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("input %d took %v", i, d)
		}
	}
	// Under the byte cap but heavy for the scrubber: the longest text that gets past the first check.
	for i, in := range []string{strings.Repeat("A", 1999) + "=", strings.Repeat("a1-", 666), strings.Repeat("https://u:", 200)} {
		start := time.Now()
		_ = CheckInstructions(in)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("scrubber input %d took %v", i, d)
		}
	}
}

func TestEchoes_LinearOn200KB(t *testing.T) {
	big := strings.Repeat("a", 200<<10)
	inputs := [][2]string{
		{big, big},
		{strings.Repeat("ab ", 70<<10), strings.Repeat("ab ", 70<<10)},
		{adversarialText(200 << 10), adversarialText(200 << 10)},
		{strings.Repeat("x", 2000), big},
		{strings.Repeat("ab-", 70<<10), strings.Repeat("ab-", 70<<10)},
	}
	for i, in := range inputs {
		start := time.Now()
		_ = Echoes(in[0], in[1])
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("input %d took %v", i, d)
		}
	}
}
