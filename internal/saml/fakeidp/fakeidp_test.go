package fakeidp

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"

	"github.com/rcss-labs/harbormaster/internal/saml"
)

const testBase = "http://127.0.0.1:8480"

func newFake(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{BaseURL: testBase})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newSP builds a crewjam service provider that trusts the fake IdP's metadata.
func newSP(t *testing.T, f *Server) *crewjam.ServiceProvider {
	t.Helper()
	md := &crewjam.EntityDescriptor{}
	if err := xml.Unmarshal(f.Metadata(), md); err != nil {
		t.Fatalf("parse fake metadata: %v", err)
	}
	return &crewjam.ServiceProvider{
		EntityID:    saml.SPEntityID(testBase),
		MetadataURL: mustURL(t, saml.MetadataURL(testBase)),
		AcsURL:      mustURL(t, saml.ACSURL(testBase)),
		IDPMetadata: md,
	}
}

func mustURL(t *testing.T, raw string) url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}

// authnRequest returns the request XML, its ID, and the deflated+base64 form
// used in the redirect binding.
func authnRequest(t *testing.T, sp *crewjam.ServiceProvider, f *Server) (xmlBytes []byte, id, redirectParam string) {
	t.Helper()
	req, err := sp.MakeAuthenticationRequest(f.ssoURL(), crewjam.HTTPRedirectBinding, crewjam.HTTPPostBinding)
	if err != nil {
		t.Fatal(err)
	}
	xmlBytes = mustMarshal(t, req)
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = zw.Write(xmlBytes)
	_ = zw.Close()
	return xmlBytes, req.ID, base64.StdEncoding.EncodeToString(buf.Bytes())
}

func mustMarshal(t *testing.T, req *crewjam.AuthnRequest) []byte {
	t.Helper()
	doc := etree.NewDocument()
	doc.SetRoot(req.Element())
	b, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parse(t *testing.T, sp *crewjam.ServiceProvider, respB64, requestID string) error {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(respB64)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sp.ParseXMLResponse(raw, []string{requestID}, mustURL(t, saml.ACSURL(testBase)))
	return err
}

func TestFakeIdP_MetadataParsesWithInternalSaml(t *testing.T) {
	f := newFake(t)
	md, err := saml.ParseIdPMetadata(f.Metadata(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if md.EntityID != testBase+"/fake-idp" || f.EntityID() != md.EntityID {
		t.Fatalf("entity ID = %q / %q", md.EntityID, f.EntityID())
	}
	if md.SSORedirectURL != testBase+"/fake-idp/sso" {
		t.Fatalf("redirect URL = %q", md.SSORedirectURL)
	}
	if len(md.SigningCerts) != 1 {
		t.Fatalf("signing certs = %d", len(md.SigningCerts))
	}
}

func TestFakeIdP_ResponseVerifiesWithCrewjam(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	reqXML, id, _ := authnRequest(t, sp, f)

	good, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), Tamper{})
	if err != nil {
		t.Fatal(err)
	}
	if err := parse(t, sp, good, id); err != nil {
		t.Fatalf("well-formed response rejected: %v", err)
	}

	bad, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), Tamper{SignWithWrongKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := parse(t, sp, bad, id); err == nil {
		t.Fatal("response signed with the wrong key was accepted")
	}
}

func TestFakeIdP_AttributesAndNameID(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	reqXML, id, _ := authnRequest(t, sp, f)
	resp, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), Tamper{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(resp)
	a, err := sp.ParseXMLResponse(raw, []string{id}, mustURL(t, saml.ACSURL(testBase)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Subject.NameID.Value != "alex@example.test" || a.Subject.NameID.Format != string(crewjam.EmailAddressNameIDFormat) {
		t.Fatalf("NameID = %+v", a.Subject.NameID)
	}
	got := map[string]string{}
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			got[at.Name] = at.Values[0].Value
		}
	}
	want := map[string]string{"email": "alex@example.test", "displayName": "Alex", "givenName": "Alex", "sn": "Example"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestFakeIdP_TamperedResponsesFailCrewjam(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	reqXML, id, _ := authnRequest(t, sp, f)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	cases := map[string]Tamper{
		"unsigned":       {OmitSignature: true},
		"audience":       {Audience: "https://other-sp.example/metadata"},
		"destination":    {Destination: "https://evil.example/acs"},
		"recipient":      {Recipient: "https://evil.example/acs"},
		"in_response_to": {InResponseTo: "_not-the-request"},
		"expired":        {NotOnOrAfter: &past},
		"not_yet_valid":  {NotBefore: &future},
		"status":         {Status: "urn:oasis:names:tc:SAML:2.0:status:Responder"},
		"wrong_key":      {SignWithWrongKey: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), tc)
			if err != nil {
				t.Fatal(err)
			}
			if err := parse(t, sp, resp, id); err == nil {
				t.Fatal("response was accepted")
			}
		})
	}
}

func TestFakeIdP_StructuralKnobs(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	reqXML, _, _ := authnRequest(t, sp, f)
	decode := func(tc Tamper) string {
		resp, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), tc)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(resp)
		return string(raw)
	}
	if n := strings.Count(decode(Tamper{DuplicateAssertion: true}), "<saml:Assertion "); n != 2 {
		t.Errorf("DuplicateAssertion: %d assertions", n)
	}
	if n := strings.Count(decode(Tamper{}), "<saml:Assertion "); n != 1 {
		t.Errorf("default: %d assertions", n)
	}
	if strings.Contains(decode(Tamper{OmitAuthnStatement: true}), "AuthnStatement") {
		t.Error("OmitAuthnStatement left an AuthnStatement")
	}
	if strings.Contains(decode(Tamper{OmitNameID: true}), "<saml:NameID") {
		t.Error("OmitNameID left a NameID")
	}
	if !strings.Contains(decode(Tamper{NameIDFormat: "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"}), "nameid-format:transient") {
		t.Error("NameIDFormat not applied")
	}
	sess := time.Now().Add(2 * time.Hour).UTC()
	if !strings.Contains(decode(Tamper{SessionNotOnOrAfter: &sess}), "SessionNotOnOrAfter=") {
		t.Error("SessionNotOnOrAfter not applied")
	}
	if strings.Contains(decode(Tamper{OmitSignature: true}), "Signature") {
		t.Error("OmitSignature left a signature")
	}
}

func TestFakeIdP_EncryptedAssertion(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	certPEM, keyPEM, err := saml.GenerateSPKey("127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	spKey, err := saml.ParseSPKey(1, certPEM, keyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp.Key, sp.Certificate = spKey.Key, spKey.Cert
	reqXML, id, _ := authnRequest(t, sp, f)
	resp, err := f.Response(reqXML, "alex", saml.ACSURL(testBase), Tamper{EncryptFor: spKey.Cert})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(resp)
	if !bytes.Contains(raw, []byte("EncryptedAssertion")) {
		t.Fatal("assertion not encrypted")
	}
	if err := parse(t, sp, resp, id); err != nil {
		t.Fatalf("encrypted response rejected: %v", err)
	}
}

func TestFakeIdP_SSOPageAutoPostsUnderCSP(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	_, id, param := authnRequest(t, sp, f)

	q := url.Values{"SAMLRequest": {param}, "RelayState": {id}, "user": {"alex"}, "auto": {"1"}}
	rec := httptest.NewRecorder()
	f.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fake-idp/sso?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	scripts := regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`).FindAllStringSubmatch(body, -1)
	if len(scripts) != 1 || strings.TrimSpace(scripts[0][1]) != "" || !strings.Contains(body, `src="/fake-idp/submit.js"`) {
		t.Fatalf("expected exactly one external, empty script tag; body: %s", body)
	}
	if regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(body) {
		t.Fatal("inline event handler in page")
	}
	m := regexp.MustCompile(`name="SAMLResponse" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, `action="`+saml.ACSURL(testBase)+`"`) || !strings.Contains(body, `value="`+id+`"`) {
		t.Fatalf("form does not target the ACS with response and relay state: %s", body)
	}
	if err := parse(t, sp, html.UnescapeString(m[1]), id); err != nil {
		t.Fatalf("auto-posted response rejected: %v", err)
	}

	js := httptest.NewRecorder()
	f.Handler().ServeHTTP(js, httptest.NewRequest(http.MethodGet, "/fake-idp/submit.js", nil))
	b, _ := io.ReadAll(js.Body)
	if js.Code != http.StatusOK || !strings.Contains(js.Header().Get("Content-Type"), "javascript") || !bytes.Contains(b, []byte(".submit()")) {
		t.Fatalf("submit.js: %d %q %q", js.Code, js.Header().Get("Content-Type"), b)
	}
}

func TestFakeIdP_SSOFormPrefillsAndPostsBack(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	_, id, param := authnRequest(t, sp, f)
	q := url.Values{"SAMLRequest": {param}, "RelayState": {id}, "user": {"dana"}}
	rec := httptest.NewRecorder()
	f.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fake-idp/sso?"+q.Encode(), nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in as") || !strings.Contains(body, `value="dana"`) || !strings.Contains(body, "Continue") {
		t.Fatalf("form page: %s", body)
	}
	if strings.Contains(body, "SAMLResponse") {
		t.Fatal("form page issued a response before the user continued")
	}

	form := url.Values{"SAMLRequest": {param}, "RelayState": {id}, "user": {"dana"}}
	post := httptest.NewRequest(http.MethodPost, "/fake-idp/sso", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	f.Handler().ServeHTTP(rec, post)
	if m := regexp.MustCompile(`name="SAMLResponse" value="([^"]+)"`).FindStringSubmatch(rec.Body.String()); m == nil {
		t.Fatalf("POST did not answer with a response: %s", rec.Body)
	} else if err := parse(t, sp, html.UnescapeString(m[1]), id); err != nil {
		t.Fatal(err)
	}
}

func TestFakeIdP_RejectsBadUserAndGarbageRequest(t *testing.T) {
	f := newFake(t)
	sp := newSP(t, f)
	_, id, param := authnRequest(t, sp, f)
	for name, q := range map[string]url.Values{
		"bad_user": {"SAMLRequest": {param}, "RelayState": {id}, "user": {"Al<script>"}, "auto": {"1"}},
		"garbage":  {"SAMLRequest": {"not-base64!!"}, "user": {"alex"}, "auto": {"1"}},
		"missing":  {"user": {"alex"}},
	} {
		rec := httptest.NewRecorder()
		f.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fake-idp/sso?"+q.Encode(), nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}
