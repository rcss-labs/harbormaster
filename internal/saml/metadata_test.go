package saml

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	crewjam "github.com/crewjam/saml"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseIdPMetadata_FourVendors(t *testing.T) {
	cases := []struct {
		file, entityID, redirect, post string
		certs                          int
	}{
		{"okta-idp-metadata.xml", "http://www.okta.com/exk1throwaway0test",
			"https://example-throwaway.okta.test/app/example/exk1throwaway0test/sso/saml",
			"https://example-throwaway.okta.test/app/example/exk1throwaway0test/sso/saml", 1},
		{"entra-idp-metadata.xml", "https://sts.windows.net/00000000-0000-0000-0000-000000000000/",
			"https://login.microsoftonline.test/00000000-0000-0000-0000-000000000000/saml2",
			"https://login.microsoftonline.test/00000000-0000-0000-0000-000000000000/saml2", 2},
		{"google-idp-metadata.xml", "https://accounts.google.test/o/saml2?idpid=C00throwaway",
			"https://accounts.google.test/o/saml2/idp?idpid=C00throwaway",
			"https://accounts.google.test/o/saml2/idp?idpid=C00throwaway", 1},
		// Keycloak also lists an encryption-only certificate, which must be ignored.
		{"keycloak-idp-metadata.xml", "https://keycloak.example.test/realms/throwaway",
			"https://keycloak.example.test/realms/throwaway/protocol/saml",
			"https://keycloak.example.test/realms/throwaway/protocol/saml", 1},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			md, err := ParseIdPMetadata(readTestdata(t, tc.file), fixedNow)
			if err != nil {
				t.Fatal(err)
			}
			if md.EntityID != tc.entityID || md.SSORedirectURL != tc.redirect || md.SSOPostURL != tc.post {
				t.Errorf("got %+v", md)
			}
			if len(md.SigningCerts) != tc.certs {
				t.Errorf("certs = %d, want %d", len(md.SigningCerts), tc.certs)
			}
		})
	}
}

const mdTemplate = `<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="http://www.w3.org/2000/09/xmldsig#" entityID="https://idp.example.test">%s</md:EntityDescriptor>`

func idpDescriptor(redirect string, keyDescriptors ...string) string {
	sso := ""
	if redirect != "" {
		sso = fmt.Sprintf(`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="%s"/>`, redirect)
	}
	return fmt.Sprintf(mdTemplate, `<md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">`+
		strings.Join(keyDescriptors, "")+sso+`</md:IDPSSODescriptor>`)
}

func keyDescriptor(use, certB64 string) string {
	return fmt.Sprintf(`<md:KeyDescriptor use="%s"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>`, use, certB64)
}

func certB64At(t *testing.T, notBefore time.Time) string {
	t.Helper()
	certPEM, _, err := GenerateSPKey("expired.example.test", notBefore)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range strings.Split(certPEM, "\n") {
		if !strings.HasPrefix(l, "-----") {
			b.WriteString(l)
		}
	}
	return b.String()
}

func TestParseIdPMetadata_Rejects(t *testing.T) {
	good := certB64At(t, fixedNow.Add(-time.Hour))
	expired := certB64At(t, fixedNow.AddDate(-11, 0, 0)) // valid 10y, so expired a year ago
	var six []string
	for i := 0; i < 6; i++ {
		six = append(six, keyDescriptor("signing", certB64At(t, fixedNow.Add(-time.Duration(i+1)*time.Hour))))
	}
	redirect := "https://idp.example.test/sso"
	cases := []struct {
		name, xml, wantErr string
	}{
		{"not xml", "nope", "not valid SAML metadata"},
		{"no IDPSSODescriptor", fmt.Sprintf(mdTemplate, ""), "Metadata has no IDPSSODescriptor."},
		{"no redirect", idpDescriptor("", keyDescriptor("signing", good)), "Metadata has no HTTP-Redirect SingleSignOnService."},
		{"javascript redirect", idpDescriptor("javascript:alert(1)", keyDescriptor("signing", good)), "not valid SAML metadata"},
		{"expired cert", idpDescriptor(redirect, keyDescriptor("signing", expired)), "Certificate 1 expired on "},
		{"encryption only", idpDescriptor(redirect, keyDescriptor("encryption", good)), "Metadata has no signing certificate."},
		{"too many certs", idpDescriptor(redirect, six...), "more than 5"},
		{"garbage cert", idpDescriptor(redirect, keyDescriptor("signing", "AAAA")), "Certificate 1 is not a valid X.509 certificate."},
		{"doctype", "<!DOCTYPE x [<!ENTITY a \"b\">]>" + idpDescriptor(redirect, keyDescriptor("signing", good)), "not valid SAML metadata"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseIdPMetadata([]byte(tc.xml), fixedNow)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	// Control: the same document with one good signing cert parses.
	if _, err := ParseIdPMetadata([]byte(idpDescriptor(redirect, keyDescriptor("signing", good), keyDescriptor("encryption", expired))), fixedNow); err != nil {
		t.Fatalf("control failed (encryption-only expired cert must be ignored): %v", err)
	}
}

func TestMetadataFetcher_BlocksLinkLocalAndPrivate(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("<ok/>"))
	}))
	defer srv.Close()
	ctx := context.Background()

	prod := &MetadataFetcher{}
	// The ECS task metadata endpoint, IMDS, RFC 1918, loopback (literal and by name), IPv6 loopback and a mapped address.
	for _, u := range []string{
		"https://169.254.170.2/v2/credentials",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.5/metadata",
		"https://192.168.1.1/metadata",
		"https://172.16.0.1/metadata",
		"https://" + srv.Listener.Addr().String() + "/metadata",
		"https://localhost:" + portOf(srv) + "/metadata",
		"https://[::1]:" + portOf(srv) + "/metadata",
		"https://[::ffff:127.0.0.1]:" + portOf(srv) + "/metadata",
	} {
		if _, err := prod.Fetch(ctx, u); err == nil {
			t.Errorf("%s was fetched in production mode", u)
		}
	}
	if hits != 0 {
		t.Fatalf("blocked requests reached the server %d times", hits)
	}
	if _, err := prod.Fetch(ctx, "http://example.com/metadata"); err == nil || !strings.Contains(err.Error(), "Fetching metadata needs an https URL.") {
		t.Errorf("http URL: %v", err)
	}
	if _, err := prod.Fetch(ctx, "https://user:pw@example.com/metadata"); err == nil {
		t.Error("URL with credentials accepted")
	}

	dev := &MetadataFetcher{AllowInsecure: true, Client: srv.Client()}
	body, err := dev.Fetch(ctx, srv.URL+"/metadata")
	if err != nil || string(body) != "<ok/>" {
		t.Fatalf("dev mode: %q, %v", body, err)
	}
}

func portOf(s *httptest.Server) string {
	_, p, _ := strings.Cut(s.Listener.Addr().String(), ":")
	return p
}

func TestMetadataFetcher_CapsSizeAndRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 2048)))
	})
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 1024)))
	})
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/hop/%d", &n)
		if n == 0 {
			_, _ = w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusNotFound) })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	ctx := context.Background()
	f := &MetadataFetcher{AllowInsecure: true, Client: srv.Client(), MaxBytes: 1024}

	if _, err := f.Fetch(ctx, srv.URL+"/big"); err == nil {
		t.Error("oversized body accepted")
	}
	if b, err := f.Fetch(ctx, srv.URL+"/exact"); err != nil || len(b) != 1024 {
		t.Errorf("body exactly at the cap: %d, %v", len(b), err)
	}
	if b, err := f.Fetch(ctx, srv.URL+"/hop/3"); err != nil || string(b) != "done" {
		t.Errorf("3 redirects should be followed: %q, %v", b, err)
	}
	if _, err := f.Fetch(ctx, srv.URL+"/hop/4"); err == nil {
		t.Error("4 redirects followed")
	}
	if _, err := f.Fetch(ctx, srv.URL+"/status"); err == nil {
		t.Error("404 accepted")
	}
	short := &MetadataFetcher{AllowInsecure: true, Client: &http.Client{Transport: srv.Client().Transport, Timeout: 100 * time.Millisecond}}
	if _, err := short.Fetch(ctx, srv.URL+"/slow"); err == nil {
		t.Error("slow server did not time out")
	}
}

func TestMetadataFetcher_RedirectsOnlyToHTTPSInProduction(t *testing.T) {
	prod := &MetadataFetcher{}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/x", nil)
	if err := prod.checkRedirect(req, []*http.Request{{}}); err == nil {
		t.Error("redirect to http accepted in production mode")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	if err := prod.checkRedirect(req, []*http.Request{{}}); err != nil {
		t.Errorf("redirect to https refused: %v", err)
	}
	if err := prod.checkRedirect(req, make([]*http.Request, 4)); err == nil {
		t.Error("fourth redirect accepted")
	}
	dev := &MetadataFetcher{AllowInsecure: true}
	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/x", nil)
	if err := dev.checkRedirect(req, []*http.Request{{}}); err != nil {
		t.Errorf("dev redirect to http refused: %v", err)
	}
}

func TestMetadataFetcher_DefaultClientDoesNotUseProxyOrTrustTLSFailures(t *testing.T) {
	// A TLS server with an untrusted certificate must fail with the default client.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{}
	srv.StartTLS()
	defer srv.Close()
	f := &MetadataFetcher{AllowInsecure: true}
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestSPMetadata_Shape(t *testing.T) {
	certPEM, keyPEM, _ := GenerateSPKey("hm.example.test", fixedNow)
	active, err := ParseSPKey(2, certPEM, keyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id int64, retiredAgo time.Duration) SPKey {
		c, k, _ := GenerateSPKey("hm.example.test", fixedNow)
		r := fixedNow.Add(-retiredAgo)
		key, err := ParseSPKey(id, c, k, &r)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	recent := mk(1, 24*time.Hour)
	stale := mk(0, 8*24*time.Hour)
	base := "https://hm.example.test"

	out, err := SPMetadata(Config{NameIDFormat: "email"}, base, active, []SPKey{recent, stale}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	var ed crewjam.EntityDescriptor
	if err := xml.Unmarshal(out, &ed); err != nil {
		t.Fatalf("metadata does not parse: %v\n%s", err, out)
	}
	if ed.EntityID != base+"/api/auth/saml/metadata" {
		t.Errorf("entityID = %q", ed.EntityID)
	}
	if len(ed.SPSSODescriptors) != 1 {
		t.Fatalf("SPSSODescriptors = %d", len(ed.SPSSODescriptors))
	}
	sp := ed.SPSSODescriptors[0]
	if sp.AuthnRequestsSigned == nil || *sp.AuthnRequestsSigned {
		t.Error("AuthnRequestsSigned must be false")
	}
	if sp.WantAssertionsSigned == nil || !*sp.WantAssertionsSigned {
		t.Error("WantAssertionsSigned must be true")
	}
	if len(sp.AssertionConsumerServices) != 1 {
		t.Fatalf("ACS count = %d", len(sp.AssertionConsumerServices))
	}
	acs := sp.AssertionConsumerServices[0]
	if acs.Binding != crewjam.HTTPPostBinding || acs.Location != base+"/api/auth/saml/acs" || acs.Index != 0 || acs.IsDefault == nil || !*acs.IsDefault {
		t.Errorf("ACS = %+v", acs)
	}
	if len(sp.SingleLogoutServices) != 0 {
		t.Error("SLO must not be advertised")
	}
	if len(sp.KeyDescriptors) != 2 {
		t.Fatalf("KeyDescriptors = %d, want active + one recent retired", len(sp.KeyDescriptors))
	}
	for _, kd := range sp.KeyDescriptors {
		if kd.Use != "encryption" {
			t.Errorf("use = %q", kd.Use)
		}
	}
	if got := sp.KeyDescriptors[0].KeyInfo.X509Data.X509Certificates[0].Data; strings.Join(strings.Fields(got), "") != certBody(certPEM) {
		t.Error("first KeyDescriptor is not the active certificate")
	}
	if len(sp.NameIDFormats) != 1 || sp.NameIDFormats[0] != crewjam.EmailAddressNameIDFormat {
		t.Errorf("NameIDFormats = %v", sp.NameIDFormats)
	}
	if strings.Contains(string(out), "PRIVATE") {
		t.Error("metadata leaks key material")
	}

	// Defaults and URL helpers.
	out2, err := SPMetadata(Config{}, base+"/", active, nil, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out2), "urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified") {
		t.Errorf("default NameIDFormat is not unspecified:\n%s", out2)
	}
	if ACSURL(base+"/") != base+"/api/auth/saml/acs" || SPEntityID(base) != MetadataURL(base) || MetadataURL(base) != base+"/api/auth/saml/metadata" {
		t.Error("URL helpers")
	}
	// A configured SP entity ID wins.
	out3, _ := SPMetadata(Config{SPEntityID: "urn:custom:sp"}, base, active, nil, fixedNow)
	if !strings.Contains(string(out3), `entityID="urn:custom:sp"`) {
		t.Error("configured SP entity ID ignored")
	}
}

func certBody(pemText string) string {
	var b strings.Builder
	for _, l := range strings.Split(pemText, "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			b.WriteString(l)
		}
	}
	return b.String()
}
