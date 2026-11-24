package secretshape

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

type leakRow struct {
	text, secret string
	generated    bool // wrapped once; the others are wrapped twice
}

// leakCorpus are texts that each hold a secret the scrubber must hide, alone and inside any wrapper.
func leakCorpus() []leakRow {
	rows := []leakRow{
		// Pairs inside a hidden value that end later than it does.
		{`auth: API_KEYS=x,hunter2`, "hunter2", false},
		{`auth: secret={abc hunter2}`, "hunter2", false},
		{`secret: API_KEYS=[x hunter2]`, "hunter2", false},
		{`secret: password=[x hunter2]`, "hunter2", false},
		// Values whose end depended on the kind of name.
		{`SERVICE_TOKENS: Basic aHVudGVyMjp4`, "aHVudGVyMjp4", false},
		{`SERVICE_TOKENS=Bearer x,hunter2`, "hunter2", false},

		{`DATABASE_PASSWORD=hunter2 LOG_LEVEL=debug`, "hunter2", false},
		{`dial postgres://app:hun/ter2@db/x failed`, "hun/ter2", false},
		{`Authorization: Bearer hun}ter2`, "hun}ter2", false},
		{`x Bearer hunter2`, "hunter2", false},
		{`url=https://api.x.com/v1?api_key=hunter2&a=1`, "hunter2", false},
		{`https://x/y?a=1&token=hunter2`, "hunter2", false},
		{`line1\ntoken=hunter2`, "hunter2", false},
		{`error:password=hunter2`, "hunter2", false},
		{`password="ab\"hunter2" next=1`, "hunter2", false},
		{`password=}hunter2`, "hunter2", false},
		{`DB_PASSWORD=p[0]hunter2`, "hunter2", false},
		{`secret={abc}hunter2`, "hunter2", false},
		{`Server=db;Password=hu}nter2;X=1`, "hu}nter2", false},
		{`auth: {user}:{hunter2}`, "hunter2", false},
		{`api_key=${X}hunter2`, "hunter2", false},
		{`API_KEYS=abc,hunter2`, "hunter2", false},
		{`API_KEYS=[abc hunter2] region=eu`, "hunter2", false},
		{`"api_keys":["a]b","hunter2"],"n":1`, "hunter2", false},
		{`user[password]=hunter2 next`, "hunter2", false},
		{`secret={abc hunter2} x`, "hunter2", false},
		{`map[password:hunter2 user:bob]`, "hunter2", false},
		{`{"password":"hunter2"}`, "hunter2", false},
		{`{\"password\":\"hunter2\"}`, "hunter2", false},
		{`{"message":"{\"db\":{\"secret\":\"hunter2\"}}"}`, "hunter2", false},
		{`{"name":"DB_PASSWORD","value":"hunter2"}`, "hunter2", false},
		{`{"name":"DB_PASSWORD","value":["hunter2",{"a":"b"}]}`, "hunter2", false},
		{`{"password":123459876}`, "123459876", false},
		{`{"password":{"plain":"hunter2"}}`, "hunter2", false},
		{`{"credentials":{"user":"hunter2"}}`, "hunter2", false},
		{`{"secrets":{"db":"hunter2"}}`, "hunter2", false},
		{`{"secret":[{"plain":"hunter2"}]}`, "hunter2", false},
		{`{"password":"hunter2"}`, "hunter2", false},
		{`{"headers":{"Authorization":["Bearer hunter2"]}}`, "hunter2", false},
		{"got " + fakeBase64URL + " back", fakeBase64URL, false},
		{"env_" + fakeAWSKey + " set", fakeAWSKey, false},
		{`x\n` + fakeGitHub, fakeGitHub, false},
		{"auth " + fakeJWT + " status=200", fakeJWT, false},
	}
	const secret = "hunter2x"
	for _, name := range []string{"password", "db.password", "api_key", "AUTH_TOKEN", "secret", "credentials", "API_KEYS", "SERVICE_TOKENS"} {
		forms := []string{"@", `"@"`, `'a @'`, "[@]", "[x, @]", `["x","@"]`, "[[x],[@]]", "[a[0],@]", "Bearer @", "Basic @", "a}@", "[x @", "[@"}
		if pairNameKind(name) == nameCounter {
			forms = append(forms, "x,@", "Bearer x,@", "x,y,@")
		} else {
			forms = append(forms, "{@}", "{x @}", "{x:{y @}}", "{x @")
		}
		if namesOneSecret(name) {
			forms = append(forms, `{"a":"@"}`, `{"a":{"b":["x","@"]}}`)
		}
		for _, form := range forms {
			for _, text := range []string{name + "=" + form, name + ": " + form, `"` + name + `":` + form} {
				rows = append(rows, leakRow{strings.Replace(text, "@", secret, 1), secret, true})
			}
		}
	}
	return rows
}

// gluedRedaction is the documented case in which a second pass may hide more: text stuck to a redaction.
var gluedRedaction = regexp.MustCompile(`\[redacted\][^\s,;&"'\]}]`)

// Wrapping a text must not uncover the secret in it, and scrubbing the result again must not change it.
func TestScrubText_WrappingDoesNotUncoverSecrets(t *testing.T) {
	quote := func(text string) string { return encode(t, text, 1) }
	wrappers := map[string]func(string) string{
		"alone":    func(text string) string { return text },
		"auth":     func(text string) string { return "auth: " + text },
		"secret":   func(text string) string { return "secret: " + text },
		"token":    func(text string) string { return "token=" + text },
		"braces":   func(text string) string { return "{" + text + "}" },
		"brackets": func(text string) string { return "[" + text + "]" },
		"encoded":  quote,
		"twice":    func(text string) string { return text + " " + text },
		"log line": func(text string) string { return `{"t":1,"msg":` + quote(text) + `,"n":"after"}` },
	}
	for _, row := range leakCorpus() {
		for outer, wrapOuter := range wrappers {
			for inner, wrapInner := range wrappers {
				if row.generated && inner != "alone" {
					continue
				}
				text := wrapOuter(wrapInner(row.text))
				got := ScrubText(text)
				if strings.Contains(got, row.secret) {
					t.Errorf("%s(%s): %q left in %s\nfrom %s", outer, inner, row.secret, got, text)
				}
				if json.Valid([]byte(text)) {
					requireValid(t, text, got)
				}
				if again := ScrubText(got); again != got && !gluedRedaction.MatchString(got) {
					t.Errorf("%s(%s): scrubbing %s\nagain gave %s", outer, inner, got, again)
				}
			}
		}
	}
}
