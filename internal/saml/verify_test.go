package saml_test

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
)

const testBaseURL = "https://harbor.example.test"

type harness struct {
	t       *testing.T
	idp     *fakeidp.Server
	cfg     saml.Config
	active  saml.SPKey
	retired saml.SPKey
	v       *saml.Verifier
	reqID   string
	reqXML  []byte
}

func newSPKey(t *testing.T, id int64) saml.SPKey {
	t.Helper()
	certPEM, keyPEM, err := saml.GenerateSPKey("harbor.example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	k, err := saml.ParseSPKey(id, certPEM, keyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: "http://127.0.0.1:8481"})
	if err != nil {
		t.Fatal(err)
	}
	return newHarnessWith(t, idp, time.Now())
}

// newHarnessWith builds a Verifier trusting idp and a request issued at requestTime.
func newHarnessWith(t *testing.T, idp *fakeidp.Server, requestTime time.Time) *harness {
	t.Helper()
	md, err := saml.ParseIdPMetadata(idp.Metadata(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := saml.Config{
		Enabled: true, IdPEntityID: md.EntityID, IdPSSORedirectURL: md.SSORedirectURL, IdPSSOPostURL: md.SSOPostURL,
		IdPCerts: md.SigningCerts, SPEntityID: saml.SPEntityID(testBaseURL), NameIDFormat: "email",
		ClockSkew: 120 * time.Second,
	}
	reqID, err := saml.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	_, reqXML, err := saml.BuildRedirect(cfg, testBaseURL, reqID, requestTime)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		t: t, idp: idp, cfg: cfg, active: newSPKey(t, 1), retired: newSPKey(t, 2),
		v: &saml.Verifier{Cfg: cfg, BaseURL: testBaseURL}, reqID: reqID, reqXML: reqXML,
	}
}

func (h *harness) respond(tm fakeidp.Tamper) string {
	h.t.Helper()
	b64, err := h.idp.Response(h.reqXML, "alex", saml.ACSURL(testBaseURL), tm)
	if err != nil {
		h.t.Fatal(err)
	}
	return b64
}

func (h *harness) verify(b64 string, keys ...saml.SPKey) (saml.Identity, error) {
	if len(keys) == 0 {
		keys = []saml.SPKey{h.active}
	}
	return h.v.Verify(saml.VerifyInput{ResponseB64: b64, RequestID: h.reqID, Now: time.Now(), Keys: keys})
}

func (h *harness) wantReason(b64, reason string, keys ...saml.SPKey) {
	h.t.Helper()
	_, err := h.verify(b64, keys...)
	if err == nil {
		h.t.Fatalf("Verify succeeded, want reason %q", reason)
	}
	if got := saml.ReasonOf(err); got != reason {
		h.t.Fatalf("reason = %q (%v), want %q", got, err, reason)
	}
}

func at(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }

// mutate decodes a response, edits it and re-encodes it (without re-signing).
func mutate(t *testing.T, b64 string, edit func(root *etree.Element)) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		t.Fatal(err)
	}
	edit(doc.Root())
	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func TestVerify_HappyPath_IdentityFields(t *testing.T) {
	h := newHarness(t)
	id, err := h.verify(h.respond(fakeidp.Tamper{}))
	if err != nil {
		t.Fatal(err)
	}
	if id.NameID != "alex@example.test" || id.NameIDFormat != string(crewjam.EmailAddressNameIDFormat) {
		t.Fatalf("NameID = %q / %q", id.NameID, id.NameIDFormat)
	}
	if id.AssertionID == "" || id.ResponseID == "" || id.AssertionID == id.ResponseID {
		t.Fatalf("IDs = %q / %q", id.AssertionID, id.ResponseID)
	}
	if !id.NotOnOrAfter.After(time.Now()) {
		t.Fatalf("NotOnOrAfter = %v", id.NotOnOrAfter)
	}
	if id.SessionNotOnOrAfter != nil {
		t.Fatalf("SessionNotOnOrAfter = %v, want nil", id.SessionNotOnOrAfter)
	}
	want := map[string]string{"email": "alex@example.test", "displayName": "Alex", "givenName": "Alex", "sn": "Example"}
	for k, v := range want {
		if got := id.Attributes[k]; len(got) != 1 || got[0] != v {
			t.Errorf("attribute %q = %v, want [%s]", k, got, v)
		}
	}
}

func TestVerify_RejectsMalformed(t *testing.T) {
	h := newHarness(t)
	for name, in := range map[string]string{
		"not base64": "!!!not base64!!!",
		"empty":      "",
		"not xml":    base64.StdEncoding.EncodeToString([]byte("hello")),
		"doctype":    base64.StdEncoding.EncodeToString([]byte(`<!DOCTYPE x [<!ENTITY a "b">]><Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol"/>`)),
		"wrong root": base64.StdEncoding.EncodeToString([]byte(`<foo/>`)),
		"no assertion": mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
			for _, c := range root.ChildElements() {
				if c.Tag == "Assertion" || c.Tag == "Signature" {
					root.RemoveChild(c)
				}
			}
		}),
	} {
		t.Run(name, func(t *testing.T) { h.wantReason(in, saml.ReasonMalformed) })
	}
}

func TestVerify_RejectsUnsigned(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{OmitSignature: true}), saml.ReasonUnsigned)
}

func TestVerify_RejectsWrongKey(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{SignWithWrongKey: true}), saml.ReasonInvalidSignature)
}

func TestVerify_RejectsTamperedSignedContent(t *testing.T) {
	h := newHarness(t)
	b64 := mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
		root.FindElement("//NameID").SetText("admin@example.test")
	})
	h.wantReason(b64, saml.ReasonInvalidSignature)
}

func TestVerify_RejectsMultipleAssertions(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{DuplicateAssertion: true}), saml.ReasonMultipleAssertions)
}

// Signature wrapping: the Response signature is removed so only the first
// assertion's own signature is valid, and an unsigned attacker assertion is
// added. crewjam alone accepts this (see TestCrewjamBaseline_*); we must not.
func TestVerify_RejectsInjectedUnsignedAssertion(t *testing.T) {
	h := newHarness(t)
	for _, evilFirst := range []bool{false, true} {
		b64 := mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
			if sig := root.FindElement("./Signature"); sig != nil {
				root.RemoveChild(sig)
			}
			good := root.FindElement("./Assertion")
			evil := good.Copy()
			evil.RemoveChild(evil.FindElement("./Signature"))
			evil.FindElement("//NameID").SetText("admin@example.test")
			evil.CreateAttr("ID", "_evil")
			if evilFirst {
				root.InsertChildAt(good.Index(), evil)
			} else {
				root.AddChild(evil)
			}
		})
		h.wantReason(b64, saml.ReasonMultipleAssertions)
	}
}

func TestVerify_RejectsInjectedAssertionInSignedResponse(t *testing.T) {
	h := newHarness(t)
	b64 := mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
		good := root.FindElement("./Assertion")
		evil := good.Copy()
		evil.RemoveChild(evil.FindElement("./Signature"))
		evil.FindElement("//NameID").SetText("admin@example.test")
		root.AddChild(evil)
	})
	h.wantReason(b64, saml.ReasonInvalidSignature)
}

// Canary for the library assumption: crewjam v0.5.1 returns the first valid
// assertion and does NOT reject a second one. If this starts failing, crewjam
// changed and the multiple_assertions pre-check should be re-evaluated.
func TestCrewjamBaseline_AcceptsSecondUnsignedAssertion(t *testing.T) {
	h := newHarness(t)
	b64 := mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
		root.RemoveChild(root.FindElement("./Signature"))
		evil := root.FindElement("./Assertion").Copy()
		evil.RemoveChild(evil.FindElement("./Signature"))
		root.AddChild(evil)
	})
	raw, _ := base64.StdEncoding.DecodeString(b64)
	acs, _ := url.Parse(saml.ACSURL(testBaseURL))
	sp := crewjam.ServiceProvider{EntityID: h.cfg.SPEntityID, AcsURL: *acs, IDPMetadata: &crewjam.EntityDescriptor{
		EntityID: h.cfg.IdPEntityID,
		IDPSSODescriptors: []crewjam.IDPSSODescriptor{{SSODescriptor: crewjam.SSODescriptor{RoleDescriptor: crewjam.RoleDescriptor{
			KeyDescriptors: []crewjam.KeyDescriptor{{Use: "signing", KeyInfo: crewjam.KeyInfo{X509Data: crewjam.X509Data{
				X509Certificates: []crewjam.X509Certificate{{Data: base64.StdEncoding.EncodeToString(h.cfg.IdPCerts[0].Raw)}}}}}},
		}}}},
	}}
	if _, err := sp.ParseXMLResponse(raw, []string{h.reqID}, *acs); err != nil {
		t.Fatalf("crewjam now rejects a second unsigned assertion: %v", err)
	}
}

func TestVerify_RejectsForeignAudience(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{Audience: "https://other-sp.example.test/metadata"}), saml.ReasonAudience)
}

func TestVerify_RejectsWrongIssuer(t *testing.T) {
	h := newHarness(t)
	h.v.Cfg.IdPEntityID = "http://127.0.0.1:9999/some-other-idp"
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonIssuer)
}

func TestVerify_RejectsWrongDestination(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{Destination: "https://evil.example/api/auth/saml/acs"}), saml.ReasonDestination)
}

// The expected destination comes from the configured base URL only: a response
// addressed to another host's ACS is refused even though it is validly signed.
func TestVerify_DestinationDerivedFromConfiguredBaseURL(t *testing.T) {
	h := newHarness(t)
	b64, err := h.idp.Response(h.reqXML, "alex", saml.ACSURL("https://attacker.example.test"), fakeidp.Tamper{})
	if err != nil {
		t.Fatal(err)
	}
	h.wantReason(b64, saml.ReasonDestination)
}

func TestVerify_RejectsWrongRecipient(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{Recipient: "https://evil.example/api/auth/saml/acs"}), saml.ReasonRecipient)
}

func TestVerify_RejectsWrongInResponseTo(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{InResponseTo: "_someone-elses-request"}), saml.ReasonInResponseTo)
}

func TestVerify_RejectsExpiredAndNotYetValid_WithinSkewAccepted(t *testing.T) {
	h := newHarness(t) // skew 120 s
	if _, err := h.verify(h.respond(fakeidp.Tamper{NotOnOrAfter: at(-60 * time.Second)})); err != nil {
		t.Fatalf("60 s past within skew: %v", err)
	}
	h.wantReason(h.respond(fakeidp.Tamper{NotOnOrAfter: at(-200 * time.Second)}), saml.ReasonExpired)
	if _, err := h.verify(h.respond(fakeidp.Tamper{NotBefore: at(60 * time.Second)})); err != nil {
		t.Fatalf("60 s early within skew: %v", err)
	}
	h.wantReason(h.respond(fakeidp.Tamper{NotBefore: at(200 * time.Second)}), saml.ReasonNotYetValid)
}

func TestVerify_SkewIsBoundedTo300Seconds(t *testing.T) {
	h := newHarness(t)
	h.v.Cfg.ClockSkew = time.Hour // configured beyond the cap
	if _, err := h.verify(h.respond(fakeidp.Tamper{NotOnOrAfter: at(-250 * time.Second)})); err != nil {
		t.Fatalf("250 s past with capped skew: %v", err)
	}
	h.wantReason(h.respond(fakeidp.Tamper{NotOnOrAfter: at(-400 * time.Second)}), saml.ReasonExpired)
	h.wantReason(h.respond(fakeidp.Tamper{NotBefore: at(400 * time.Second)}), saml.ReasonNotYetValid)
}

func TestVerify_ZeroSkewIsStrict(t *testing.T) {
	h := newHarness(t)
	h.v.Cfg.ClockSkew = 0
	h.wantReason(h.respond(fakeidp.Tamper{NotOnOrAfter: at(-5 * time.Second)}), saml.ReasonExpired)
}

func TestVerify_SubjectConfirmationExpiryIsChecked(t *testing.T) {
	h := newHarness(t)
	lenient := lenientLayer()
	h.v.SetFirstLayerForTest(func(raw []byte) (*crewjam.Assertion, error) {
		a, err := lenient(raw)
		a.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotOnOrAfter = time.Now().Add(-200 * time.Second)
		return a, err
	})
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonExpired)
}

func TestVerify_RejectsStaleIssueInstant(t *testing.T) {
	old := time.Now().Add(-10 * time.Minute)
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: "http://127.0.0.1:8481", Now: func() time.Time { return old }})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, idp, time.Now())
	h.wantReason(h.respond(fakeidp.Tamper{NotOnOrAfter: at(time.Minute)}), saml.ReasonExpired)
}

func TestVerify_UsesInputNowForBothLayers(t *testing.T) {
	issued := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: "http://127.0.0.1:8481", Now: func() time.Time { return issued }})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, idp, issued)
	b64 := h.respond(fakeidp.Tamper{})
	if _, err := h.v.Verify(saml.VerifyInput{ResponseB64: b64, RequestID: h.reqID, Now: issued.Add(10 * time.Second), Keys: []saml.SPKey{h.active}}); err != nil {
		t.Fatalf("verification at the injected time: %v", err)
	}
	_, err = h.v.Verify(saml.VerifyInput{ResponseB64: b64, RequestID: h.reqID, Now: issued.Add(10 * time.Minute), Keys: []saml.SPKey{h.active}})
	if saml.ReasonOf(err) != saml.ReasonExpired {
		t.Fatalf("10 min later: %v", err)
	}
}

func TestVerify_RejectsIdPStatusRequester(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{Status: "urn:oasis:names:tc:SAML:2.0:status:Requester"}), saml.ReasonIdPStatus)
	if got := saml.UserMessageFor(saml.ReasonIdPStatus); got != "sso_declined" {
		t.Fatalf("UserMessageFor = %q", got)
	}
}

func TestVerify_RejectsTransientNameID(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{NameIDFormat: "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"}), saml.ReasonTransientNameID)
}

func TestVerify_RejectsMissingNameID(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{OmitNameID: true}), saml.ReasonNoNameID)
}

func TestVerify_RejectsMissingAuthnStatement(t *testing.T) {
	h := newHarness(t)
	h.wantReason(h.respond(fakeidp.Tamper{OmitAuthnStatement: true}), saml.ReasonNoAuthnStatement)
}

func TestVerify_DecryptsWithActiveThenRetiredKey(t *testing.T) {
	h := newHarness(t)
	for name, enc := range map[string]*x509.Certificate{"active": h.active.Cert, "retired": h.retired.Cert} {
		t.Run(name, func(t *testing.T) {
			id, err := h.verify(h.respond(fakeidp.Tamper{EncryptFor: enc}), h.active, h.retired)
			if err != nil {
				t.Fatal(err)
			}
			if id.NameID != "alex@example.test" {
				t.Fatalf("NameID = %q", id.NameID)
			}
		})
	}
}

func TestVerify_DecryptFailedWithoutKey(t *testing.T) {
	h := newHarness(t)
	enc := h.respond(fakeidp.Tamper{EncryptFor: h.active.Cert})
	_, err := h.v.Verify(saml.VerifyInput{ResponseB64: enc, RequestID: h.reqID, Now: time.Now()})
	if saml.ReasonOf(err) != saml.ReasonSPKeyUnavailable {
		t.Fatalf("no keys: %v", err)
	}
	// Keys present but none matches the one the IdP encrypted for.
	h.wantReason(enc, saml.ReasonDecryptFailed, h.retired)
}

func TestVerify_SessionNotOnOrAfterParsed(t *testing.T) {
	h := newHarness(t)
	want := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	id, err := h.verify(h.respond(fakeidp.Tamper{SessionNotOnOrAfter: &want}))
	if err != nil {
		t.Fatal(err)
	}
	if id.SessionNotOnOrAfter == nil || !id.SessionNotOnOrAfter.Equal(want) {
		t.Fatalf("SessionNotOnOrAfter = %v, want %v", id.SessionNotOnOrAfter, want)
	}
}

// --- second layer in isolation (the library layer is replaced) ---

func lenientLayer() func(raw []byte) (*crewjam.Assertion, error) {
	return func(raw []byte) (*crewjam.Assertion, error) {
		var r crewjam.Response
		if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&r); err != nil {
			return nil, err
		}
		return r.Assertion, nil
	}
}

func TestVerify_ValidatorDisagreement(t *testing.T) {
	cases := map[string]fakeidp.Tamper{
		"recipient":       {Recipient: "https://evil.example/api/auth/saml/acs"},
		"audience":        {Audience: "https://other-sp.example.test/metadata"},
		"in_response_to":  {InResponseTo: "_other"},
		"response_target": {Destination: "https://evil.example/api/auth/saml/acs"},
	}
	for name, tm := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.v.SetFirstLayerForTest(lenientLayer())
			h.wantReason(h.respond(tm), saml.ReasonValidatorDisagreement)
		})
	}
	t.Run("issuer", func(t *testing.T) {
		h := newHarness(t)
		h.v.Cfg.IdPEntityID = "http://127.0.0.1:9999/other"
		h.v.SetFirstLayerForTest(lenientLayer())
		h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonValidatorDisagreement)
	})
}

func TestVerify_SecondLayerRequiresWhatLibraryTreatsAsOptional(t *testing.T) {
	h := newHarness(t)
	edit := func(f func(a *crewjam.Assertion)) {
		lenient := lenientLayer()
		h.v.SetFirstLayerForTest(func(raw []byte) (*crewjam.Assertion, error) {
			a, err := lenient(raw)
			f(a)
			return a, err
		})
	}
	edit(func(a *crewjam.Assertion) { a.Conditions.AudienceRestrictions = nil })
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonAudience)
	edit(func(a *crewjam.Assertion) {
		a.Subject.SubjectConfirmations[0].Method = "urn:oasis:names:tc:SAML:2.0:cm:holder-of-key"
	})
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonRecipient)
	edit(func(a *crewjam.Assertion) { a.Subject.SubjectConfirmations = nil })
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonRecipient)
	edit(func(a *crewjam.Assertion) { a.Conditions = nil })
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonExpired)
}

func TestVerify_UnsignedResponseWithoutDestinationIsRefused(t *testing.T) {
	// crewjam allows an omitted Destination on an unsigned Response; we do not.
	h := newHarness(t)
	b64 := mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
		root.RemoveChild(root.FindElement("./Signature"))
		root.RemoveAttr("Destination")
	})
	h.wantReason(b64, saml.ReasonDestination)
}

func TestVerify_LibraryPanicBecomesMalformed(t *testing.T) {
	h := newHarness(t)
	h.v.SetFirstLayerForTest(func([]byte) (*crewjam.Assertion, error) { panic("boom") })
	h.wantReason(h.respond(fakeidp.Tamper{}), saml.ReasonMalformed)
}

func TestVerify_AttributesIndexedByFriendlyName(t *testing.T) {
	h := newHarness(t)
	lenient := lenientLayer()
	h.v.SetFirstLayerForTest(func(raw []byte) (*crewjam.Assertion, error) {
		a, err := lenient(raw)
		a.AttributeStatements = []crewjam.AttributeStatement{{Attributes: []crewjam.Attribute{{
			FriendlyName: "mail", Name: "urn:oid:0.9.2342.19200300.100.1.3",
			Values: []crewjam.AttributeValue{{Value: "a@b.test"}},
		}}}}
		return a, err
	})
	id, err := h.verify(h.respond(fakeidp.Tamper{}))
	if err != nil {
		t.Fatal(err)
	}
	if id.Attributes["mail"][0] != "a@b.test" || id.Attributes["urn:oid:0.9.2342.19200300.100.1.3"][0] != "a@b.test" {
		t.Fatalf("attributes = %v", id.Attributes)
	}
}

func TestVerify_ConcurrentCalls(t *testing.T) {
	h := newHarness(t)
	b64 := h.respond(fakeidp.Tamper{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.verify(b64); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestUserMessageFor(t *testing.T) {
	for reason, want := range map[string]string{
		saml.ReasonIdPStatus: "sso_declined", saml.ReasonSSOOff: "sso_off",
		saml.ReasonUnsigned: "sso_failed", saml.ReasonValidatorDisagreement: "sso_failed", saml.ReasonInternal: "sso_failed",
	} {
		if got := saml.UserMessageFor(reason); got != want {
			t.Errorf("UserMessageFor(%q) = %q, want %q", reason, got, want)
		}
	}
	if saml.ReasonOf(nil) != "" || saml.ReasonOf(errors.New("x")) != saml.ReasonInternal {
		t.Fatal("ReasonOf nil/unknown")
	}
}
