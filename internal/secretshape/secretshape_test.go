package secretshape

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// Fake vendor-looking values are assembled from pieces so secret scanners
// do not flag this file.
var (
	fakeAWSKey      = "AKIA" + "IOSFODNN7EXAMPLE"
	fakeAWSTemp     = "ASIA" + "IOSFODNN7EXAMPLE"
	fakeJWT         = "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJ0ZXN0In0" + "." + "c2lnbmF0dXJlLWZha2U"
	fakeSK          = "sk-" + "abcdefghijklmnopqrstuvwx"
	fakeAnthropic   = "sk-ant-" + "api03-abcdefghijklmnopqrstuvwx"
	fakeGitHub      = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	fakeGitHubOA    = "gho_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	fakeSlackBot    = "xoxb-" + "123456789012-abcdefghijkl"
	fakeSlackUser   = "xoxp-" + "123456789012-abcdefghijkl"
	fakeGoogle      = "AIza" + "SyAbcdefghijklmnopqrstuvwxyz012345"
	fakeHM          = "0123456789abcdefABCDEF"
	fakeMixed       = "Zm9vYmFyQmF6" + "QXV0aDB4OTlYWXo" + "1Mj+/abcdEFGH"
	fakeHex40       = "0123456789abcdef0123456789abcdef01234567"
	fakeBase64URL   = "kQ9x_Tz3-Lm8vWb2Nc7Yd4Re1Uf6Hg0Js5Ap" + "Xo-Qi_Zr"
	fakeBase64Slash = "Zm9vYmFy/QmF6QXV0aDB4OTlYWXo1Mj+abcdEFGHijKL0123" + "4567=="
)

func TestNameLooksSecret(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"DATABASE_PASSWORD", true},
		{"api-key", true},
		{"STRIPE_SECRET", true},
		{"AUTH_TOKEN", true},
		{"apikey", true},
		{"db.passwd", true},
		{"GOOGLE_CREDENTIALS", true},
		{"tls_cert", true},
		{"SENTRY_DSN", true},
		{"private-key", true},
		{"KEYBOARD_LAYOUT", false},
		{"Authorization", true},
		{"apiKey", true},
		{"AUTHOR", false},
		{"LOG_LEVEL", false},
		{"DATABASE_HOST", false},
		{"", false},
		{"API_KEYS", true},
		{"AUTH_TOKENS", true},
		{"APP_SECRETS", true},
		{"ADMIN_PASSWORDS", true},
		{"DB_PASS", true},
		{"GPG_PASSPHRASE", true},
		{"PGPASSWORD", true},
		{"DBPASSWORD", true},
		{"MYSQL_ROOTPASSWD", true},
		{"SECRETKEY", true},
		{"ACCESSKEY", true},
		{"PRIVATEKEY", true},
		{"APIKEYS", true},
		{"BYPASS_CACHE", false},
		{"PASSIVE_MODE", false},
		{"KEYSPACE", false},
		{"COMPASS_URL", false},
	}
	for _, tt := range tests {
		if got := NameLooksSecret(tt.name); got != tt.want {
			t.Errorf("NameLooksSecret(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestValueLooksSecret(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"aws access key", fakeAWSKey, true},
		{"aws temporary key", fakeAWSTemp, true},
		{"pem header", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"jwt", fakeJWT, true},
		{"openai style", fakeSK, true},
		{"anthropic style", fakeAnthropic, true},
		{"github pat", fakeGitHub, true},
		{"github oauth", fakeGitHubOA, true},
		{"slack bot", fakeSlackBot, true},
		{"slack user", fakeSlackUser, true},
		{"google", fakeGoogle, true},
		{"hmp token", "hmp_" + fakeHM, true},
		{"hmo token", "hmo_" + fakeHM, true},
		{"hmr token", "hmr_" + fakeHM, true},
		{"hma token", "hma_" + fakeHM, true},
		{"url with password", "postgres://app:hunter2@db/x", true},
		{"mixed class run", fakeMixed, true},
		{"long hex", fakeHex40, true},
		{"region", "eu-west-1", false},
		{"host and port", "db.internal:5432", false},
		{"bool", "true", false},
		{"uuid", "123e4567-e89b-12d3-a456-426614174000", false},
		{"plain url", "https://example.com/path", false},
		{"url without password", "postgres://app@db/x", false},
		{"short hex", "0123456789abcdef", false},
		{"long lowercase path", "/usr/local/share/some/very/long/directory/name", false},
		{"sha256 digest", "sha256:" + strings.Repeat("ab12", 16), false},
		{"digest with at", "repo@sha256:" + strings.Repeat("ab12", 16), false},
		{"arn", "arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/My-Tg/73e2d6bc24d8a067", false},
		{"ecs task id", "0123456789abcdef0123456789abcdef", false},
		{"image tag with 40 hex", "registry.example.com/team/app:" + fakeHex40, false},
		{"pod name", "payments-api-deployment-7d9f8b6c5d-x2k4j", false},
		{"dated file url", "https://example.com/files/2026-11-24-run-01.tar", false},
		{"base64url with separators", fakeBase64URL, true},
		{"standard base64 with slash", fakeBase64Slash, true},
		{"prefixed by underscore aws", "env_" + fakeAWSKey, true},
		{"prefixed by underscore sk", "x_" + fakeSK, true},
		{"url password with slash", "postgres://app:pa/ss" + "word@db/x", true},
		{"url with port and email", "http://host:8080/path?email=a@b.example", false},
		{"task definition name", "task-definition-payments-api-prod", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		if got := ValueLooksSecret(tt.value); got != tt.want {
			t.Errorf("%s: ValueLooksSecret(%q) = %v, want %v", tt.name, tt.value, got, tt.want)
		}
	}
}

func TestScrubText_ReplacesOnlyTheSecretSpan(t *testing.T) {
	in := "2026-11-24T10:00:00Z level=info msg=\"calling upstream\" auth " + fakeJWT + " status=200"
	want := "2026-11-24T10:00:00Z level=info msg=\"calling upstream\" auth " + Redacted + " status=200"
	if got := ScrubText(in); got != want {
		t.Errorf("ScrubText:\n got %q\nwant %q", got, want)
	}
}

func TestScrubText_NamedPairs(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"equals", "DATABASE_PASSWORD=hunter2 LOG_LEVEL=debug", "DATABASE_PASSWORD=" + Redacted + " LOG_LEVEL=debug"},
		{"json", `{"api-key": "abc", "region": "eu-west-1"}`, `{"api-key": "` + Redacted + `", "region": "eu-west-1"}`},
		{"colon", "token: abc123", "token: " + Redacted},
		{"url password", "dial postgres://app:hunter2@db/x failed", "dial postgres://app:" + Redacted + "@db/x failed"},
	}
	for _, tt := range tests {
		if got := ScrubText(tt.in); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestScrubText_LeavesCleanTextIdentical(t *testing.T) {
	in := "2026-11-24T10:00:00Z level=warn msg=\"task stopped\" region=eu-west-1 id=123e4567-e89b-12d3-a456-426614174000 host=db.internal:5432 author=bob\n\tkeyboard=us\n"
	if got := ScrubText(in); got != in {
		t.Errorf("ScrubText changed clean text:\n got %q\nwant %q", got, in)
	}
}

func TestScrubText_KeepsDiagnosticIdentifiersIntact(t *testing.T) {
	tests := []struct{ name, in string }{
		{"pod name", "pod payments-api-deployment-7d9f8b6c5d-x2k4j restarted"},
		{"go test name", "--- FAIL: TestScrubText_ReplacesOnlyTheSecretSpan_WithLongerNames/case_two (0.00s)"},
		{"dated url", "fetch https://example.com/files/2026-11-24-run-01.tar ok"},
		{"arn with mixed case target group", "target arn:aws:elasticloadbalancing:eu-west-1:123456789012:targetgroup/My-Tg/73e2d6bc24d8a067 healthy"},
		{"log stream", "stream prefix/container/0123456789abcdef0123456789abcdef"},
		{"sha256 digest", "image sha256:" + strings.Repeat("ab12", 16)},
		{"digest after at", "image repo/app@sha256:" + strings.Repeat("ab12", 16)},
		{"image reference with 40 hex tag", "pull registry.example.com/team/app:" + fakeHex40 + " done"},
		{"ecs task id", "task 0123456789abcdef0123456789abcdef stopped"},
		{"timestamped line", "2026-11-24T10:00:00.123Z level=info msg=\"deploy finished\" duration=3.2s"},
		{"stack frame", "\t/usr/local/go/src/net/http/server.go:2136 +0x1a2b"},
	}
	for _, tt := range tests {
		if got := ScrubText(tt.in); got != tt.in {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.in)
		}
	}
}

func TestScrubText_FindsSecretsInsideOtherText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"query string in non-secret pair", "url=https://api.x.com/v1?api_key=abc123 status=200", "url=https://api.x.com/v1?api_key=" + Redacted + " status=200"},
		{"nested pair", "error:password=hunter2", "error:password=" + Redacted},
		{"bearer", "Authorization: Bearer abc", "Authorization: Bearer " + Redacted},
		{"basic", "Authorization: Basic dXNlcg==", "Authorization: Basic " + Redacted},
		{"json authorization", `{"authorization": "Bearer abc"}`, `{"authorization": "` + Redacted + `"}`},
		{"json pair", `{"data": {"client_secret": "abc"}, "ok": true}`, `{"data": {"client_secret": "` + Redacted + `"}, "ok": true}`},
		{"escaped quote in value", `password="ab\"cd" next=1`, `password="` + Redacted + `" next=1`},
		{"base64url secret", "got " + fakeBase64URL + " back", "got " + Redacted + " back"},
		{"url password with slash", "dial postgres://app:pa/ss" + "word@db/x failed", "dial postgres://app:" + Redacted + "@db/x failed"},
		{"underscore prefixed key", "env_" + fakeAWSKey + " set", "env_" + Redacted + " set"},
		{"secret named pair keeps arn exemption off", "secret=arn:aws:secretsmanager:eu-west-1:123456789012:secret:db-AbCdEf", "secret=" + Redacted},
	}
	for _, tt := range tests {
		if got := ScrubText(tt.in); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

// Real tokens are prefix + 12 base32 characters + "_" + 43 base64url characters (see apitoken.Generate).
func TestHarbormasterTokenShape(t *testing.T) {
	const id = "abcd2345efgh"
	// The first secret has no random-looking run, so only the token pattern can catch it.
	secrets := []string{strings.Repeat("a", 43), "Qk3Zx9Lm2Pv7Rt4Wn8Yb5Jc1Hd6Gf0Sa-eAbCdEfGhI"}
	for _, prefix := range []string{"hmp_", "hmo_", "hmr_", "hma_"} {
		for _, secret := range secrets {
			if len(secret) != 43 {
				t.Fatalf("fixture secret is %d characters", len(secret))
			}
			token := prefix + id + "_" + secret
			if !ValueLooksSecret(token) {
				t.Errorf("%s token not detected", prefix)
			}
			if got := ScrubText("Authorization failed for " + token + " today"); got != "Authorization failed for "+Redacted+" today" {
				t.Errorf("%s token: scrubbed to %q", prefix, got)
			}
		}
	}
}

func TestNameLooksSecret_GluedEnvironmentNames(t *testing.T) {
	for name, want := range map[string]bool{
		"DBPASS": true, "SMTPPASS": true, "CLIENTSECRET": true, "AUTHTOKEN": true, "ACCESSTOKEN": true,
		"SECRETACCESSKEY": true, "AWSSECRETKEY": true, "CONNECTION_STRING": true, "connectionString": true,
		"BYPASS_CACHE": false, "COMPASS_URL": false, "PASSTHROUGH": false, "PASSENGER_APP": false,
		"TOKENIZER_MODEL": false, "KEYSPACE": false, "KEYSTORE_PATH": false, "PASSWORDLESS_LOGIN": false,
		"SECRETSMANAGER_ENDPOINT": false, "AWS_REGION": false, "BYPASS": false, "COMPASS": false,
	} {
		if got := NameLooksSecret(name); got != want {
			t.Errorf("NameLooksSecret(%q) = %v, want %v", name, got, want)
		}
	}
}

// Free text is judged by a narrower name rule than environment names: counters and test output stay readable.
func TestScrubText_FreeTextCountersAndWordsStayIdentical(t *testing.T) {
	for _, text := range []string{
		"max_tokens=4096", "tokens: 1200", "total_tokens=900 input_tokens=3",
		`{"max_tokens":4096,"usage":{"output_tokens": 4}}`, "ratelimit-remaining-tokens: 149000",
		"keys: 12 expired", "evicted keys=44", "missing keys: a", "secrets: 4 loaded", "pass: 3 fail: 1", "pass=true",
		"--- PASS: TestX", "health check pass: ok", "proxy_pass: http://backend:8080",
	} {
		if got := ScrubText(text); got != text {
			t.Errorf("ScrubText(%q) = %q, want it unchanged", text, got)
		}
	}
}

func TestScrubText_FreeTextSecretNamesStillRedacted(t *testing.T) {
	for text, leaked := range map[string]string{
		"DB_PASS=1234": "1234", "smtp.pass=hunter2": "hunter2", "password: 99887766": "99887766",
		"API_KEYS=abc,def": "def", `{"client_secret":"x1"}`: "x1", "passphrase=open sesame": "open",
		"DB_PASSWORDS=hunter2": "hunter2", "pgpassword=hunter2": "hunter2", "MYSQL_ROOTPASSWD=hunter2": "hunter2",
		"aws_secret_access_key=hunter2": "hunter2", "AUTH_TOKENS=hunter2": "hunter2",
	} {
		if got := ScrubText(text); strings.Contains(got, leaked) {
			t.Errorf("ScrubText(%q) = %q, leaked %q", text, got, leaked)
		}
	}
}

func TestScrubText_UnquotedValueStopsAtClosingBrackets(t *testing.T) {
	for text, want := range map[string]string{
		`{"password": hunter2}`:          `{"password": ` + Redacted + `}`,
		`["token=hunter2"]`:              `["token=` + Redacted + `"]`,
		`{"a":{"secret":hunter2},"b":1}`: `{"a":{"secret":` + Redacted + `},"b":1}`,
		`{"password":hunter2}}`:          `{"password":` + Redacted + `}}`,
		`{"x":[password=hunter2]}`:       `{"x":[password=` + Redacted + `]}`,
		`{"password":}`:                  `{"password":}`,
	} {
		if got := ScrubText(text); got != want {
			t.Errorf("ScrubText(%q) = %q, want %q", text, got, want)
		}
	}
}

// Brackets inside a secret are part of it: nothing after the first one may leak.
func TestScrubText_BracketsInsideSecretsDoNotTruncate(t *testing.T) {
	for text, want := range map[string]string{
		"password=}abcdef":                "password=" + Redacted,
		"password=]abcdef":                "password=" + Redacted,
		"password=ab}cd-ef":               "password=" + Redacted,
		"password: a]bcdef":               "password: " + Redacted,
		"DB_PASSWORD=p[0]rest":            "DB_PASSWORD=" + Redacted,
		"secret={abc}def":                 "secret=" + Redacted,
		"pwd=x}}yz":                       "pwd=" + Redacted,
		"secret_tokens=a}b":               "secret_tokens=" + Redacted,
		"token=abc}def&x=1":               "token=" + Redacted + "&x=1",
		"https://x/y?token=ab]cd":         "https://x/y?token=" + Redacted,
		"Server=db;Password=hu}nter2;X=1": "Server=db;Password=" + Redacted + ";X=1",
		"auth: {user}:{pw}":               "auth: " + Redacted,
		"api_key=${X}realtail":            "api_key=" + Redacted,
	} {
		got := ScrubText(text)
		if got != want {
			t.Errorf("ScrubText(%q) = %q, want %q", text, got, want)
		}
	}
}

// Log lines arrive as JSON strings inside JSON: escaped quotes must not hide a pair.
func TestScrubText_EscapedJSON(t *testing.T) {
	for text, want := range map[string]string{
		`{\"password\":\"hunter2\"}`:               `{\"password\":\"` + Redacted + `\"}`,
		`{\\\"password\\\":\\\"hunter2\\\"}`:       `{\\\"password\\\":\\\"` + Redacted + `\\\"}`,
		`\"token\": \"abc\\\"def\"`:                `\"token\": \"` + Redacted + `\"`,
		`"message":"{\"db\":{\"secret\":\"x1\"}}"`: `"message":"{\"db\":{\"secret\":\"` + Redacted + `\"}}"`,
	} {
		if got := ScrubText(text); got != want {
			t.Errorf("ScrubText(%s) = %s, want %s", text, got, want)
		}
	}
	answer := `{"lines":["{\"password\":\"hunter2\"}","x"],"message":"{\"db\":{\"secret\":\"x1\"}}","t":"\"token\": \"abc\\\"def\"","n":1}`
	if !json.Valid([]byte(answer)) {
		t.Fatal("fixture is not valid JSON")
	}
	got := ScrubText(answer)
	if !json.Valid([]byte(got)) {
		t.Errorf("scrubbed answer is not valid JSON: %s", got)
	}
	for _, leaked := range []string{"hunter2", "x1", "abc"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q survived in %s", leaked, got)
		}
	}
}

func TestScrubText_CounterNamesJudgeTheFirstItem(t *testing.T) {
	for _, text := range []string{
		`"missing_keys":["a","b"]`, "max_tokens=-5", "tokens: 1.5", "total_tokens=12k", "ttl_secrets: 30s", "keys: 90%",
		"api_keys=n/m", "api_keys=null", "api_keys=false,true", "api_keys=12,34", "api_keys=1.5ms,2ms",
	} {
		if got := ScrubText(text); got != text {
			t.Errorf("ScrubText(%q) = %q, want it unchanged", text, got)
		}
	}
	if got := ScrubText("API_KEYS=abc,def"); strings.Contains(got, "def") || strings.Contains(got, "abc") {
		t.Errorf("a list whose first item is not a number leaked: %q", got)
	}
}

func TestScrubText_RandomSecretsNeverLeakASixCharacterPiece(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var alphabet []byte
	for c := byte('!'); c <= '~'; c++ {
		if !strings.ContainsRune(`"',;&`, rune(c)) {
			alphabet = append(alphabet, c)
		}
	}
	for i := 0; i < 2000; i++ {
		secret := make([]byte, 8+rng.Intn(24))
		for j := range secret {
			secret[j] = alphabet[rng.Intn(len(alphabet))]
		}
		out := ScrubText("password=" + string(secret))
		for j := 0; j+6 <= len(secret); j++ {
			if piece := string(secret[j : j+6]); strings.Contains(out, piece) {
				t.Fatalf("secret %q: piece %q left in %q", secret, piece, out)
			}
		}
	}
}

// Whether a hex run is a digest or an image tag depends only on a short
// stretch before it. The check for a run at the end of a 4 MB log must not
// read the megabytes before it: a scan that did would take seconds here, one
// that does not takes microseconds, on any machine and under -race.
func TestHexRunIsDiagnostic_DoesNotReadTheWholeLog(t *testing.T) {
	line := fakeHex40 + " "
	text := strings.Repeat(line, 4_000_000/len(line))
	last := len(text) - len(line)

	start := time.Now()
	for i := 0; i < 100_000; i++ {
		if hexRunIsDiagnostic(text, last) {
			t.Fatal("a plain hex run is not a digest or image tag")
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("100,000 checks late in a %d byte log took %v, want well under a second", len(text), elapsed)
	}

	for name, before := range map[string]string{"digest": "image sha256:", "image tag": "pull registry.example.com/team/app:"} {
		if !hexRunIsDiagnostic(before+fakeHex40, len(before)) {
			t.Errorf("%s: not recognised", name)
		}
	}
}

// ScrubText of a 200 KB hex-heavy log finishes, redacting every run.
func TestScrubText_HexHeavyLogFinishes(t *testing.T) {
	text := strings.Repeat(fakeHex40+" ", 200_000/(len(fakeHex40)+1))
	if got := ScrubText(text); strings.Contains(got, "0123456789abcdef") {
		t.Errorf("hex runs were not redacted")
	}
}
