package saml_test

import (
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/saml"
)

func attrs(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = []string{kv[i+1]}
	}
	return m
}

func TestDeriveUsername_Table(t *testing.T) {
	long := strings.Repeat("abcdefghij", 4)
	cases := []struct {
		name  string
		id    saml.Identity
		cfg   saml.Config
		taken map[string]bool
		want  string
	}{
		{"email local part", saml.Identity{NameID: "zzz", Attributes: attrs("email", "Alex.Doe@Example.test")}, saml.Config{}, nil, "alex.doe"},
		{"configured attribute wins", saml.Identity{NameID: "zzz", Attributes: attrs("email", "a.b@c.test", "uid", "JDoe")}, saml.Config{AttrUsername: "uid"}, nil, "jdoe"},
		{"configured attribute absent falls back to email", saml.Identity{NameID: "zzz", Attributes: attrs("email", "kim@c.test")}, saml.Config{AttrUsername: "uid"}, nil, "kim"},
		{"configured email attribute", saml.Identity{NameID: "zzz", Attributes: attrs("corpmail", "lee@c.test", "email", "other@c.test")}, saml.Config{AttrEmail: "corpmail"}, nil, "lee"},
		{"nameid fallback", saml.Identity{NameID: "pairwise-abc"}, saml.Config{}, nil, "pairwise-abc"},
		{"nameid local part", saml.Identity{NameID: "Bob@corp.test"}, saml.Config{}, nil, "bob"},
		{"illegal characters become dashes and collapse", saml.Identity{NameID: "Alex  Doe+test!!x"}, saml.Config{}, nil, "alex-doe-test-x"},
		{"leading digits dropped", saml.Identity{NameID: "123abc"}, saml.Config{}, nil, "abc"},
		{"leading separators dropped", saml.Identity{NameID: "_.-user"}, saml.Config{}, nil, "user"},
		{"all digits is empty", saml.Identity{NameID: "12345"}, saml.Config{}, nil, "sso-user"},
		{"empty", saml.Identity{}, saml.Config{}, nil, "sso-user"},
		{"40 chars truncated to 32", saml.Identity{NameID: long}, saml.Config{}, nil, long[:32]},
		{"single letter padded to valid length", saml.Identity{NameID: "q"}, saml.Config{}, nil, "q-"},
		{"collision", saml.Identity{NameID: "alex"}, saml.Config{}, map[string]bool{"alex": true}, "alex-2"},
		{"second collision", saml.Identity{NameID: "alex"}, saml.Config{}, map[string]bool{"alex": true, "alex-2": true}, "alex-3"},
		{"suffix keeps total within 32", saml.Identity{NameID: long}, saml.Config{}, map[string]bool{long[:32]: true}, long[:30] + "-2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := saml.DeriveUsername(c.id, c.cfg, func(s string) bool { return c.taken[s] })
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeriveUsername_TwoDigitSuffixTrimsBase(t *testing.T) {
	base := strings.Repeat("a", 32)
	taken := map[string]bool{base: true}
	for i := 2; i < 10; i++ {
		taken[base[:30]+"-"+string(rune('0'+i))] = true
	}
	got, err := saml.DeriveUsername(saml.Identity{NameID: base}, saml.Config{}, func(s string) bool { return taken[s] })
	if err != nil || got != base[:29]+"-10" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDeriveUsername_GivesUpAfter99(t *testing.T) {
	_, err := saml.DeriveUsername(saml.Identity{NameID: "alex"}, saml.Config{}, func(string) bool { return true })
	if saml.ReasonOf(err) != saml.ReasonUserCreateFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentity_EmailAndDisplayName_WellKnownNames(t *testing.T) {
	const (
		entraEmail = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"
		entraName  = "http://schemas.microsoft.com/identity/claims/displayname"
		oidMail    = "urn:oid:0.9.2342.19200300.100.1.3"
		oidName    = "urn:oid:2.16.840.1.113730.3.1.241"
	)
	cases := []struct {
		name         string
		attrs        map[string][]string
		cfg          saml.Config
		email, dname string // "" means nil
	}{
		{"okta", attrs("email", "a@b.test", "displayName", "Alex"), saml.Config{}, "a@b.test", "Alex"},
		{"entra claims", attrs(entraEmail, "a@b.test", entraName, "Alex E"), saml.Config{}, "a@b.test", "Alex E"},
		{"oid", attrs(oidMail, "a@b.test", oidName, "Alex O"), saml.Config{}, "a@b.test", "Alex O"},
		{"friendly name index", attrs("mail", "a@b.test", "name", "Alex F"), saml.Config{}, "a@b.test", "Alex F"},
		{"order: email before mail", attrs("mail", "second@b.test", "email", "first@b.test"), saml.Config{}, "first@b.test", ""},
		{"configured wins", attrs("email", "a@b.test", "corp", "c@b.test", "displayName", "A", "full", "Full"), saml.Config{AttrEmail: "corp", AttrDisplayName: "full"}, "c@b.test", "Full"},
		{"configured but missing does not fall back", attrs("email", "a@b.test", "displayName", "A"), saml.Config{AttrEmail: "corp", AttrDisplayName: "full"}, "", ""},
		{"blank values are absent", attrs("email", "  ", "displayName", ""), saml.Config{}, "", ""},
		{"none", attrs(), saml.Config{}, "", ""},
	}
	check := func(t *testing.T, got *string, want, what string) {
		t.Helper()
		switch {
		case want == "" && got != nil:
			t.Errorf("%s = %q, want nil", what, *got)
		case want != "" && (got == nil || *got != want):
			t.Errorf("%s = %v, want %q", what, got, want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := saml.Identity{Attributes: c.attrs}
			check(t, id.Email(c.cfg), c.email, "email")
			check(t, id.DisplayName(c.cfg), c.dname, "display name")
		})
	}
}
