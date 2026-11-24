package secretshape

import (
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

// A large log full of hex runs must be scanned in linear time: the digest and
// image tag checks look at a bounded stretch before each run, not at the whole
// text before it. longRunSpans is called directly so the other, linear, passes
// of ScrubText do not hide the difference.
func TestLongRunSpans_HexHeavyLogIsLinear(t *testing.T) {
	line := fakeHex40 + " "
	text := strings.Repeat(line, 4_000_000/len(line))

	start := time.Now()
	spans := longRunSpans(text)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("longRunSpans of %d bytes took %v, want under 2s", len(text), elapsed)
	}
	if want := len(text) / len(line); len(spans) != want {
		t.Errorf("found %d spans, want %d", len(spans), want)
	}
}
