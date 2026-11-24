package secretshape

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// encode wraps text in times JSON string literals, as a JSON encoder writes it.
func encode(t *testing.T, text string, times int) string {
	t.Helper()
	for i := 0; i < times; i++ {
		raw, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	return text
}

func requireValid(t *testing.T, label, text string) {
	t.Helper()
	if !json.Valid([]byte(text)) {
		t.Errorf("%s: not valid JSON: %s", label, text)
	}
}

// A secret right after a JSON escape must be found: as a JSON string, inside an answer, re-encoded, and when the
// answer was cut and only the plain-text rules are left.
func TestScrubText_SecretsAfterJSONEscapes(t *testing.T) {
	for _, tt := range []struct{ literal, leaked string }{
		{`"line1\ntoken=hunter2"`, "hunter2"},
		{`"a\tkey=hunter2"`, "hunter2"},
		{`"a\rpassword=hunter2"`, "hunter2"},
		{`"a\bsecret: hunter2"`, "hunter2"},
		{`"a\fauth=hunter2"`, "hunter2"},
		{`"a\/token=hunter2"`, "hunter2"},
		{`"x\n` + fakeAWSKey + `"`, fakeAWSKey},
		{`"x\t` + fakeGitHub + `"`, fakeGitHub},
		{`"x\n` + fakeSK + `"`, fakeSK},
		{`"x\n` + fakeJWT + `"`, fakeJWT},
		{`"x\n` + fakeHex40 + ` y"`, fakeHex40},
		{`"x\nBearer hunter2"`, "hunter2"},
		{`"https://x/y?a=1\u0026token=hunter2"`, "hunter2"},
		{`"https://x/y?a=1&token=hunter2"`, "hunter2"},
		{`"password\u003dhunter2"`, "hunter2"},
		{`"\u0022password\u0022: \u0022hunter2\u0022"`, "hunter2"},
		{`"{\"name\":\"DB_PASSWORD\",\"value\":\"hunter2\"}"`, "hunter2"},
	} {
		answer := `{"lines":["ok",` + tt.literal + `],"n":1}`
		for depth := 1; depth <= 3; depth++ {
			for label, text := range map[string]string{
				"string": encode(t, tt.literal, depth-1),
				"answer": encode(t, answer, depth-1),
				"object": `{"a":` + encode(t, tt.literal, depth-1) + `,"b":"after"}`,
			} {
				got := ScrubText(text)
				if strings.Contains(got, tt.leaked) {
					t.Errorf("%s depth %d: %q leaked from %s: %s", label, depth, tt.leaked, text, got)
				}
				requireValid(t, label, got)

				// The same text followed by a note, as a result cut at the read cap arrives.
				if cut := ScrubText(text + ",\n[The rest was left out.]"); strings.Contains(cut, tt.leaked) {
					t.Errorf("%s depth %d, cut: %q leaked: %s", label, depth, tt.leaked, cut)
				}
			}
		}
	}
}

func TestScrubText_JSONMembers(t *testing.T) {
	for text, want := range map[string]string{
		`{"name":"DB_PASSWORD","value":"hunter2"}`:             `{"name":"DB_PASSWORD","value":"[redacted]"}`,
		`{"value":"hunter2","Name":"PGPASS"}`:                  `{"value":"[redacted]","Name":"PGPASS"}`,
		`{"name":"DB_PASSWORD","value":["hunter2",{"a":"b"}]}`: `{"name":"DB_PASSWORD","value":["[redacted]",{"a":"[redacted]"}]}`,
		`{"name":"LOG_LEVEL","value":"debug"}`:                 `{"name":"LOG_LEVEL","value":"debug"}`,
		`{"password":12345}`:                                   `{"password":"[redacted]"}`,
		`{"password":-1.5e3,"n":1.50}`:                         `{"password":"[redacted]","n":1.50}`,
		`{"password":"","token":null}`:                         `{"password":"","token":null}`,
		`{"api_keys":["hunter2"]}`:                             `{"api_keys":["[redacted]"]}`,
		`{"api_keys":["hunter2",12,"x9"]}`:                     `{"api_keys":["[redacted]",12,"[redacted]"]}`,
		`{"api_keys":"abc,def"}`:                               `{"api_keys":"[redacted]"}`,
		`{"password":["a",["b"]]}`:                             `{"password":["[redacted]",["[redacted]"]]}`,
		`{"missing_keys":["DB_HOST","b"],"max_tokens":[1,2]}`:  `{"missing_keys":["DB_HOST","b"],"max_tokens":[1,2]}`,
		`{"max_tokens":4096,"total_tokens":"10/20"}`:           `{"max_tokens":4096,"total_tokens":"10/20"}`,
		`{"pass\u0077ord":"hunter2"}`:                          `{"pass\u0077ord":"[redacted]"}`,
		`{"a":"password=hun}ter2"}`:                            `{"a":"password=[redacted]"}`,
		`{"token=hunter2":1}`:                                  `{"token=[redacted]":1}`,
		`{"id":` + strings.Repeat("12345678", 6) + `}`:         `{"id":"[redacted]"}`,
	} {
		got := ScrubText(text)
		if got != want {
			t.Errorf("ScrubText(%s) = %s, want %s", text, got, want)
		}
		requireValid(t, text, got)
		if again := ScrubText(got); again != got {
			t.Errorf("scrubbing %s again gave %s", got, again)
		}
	}
}

// What is not redacted keeps its exact bytes: spacing, key order, number literals, duplicate keys and escapes.
func TestScrubText_JSONKeepsEverythingElse(t *testing.T) {
	for text, want := range map[string]string{
		" {\n \"b\" : 1.0e+2, \"a\":\"<\\u003c\\/ \\ud83d\\ude00 \\ud800\", \"a\":12345678901234567890,\n\"token\" : \"x\" }\n": "" +
			" {\n \"b\" : 1.0e+2, \"a\":\"<\\u003c\\/ \\ud83d\\ude00 \\ud800\", \"a\":12345678901234567890,\n\"token\" : \"[redacted]\" }\n",
		`{"m":"\u00e9\ud83d\ude00 token=ab\u00e9 \u00e9 <b>"}`: `{"m":"\u00e9\ud83d\ude00 token=[redacted] \u00e9 <b>"}`,
		"{\"token\":\"a\"}\n{\"x\":1}\n\n{\"m\":\"pwd=a\"}\n":  "{\"token\":\"[redacted]\"}\n{\"x\":1}\n\n{\"m\":\"pwd=[redacted]\"}\n",
		"start\n{\"name\":\"DB_PASS\",\"value\":\"a1\"}\nend":  "start\n{\"name\":\"DB_PASS\",\"value\":\"[redacted]\"}\nend",
		`{"a":[{"b":{}},[],"","\\",null,true]}`:                `{"a":[{"b":{}},[],"","\\",null,true]}`,
	} {
		if got := ScrubText(text); got != want {
			t.Errorf("ScrubText(%s)\n got %s\nwant %s", text, got, want)
		}
	}
}

func TestScrubText_ValidJSONStaysValid(t *testing.T) {
	for text, leaked := range map[string]string{
		`"{\"url\":\"https://x/y?token=hunter2\"}"`:                          "hunter2",
		`{"lines":["{\"password\":\"hun","normal line two","three"],"n":1}`:  `\"hun`,
		`{"password":"hunter2\\"}`:                                           "hunter2",
		`{"line":"password=hunter2\\"}`:                                      "hunter2",
		`{"line":"{\"password\":\"hunter2\\\\\"}"}`:                          "hunter2",
		`{"line":"\"{\\\"password\\\":\\\"hunter2\\\\\\\\\\\"}\"","n":[1]}`:  "hunter2",
		`{"line":"{\"password\":{\"a\":[\"password=hunter2}]\"]}}","n":"}"}`: "hunter2",
	} {
		requireValid(t, "fixture", text)
		for depth := 0; depth <= 2; depth++ {
			in := encode(t, text, depth)
			got := ScrubText(in)
			requireValid(t, in, got)
			if strings.Contains(got, leaked) {
				t.Errorf("%q left in %s", leaked, got)
			}
		}
	}
	got := ScrubText(`{"lines":["{\"password\":\"hun","normal line two","three"],"n":1}`)
	if want := `{"lines":["{\"password\":\"[redacted]","normal line two","three"],"n":1}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// In text that is not JSON, redaction stops at the end of the line or at the quote around it.
func TestScrubText_PlainTextLists(t *testing.T) {
	for text, want := range map[string]string{
		`API_KEYS=[abc,def] region=eu`:                  `API_KEYS=[[redacted]] region=eu`,
		`API_KEYS=[abc def] region=eu`:                  `API_KEYS=[[redacted]] region=eu`,
		`"api_keys":["hunter2"]`:                        `"api_keys":[[redacted]]`,
		`"api_keys":["a]b","c"],"n":1`:                  `"api_keys":[[redacted]],"n":1`,
		`"password":["hunter2"],"n":1`:                  `"password":[[redacted]],"n":1`,
		`API_KEYS=[[a],[b]]`:                            `API_KEYS=[[[redacted]]]`,
		`{"lines":["API_KEYS=[abc, de","normal"],"n":1`: `{"lines":["API_KEYS=[[redacted]","normal"],"n":1`,
		"API_KEYS=[abc, de\nnormal":                     "API_KEYS=[[redacted]\nnormal",
		`max_tokens=[1, 2] missing_keys=[a,b]`:          `max_tokens=[1, 2] missing_keys=[a,b]`,
		`"password": {"rotated": 1`:                     `"password": {"rotated": 1`,
		`password=[abc]defghi x`:                        `password=[redacted] x`,
		`Authorization: Bearer hun}ter2, next`:          `Authorization: Bearer [redacted], next`,
	} {
		got := ScrubText(text)
		if got != want {
			t.Errorf("ScrubText(%s) = %s, want %s", text, got, want)
		}
		if again := ScrubText(got); again != got {
			t.Errorf("scrubbing %s again gave %s", got, again)
		}
	}
}

func TestScrubText_CountersWithPunctuation(t *testing.T) {
	for _, text := range []string{
		"max_tokens=10/20", "used total_tokens=4096.", "redis (evicted_keys=44) ok", `{"m":"(evicted_keys=44)"}`,
		`{"m":"max_tokens: 10/20\nok"}`, `{"m":"max_tokens=4096\nok`,
	} {
		if got := ScrubText(text); got != text {
			t.Errorf("ScrubText(%q) = %q, want it unchanged", text, got)
		}
	}
}

func scrubTime(text string) time.Duration {
	start := time.Now()
	ScrubText(text)
	return time.Since(start)
}

// Four times the input may not take much more than four times as long; a quadratic pass takes sixteen. A busy machine
// can spoil a measurement, so one good attempt is enough.
func TestScrubText_TimeGrowsWithTheInput(t *testing.T) {
	nested := func(open, inner, end string) func(int) string {
		return func(n int) string {
			k := n / (len(open) + len(end))
			return strings.Repeat(open, k) + inner + strings.Repeat(end, k)
		}
	}
	repeat := func(unit string) func(int) string {
		return func(n int) string { return strings.Repeat(unit, n/len(unit)) }
	}
	for name, build := range map[string]func(int) string{
		"plural pairs":        repeat("a_keys=b,"),
		"harmless plurals":    repeat("a_keys=1:"),
		"escaped plurals":     repeat(`a_keys=1\n`),
		"plural lists":        repeat("a_keys=[1 "),
		"secret lists":        repeat("token=["),
		"quoted lists":        repeat(`token=["a",`),
		"nested pairs":        repeat("token=token="),
		"closers":             func(n int) string { return "password=" + strings.Repeat("}", n) },
		"pairs then closers":  func(n int) string { return repeat("password=")(n/2) + strings.Repeat("}", n/2) },
		"quotes":              repeat(`password="`),
		"backslashes":         repeat(`\`),
		"password backslash":  func(n int) string { return "password=" + strings.Repeat(`\`, n) },
		"backslash pairs":     repeat(`\\\\token=`),
		"escaped quotes":      repeat(`\"token\":\"`),
		"open arrays":         repeat("["),
		"open objects":        repeat(`{"a":`),
		"nested arrays":       nested("[", "1", "]"),
		"nested objects":      nested(`{"abcdefghijklmnopqrstu":`, "1", "}"),
		"nested secret pairs": nested(`{"name":"token","value":`, "1", "}"),
		"json strings":        func(n int) string { return "[" + repeat(`"password=a",`)(n) + "1]" },
		"json lines":          repeat("{\"password\":\"a\"}\n"),
		"json in json":        func(n int) string { return encode(t, encode(t, `{"a":"`+repeat("token=a ")(n/2)+`"}`, 1), 1) },
	} {
		t.Run(name, func(t *testing.T) {
			small, large := build(50_000), build(200_000)
			var ratio float64
			for attempt := 0; attempt < 6; attempt++ {
				ratio = float64(scrubTime(large)) / float64(max(scrubTime(small), time.Microsecond))
				if ratio < 9 {
					return
				}
			}
			t.Errorf("200 KB took %.1f times as long as 50 KB", ratio)
		})
	}
}

// No six-character piece of a secret may survive, whatever encoding it went through and wherever it was cut.
func TestScrubText_EncodedRandomSecretsNeverLeak(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var alphabet []byte
	for c := byte('!'); c <= '~'; c++ {
		if !strings.ContainsRune(`"',;&`, rune(c)) {
			alphabet = append(alphabet, c)
		}
	}
	for i := 0; i < 2000; i++ {
		random := make([]byte, 8+rng.Intn(24))
		for j := range random {
			random[j] = alphabet[rng.Intn(len(alphabet))]
		}
		secret := string(random)
		line := "password=" + secret
		object := encode(t, "2026-11-24T10:00:00Z INFO login "+line+" ok\nnext line", 1)
		texts := []string{line, encode(t, line, 1), encode(t, line, 2), encode(t, line, 3)}
		for depth := 0; depth <= 2; depth++ {
			texts = append(texts, encode(t, `{"t":1,"lines":["a\tb",`+object+`],"n":2}`, depth))
		}
		for _, text := range texts {
			got := ScrubText(text)
			if text != line && !json.Valid([]byte(got)) {
				t.Fatalf("%s became invalid JSON: %s", text, got)
			}
			cut := ScrubText(text[:rng.Intn(len(text)+1)] + []string{"", "\n[cut]"}[i%2])
			for label, out := range map[string]string{"whole": got, "cut": cut} {
				// What a cut or an encoding leaves of the secret, decoded at each level.
				for _, view := range decodedForms(out) {
					for j := 0; j+6 <= len(secret); j++ {
						if piece := secret[j : j+6]; strings.Contains(view, piece) {
							t.Fatalf("%s: piece %q of %q left in %s\nfrom %s", label, piece, secret, out, text)
						}
					}
				}
			}
		}
	}
}

// decodedForms returns text and what a reader sees after removing one to three levels of JSON escapes.
func decodedForms(text string) []string {
	forms := []string{text}
	replacer := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\u003c`, "<", `\u003e`, ">", `\u0026`, "&", `\n`, "\n", `\t`, "\t")
	for i := 0; i < 3; i++ {
		text = replacer.Replace(text)
		forms = append(forms, text)
	}
	return forms
}
