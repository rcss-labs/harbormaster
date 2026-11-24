package secretshape

import "testing"

// Fake vendor-looking values are assembled from pieces so secret scanners
// do not flag this file.
var (
	fakeAWSKey    = "AKIA" + "IOSFODNN7EXAMPLE"
	fakeAWSTemp   = "ASIA" + "IOSFODNN7EXAMPLE"
	fakeJWT       = "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJ0ZXN0In0" + "." + "c2lnbmF0dXJlLWZha2U"
	fakeSK        = "sk-" + "abcdefghijklmnopqrstuvwx"
	fakeAnthropic = "sk-ant-" + "api03-abcdefghijklmnopqrstuvwx"
	fakeGitHub    = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	fakeGitHubOA  = "gho_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	fakeSlackBot  = "xoxb-" + "123456789012-abcdefghijkl"
	fakeSlackUser = "xoxp-" + "123456789012-abcdefghijkl"
	fakeGoogle    = "AIza" + "SyAbcdefghijklmnopqrstuvwxyz012345"
	fakeHM        = "0123456789abcdefABCDEF"
	fakeMixed     = "Zm9vYmFyQmF6" + "QXV0aDB4OTlYWXo" + "1Mj+/abcdEFGH"
	fakeHex40     = "0123456789abcdef0123456789abcdef01234567"
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
