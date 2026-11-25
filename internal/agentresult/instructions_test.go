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

const echoSource = "The quick brown fox jumps over the lazy dog while the cat watches quietly"

func TestEchoes_ThirtyTwoCharactersInARow(t *testing.T) {
	norm := Normalize(echoSource)
	run32 := norm[10:42]
	if len(run32) != 32 {
		t.Fatalf("test setup: run is %d characters", len(run32))
	}
	if Echoes("9"+run32[:31]+"9", echoSource) {
		t.Error("31 characters in a row reported as an echo")
	}
	if !Echoes("Intro: "+run32+" and more", echoSource) {
		t.Error("32 characters in a row not reported as an echo")
	}
	if !Echoes(strings.ToUpper(strings.ReplaceAll(run32, " ", " \t\n,; ")), echoSource) {
		t.Error("same words in other case and spacing not reported as an echo")
	}
}

func TestEchoes_OwnWordsAreNotAnEcho(t *testing.T) {
	if Echoes("Three databases sit idle and cost money; stop them after checking owners.", echoSource) {
		t.Error("own words reported as an echo")
	}
}

func TestEchoes_EmptyOrShortSourceIsFalse(t *testing.T) {
	for _, source := range []string{"", "   ", "short text of 20 chars", strings.Repeat("a", 31)} {
		if Echoes(source+" "+source, source) {
			t.Errorf("source %q: reported as an echo", source)
		}
	}
	if Echoes("", echoSource) {
		t.Error("empty candidate reported as an echo")
	}
	if !Echoes(strings.Repeat("a", 32), strings.Repeat("a", 32)) {
		t.Error("exactly 32 normalised characters in both not reported")
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
	}
	for i, in := range inputs {
		start := time.Now()
		_ = Echoes(in[0], in[1])
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("input %d took %v", i, d)
		}
	}
}
