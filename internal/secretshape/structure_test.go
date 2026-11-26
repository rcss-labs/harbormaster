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
		{`"a\tauth_key=hunter2"`, "hunter2"},
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
		`API_KEYS=[[a],[b]]`:                            `API_KEYS=[[redacted]]`,
		`{"lines":["API_KEYS=[abc, de","normal"],"n":1`: `{"lines":["API_KEYS=[[redacted]","normal"],"n":1`,
		"API_KEYS=[abc, de\nnormal":                     "API_KEYS=[[redacted]\nnormal",
		`max_tokens=[1, 2] missing_keys=[a,b]`:          `max_tokens=[1, 2] missing_keys=[a,b]`,
		`"auth": {"rotated": 1`:                         `"auth": {"rotated": 1`,
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
		"open braces":         repeat("secret={"),
		"open brace objects":  repeat(`"secret":{"`),
		"nested secrets":      nested(`{"a_long_secret_name":`, "1", "}"),
		"bracketed names":     repeat("a[b]"),
		"hex runs":            repeat(fakeHex40 + " "),
		"plurals after auth":  func(n int) string { return "auth: " + repeat("a_keys=b,")(n) },
		"token of brackets":   func(n int) string { return "token=" + strings.Repeat("[", n/2) + strings.Repeat("]", n/2) },
		"lists in lists":      nested("a_keys=[", "x", "]"),
		"objects in objects":  nested(`"secret":{"a" `, "x", "}"),
		"plural braces":       nested("secret={a", "x", "}"),
		"colons":              repeat("token:x:"),
		"plural colons":       repeat("a_keys=x:"),
		"schemes":             repeat("token=Bearer "),
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
	requireEncodedSecretsHidden(t, ScrubText)
}

func requireEncodedSecretsHidden(t *testing.T, scrubText func(string) string) {
	t.Helper()
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
			got := scrubText(text)
			if text != line && !json.Valid([]byte(got)) {
				t.Fatalf("%s became invalid JSON: %s", text, got)
			}
			cut := scrubText(text[:rng.Intn(len(text)+1)] + []string{"", "\n[cut]"}[i%2])
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

// Names in free text and JSON members: strong words are secret anywhere, weak ones unless a benign word follows.
func TestScrubText_NamesThatAreNotSecret(t *testing.T) {
	for _, text := range []string{
		`secret_names=a`, `secretArn=x`, `password_file=/run/x`, `authtoken_ttl=5m`, `{"passwordPolicy":"strict","secretType":"a"}`,
		`{"credential_count":4,"dsn_version":"a","pwd_length":12}`, `private_ip=10.0.0.1`, `{"privateIps":["10.0.0.1"]}`,
		`{"nextForwardToken":"f/1","nextBackwardToken":"b/1"}`,
		`{"authorizationConfig":{"accessPointId":"fsap-1","iam":"ENABLED"}}`,
		`{"credentialsParameter":"arn:aws:secretsmanager:eu-west-1:1:secret:a"}`,
		`{"repositoryCredentials":{"credentialsParameter":"arn:aws:secretsmanager:eu-west-1:1:secret:a"}}`,
		`{"secrets":{"db":"arn:aws:secretsmanager:eu-west-1:1:secret:a","total":3}}`,
		`{"key":"Environment","value":"prod"}`, `{"key":"cpu","values":[1,2]}`, `key=prod x`, `?key=abc`,
		`{"nextToken":"abc"}`, `{"next_token":"abc"}`, `{"pageToken":"abc","continuationToken":"d"}`,
		`{"nextPageToken":"abc","pagination_token":"a","cursorToken":"b"}`, `next_token=abc`,
		`{"name":"auth-service","value":12}`, `{"name":"auth-service","value":"abc"}`,
		`{"name":"password-resets","value":12}`, `{"name":"max_tokens","value":4096}`, `{"name":"max_tokens","value":"4096"}`,
		`{"name":"nextToken","value":"abc"}`, `{"key":"cost-center","value":"a1"}`,
		`{"private_subnets":["subnet-1"]}`, `private_subnets=subnet-1`, `cert_path=/etc/tls`, `token_ttl=15m`, `key_id=abc`,
		`{"cert_arn":"x","tokenType":"bearer-a","keyName":"b","auth_status":"ok","token_limit":5,"keyHash":"ab"}`,
		`{"auth_enabled":"yes","auth_service":"b","token_rate":4,"key_fingerprint":"ab:cd"}`,
		`{"auth_enabled":true,"password":false,"secret":null}`, `{"auth":{"mode":"sso","n":1}}`,
		`{"secrets":[{"name":"DB","valueFrom":"arn:aws:secretsmanager:eu-west-1:123456789012:secret:db-AbCdEf"}]}`,
		`{"secretOptions":[{"name":"splunk","valueFrom":"arn:aws:ssm:eu-west-1:123456789012:parameter/a"}]}`,
		`{"secret":{"from":"arn:aws:secretsmanager:eu-west-1:123456789012:secret:a","also":"{{resolve:ssm:/a/b}}","on":true}}`,
		`[auth]: starting worker`, `app[auth]: starting`, `labels[key]=prod`,
	} {
		if got := ScrubText(text); got != text {
			t.Errorf("ScrubText(%s) = %s, want it unchanged", text, got)
		}
	}
}

func TestScrubText_NamesThatAreSecret(t *testing.T) {
	for text, want := range map[string]string{
		`{"credentialsParameter":"abc"}`:                   `{"credentialsParameter":"[redacted]"}`,
		`{"authorizationConfig":"abc"}`:                    `{"authorizationConfig":"[redacted]"}`,
		`{"credentials":{"user":"a","n":3,"password":4}}`:  `{"credentials":{"user":"[redacted]","n":3,"password":"[redacted]"}}`,
		`{"passwords":[{"a":"b"}],"x":1}`:                  `{"passwords":[{"a":"[redacted]"}],"x":1}`,
		`{"secrets":{"db":"x1","more":["y",{"z":"w"}]}}`:   `{"secrets":{"db":"[redacted]","more":["[redacted]",{"z":"[redacted]"}]}}`,
		`{"app_secrets":{"db":"x1"}}`:                      `{"app_secrets":{"db":"[redacted]"}}`,
		`user[password]: x next`:                           `user[password]: [redacted] next`,
		`password_hash=abc secret_id=d`:                    `password_hash=[redacted] secret_id=[redacted]`,
		`forward_token=abc`:                                `forward_token=[redacted]`,
		`token_value=abc123`:                               `token_value=[redacted]`,
		`{"private_key_data":"abc","client-key-data":"d"}`: `{"private_key_data":"[redacted]","client-key-data":"[redacted]"}`,
		`auth_token=abc`:                                   `auth_token=[redacted]`,
		`{"auth_token":"abc","authToken":5}`:               `{"auth_token":"[redacted]","authToken":"[redacted]"}`,
		`signing_key=abc`:                                  `signing_key=[redacted]`,
		`{"token":"abc","auth":"d"}`:                       `{"token":"[redacted]","auth":"[redacted]"}`,
		`clientsecret=abc dbpwd=d myapikey: e`:             `clientsecret=[redacted] dbpwd=[redacted] myapikey: [redacted]`,
		`session_next_token=abc`:                           `session_next_token=[redacted]`,

		`{"name":"db.password","value":"a1"}`:                           `{"name":"db.password","value":"[redacted]"}`,
		`{"name":"db.password","value":12}`:                             `{"name":"db.password","value":"[redacted]"}`,
		`{"value":"a1","key":"X-Api-Key"}`:                              `{"value":"[redacted]","key":"X-Api-Key"}`,
		`{"key":"API_KEY","value":"a1"}`:                                `{"key":"API_KEY","value":"[redacted]"}`,
		`{"name":"AUTH_SERVICE","value":12}`:                            `{"name":"AUTH_SERVICE","value":"[redacted]"}`,
		`{"name":"api_keys","value":"abc"}`:                             `{"name":"api_keys","value":"[redacted]"}`,
		`{"name":"api_keys","value":["abc"]}`:                           `{"name":"api_keys","value":["[redacted]"]}`,
		`{"name":"max_tokens","value":{"name":"DB_PASS","value":"a1"}}`: `{"name":"max_tokens","value":{"name":"DB_PASS","value":"[redacted]"}}`,

		`{"password":{"plain":"x1","n":[1,"y"],"on":true}}`:                                   `{"password":{"plain":"[redacted]","n":["[redacted]","[redacted]"],"on":true}}`,
		`{"secret":[{"plain":"x1"}]}`:                                                         `{"secret":[{"plain":"[redacted]"}]}`,
		`{"db_secret":{"name":"x1","arn":"arn:aws:s3:::a b"}}`:                                `{"db_secret":{"name":"[redacted]","arn":"[redacted]"}}`,
		`{"secret":"arn:aws:secretsmanager:eu-west-1:1:secret:a"}`:                            `{"secret":"[redacted]"}`,
		`{"secret":{"name":"a","value":"arn:aws:ssm:x:1:y","valueFrom":"arn:aws:ssm:x:1:z"}}`: `{"secret":{"name":"a","value":"arn:aws:ssm:x:1:y","valueFrom":"arn:aws:ssm:x:1:z"}}`,

		`user[password]=x next`:                        `user[password]=[redacted] next`,
		`a[b]['auth_token'] = 'x y' next`:              `a[b]['auth_token'] = '[redacted]' next`,
		`secret={abc def} x`:                           `secret=[redacted] x`,
		`auth: {user a}:{pw} x`:                        `auth: [redacted] x`,
		`{"m":"secret={abc def}","n":"after"}`:         `{"m":"secret=[redacted]","n":"after"}`,
		`{"m":"secret={abc def","n":"after"`:           `{"m":"secret=[redacted]","n":"after"`,
		`"password":{"plain":"a}1","n":{"x":2}},"b":3`: `"password":[redacted],"b":3`,
		`"auth":{"mode":"sso"},"b":3`:                  `"auth":{"mode":"sso"},"b":3`,
		`"name":"db.password","value":"a1","n":1`:      `"name":"db.password","value":"[redacted]","n":1`,
		`"name":"auth-service","value":12,"n":1`:       `"name":"auth-service","value":12,"n":1`,
	} {
		got := ScrubText(text)
		if got != want {
			t.Errorf("ScrubText(%s)\n got %s\nwant %s", text, got, want)
		}
		if json.Valid([]byte(text)) {
			requireValid(t, text, got)
		}
		if again := ScrubText(got); again != got {
			t.Errorf("scrubbing %s again gave %s", got, again)
		}
	}
}
