package saml_test

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"io"
	"regexp"
	"testing"
	"time"

	crewjam "github.com/crewjam/saml"

	"github.com/rcss-labs/harbormaster/internal/saml"
)

func TestNewRequestID_FormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^_[0-9a-f]{64}$`)
	a, err := saml.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := saml.NewRequestID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}

func TestBuildRedirect_DeflatedRequestRoundTrips(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for format, want := range map[string]string{
		"email":       string(crewjam.EmailAddressNameIDFormat),
		"persistent":  string(crewjam.PersistentNameIDFormat),
		"unspecified": string(crewjam.UnspecifiedNameIDFormat),
	} {
		t.Run(format, func(t *testing.T) {
			cfg := saml.Config{IdPSSORedirectURL: "https://idp.example.test/sso?tenant=1", NameIDFormat: format}
			u, raw, err := saml.BuildRedirect(cfg, testBaseURL+"/", "_abc123", now)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if u.Host != "idp.example.test" || u.Path != "/sso" || q.Get("tenant") != "1" {
				t.Fatalf("url = %s", u)
			}
			if q.Get("RelayState") != "_abc123" || q.Get("Signature") != "" || q.Get("SigAlg") != "" {
				t.Fatalf("query = %v", q)
			}
			deflated, err := base64.StdEncoding.DecodeString(q.Get("SAMLRequest"))
			if err != nil {
				t.Fatal(err)
			}
			inflated, err := io.ReadAll(flate.NewReader(bytes.NewReader(deflated)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(inflated, raw) {
				t.Fatal("returned XML differs from the encoded request")
			}
			var ar crewjam.AuthnRequest
			if err := xml.Unmarshal(inflated, &ar); err != nil {
				t.Fatal(err)
			}
			if ar.ID != "_abc123" || ar.AssertionConsumerServiceURL != testBaseURL+"/api/auth/saml/acs" ||
				ar.Destination != cfg.IdPSSORedirectURL || ar.ProtocolBinding != crewjam.HTTPPostBinding ||
				ar.Issuer.Value != testBaseURL+"/api/auth/saml/metadata" || !ar.IssueInstant.Equal(now) ||
				ar.NameIDPolicy == nil || ar.NameIDPolicy.Format == nil || *ar.NameIDPolicy.Format != want ||
				ar.Signature != nil {
				t.Fatalf("request = %+v policy=%+v", ar, ar.NameIDPolicy)
			}
		})
	}
}

func TestBuildRedirect_RejectsBadInputs(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		cfg  saml.Config
		id   string
		base string
	}{
		"no idp url":     {saml.Config{}, "_a", testBaseURL},
		"bad request id": {saml.Config{IdPSSORedirectURL: "https://idp.example.test/sso"}, "a&b", testBaseURL},
		"empty id":       {saml.Config{IdPSSORedirectURL: "https://idp.example.test/sso"}, "", testBaseURL},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := saml.BuildRedirect(c.cfg, c.base, c.id, now); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
