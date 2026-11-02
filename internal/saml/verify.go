package saml

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
)

const (
	nsAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"
	nsProtocol  = "urn:oasis:names:tc:SAML:2.0:protocol"

	statusSuccess      = "urn:oasis:names:tc:SAML:2.0:status:Success"
	nameIDTransient    = "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"
	confirmationBearer = "urn:oasis:names:tc:SAML:2.0:cm:bearer"

	maxResponseBytes = 1 << 20
	maxIssueDelay    = 90 * time.Second // same value as crewjam.MaxIssueDelay
)

// VerifyInput is one ACS POST. RequestID is the saml_requests row the caller
// consumed; Now is the clock for both validation layers; Keys are the active
// SP key first, then retired keys still inside their grace period.
type VerifyInput struct {
	ResponseB64 string
	RequestID   string
	Now         time.Time
	Keys        []SPKey
}

// Verifier validates SAML responses in two independent layers: crewjam's
// ServiceProvider.ParseXMLResponse, then our own re-check of every rule in the
// spec's validation table. A rule that both layers implement identically and on
// which they disagree is a hard failure (ReasonValidatorDisagreement). Rules
// that are intentionally stricter than the library (see recheck) fail with
// their own reason.
//
// Verify does not touch the replay cache: the caller inserts Identity's
// AssertionID and ResponseID (rule 16) once Verify succeeds.
type Verifier struct {
	Cfg     Config
	BaseURL string
	// Log receives one error-level record per validator disagreement. It never
	// receives XML or key material. Nil disables logging.
	Log *slog.Logger

	// firstLayer replaces the crewjam layer in tests.
	firstLayer func(raw []byte) (*crewjam.Assertion, error)
}

// crewjam reads its clock from the package variable crewjam.TimeNow, so calls
// are serialised while it points at VerifyInput.Now.
var crewjamMu sync.Mutex

func (v *Verifier) skew() time.Duration {
	switch {
	case v.Cfg.ClockSkew < 0:
		return 0
	case v.Cfg.ClockSkew > MaxClockSkew:
		return MaxClockSkew
	default:
		return v.Cfg.ClockSkew
	}
}

// Verify returns the identity asserted by a response that passed both layers.
func (v *Verifier) Verify(in VerifyInput) (Identity, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	raw, err := decodeResponse(in.ResponseB64)
	if err != nil {
		return Identity{}, err
	}
	root, err := parseRoot(raw)
	if err != nil {
		return Identity{}, err
	}

	first := v.firstLayer
	if first == nil {
		first = func(raw []byte) (*crewjam.Assertion, error) { return v.libraryParse(raw, root, in) }
	}
	assertion, err := runFirstLayer(first, raw)
	if err != nil {
		return Identity{}, err
	}
	if assertion == nil {
		return Identity{}, newError(ReasonMalformed, "library returned no assertion")
	}

	id, rerr := v.recheck(root, assertion, in)
	if rerr != nil {
		return Identity{}, rerr
	}
	return id, nil
}

func decodeResponse(b64 string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\r' || r == '\n' || r == '\t' {
			return -1
		}
		return r
	}, b64)
	if cleaned == "" {
		return nil, newError(ReasonMalformed, "empty SAMLResponse")
	}
	if base64.StdEncoding.DecodedLen(len(cleaned)) > maxResponseBytes {
		return nil, newError(ReasonMalformed, "SAMLResponse too large")
	}
	raw, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, newError(ReasonMalformed, "SAMLResponse is not base64")
	}
	return raw, nil
}

// parseRoot rejects DTDs and anything that is not a single protocol:Response.
func parseRoot(raw []byte) (*etree.Element, error) {
	lower := bytes.ToLower(raw)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return nil, newError(ReasonMalformed, "DTD not allowed")
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return nil, newError(ReasonMalformed, "not XML")
	}
	root := doc.Root()
	if root == nil || root.Tag != "Response" || root.NamespaceURI() != nsProtocol {
		return nil, newError(ReasonMalformed, "root element is not a SAML Response")
	}
	return root, nil
}

// runFirstLayer converts a library panic (crewjam dereferences optional
// elements such as Conditions) into a rejection instead of a crashed request.
func runFirstLayer(f func([]byte) (*crewjam.Assertion, error), raw []byte) (a *crewjam.Assertion, err error) {
	defer func() {
		if r := recover(); r != nil {
			a, err = nil, newError(ReasonMalformed, "library panicked while parsing the response")
		}
	}()
	a, err = f(raw)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			err = mapLibraryError(err)
		}
	}
	return a, err
}

// libraryParse is layer 1: crewjam's ParseXMLResponse with the SP key tried in
// order (active, then retired) when the assertion is encrypted.
func (v *Verifier) libraryParse(raw []byte, root *etree.Element, in VerifyInput) (*crewjam.Assertion, error) {
	encrypted := len(childrenNamed(root, nsAssertion, "EncryptedAssertion")) > 0
	if encrypted && len(in.Keys) == 0 {
		return nil, newError(ReasonSPKeyUnavailable, "response is encrypted but no SP key is available")
	}
	acs, err := url.Parse(ACSURL(v.BaseURL))
	if err != nil {
		return nil, newError(ReasonInternal, "bad base URL")
	}
	metadataURL, _ := url.Parse(MetadataURL(v.BaseURL))
	idp := v.idpMetadata()

	keys := in.Keys
	if !encrypted || len(keys) == 0 {
		keys = []SPKey{{}}
	}
	var lastErr error
	for _, key := range keys {
		sp := crewjam.ServiceProvider{
			EntityID:          v.spEntityID(),
			AcsURL:            *acs,
			MetadataURL:       *metadataURL,
			IDPMetadata:       idp,
			AllowIDPInitiated: false,
		}
		if key.Key != nil {
			sp.Key, sp.Certificate = key.Key, key.Cert
		}
		assertion, err := parseWithClock(&sp, raw, in)
		if err == nil {
			return assertion, nil
		}
		mapped := mapLibraryError(err)
		if mapped.Reason != ReasonDecryptFailed {
			return nil, mapped
		}
		lastErr = mapped
	}
	return nil, lastErr
}

func parseWithClock(sp *crewjam.ServiceProvider, raw []byte, in VerifyInput) (*crewjam.Assertion, error) {
	acs := sp.AcsURL
	crewjamMu.Lock()
	defer crewjamMu.Unlock()
	prev := crewjam.TimeNow
	crewjam.TimeNow = func() time.Time { return in.Now.UTC() }
	defer func() { crewjam.TimeNow = prev }()
	return sp.ParseXMLResponse(raw, []string{in.RequestID}, acs)
}

func (v *Verifier) spEntityID() string {
	if v.Cfg.SPEntityID != "" {
		return v.Cfg.SPEntityID
	}
	return SPEntityID(v.BaseURL)
}

// idpMetadata synthesises the metadata crewjam needs from the stored config.
func (v *Verifier) idpMetadata() *crewjam.EntityDescriptor {
	var certs []crewjam.X509Certificate
	for _, c := range v.Cfg.IdPCerts {
		certs = append(certs, crewjam.X509Certificate{Data: base64.StdEncoding.EncodeToString(c.Raw)})
	}
	return &crewjam.EntityDescriptor{
		EntityID: v.Cfg.IdPEntityID,
		IDPSSODescriptors: []crewjam.IDPSSODescriptor{{
			SSODescriptor: crewjam.SSODescriptor{RoleDescriptor: crewjam.RoleDescriptor{
				KeyDescriptors: []crewjam.KeyDescriptor{{Use: "signing", KeyInfo: crewjam.KeyInfo{X509Data: crewjam.X509Data{X509Certificates: certs}}}},
			}},
			SingleSignOnServices: []crewjam.Endpoint{{Binding: crewjam.HTTPRedirectBinding, Location: v.Cfg.IdPSSORedirectURL}},
		}},
	}
}

// mapLibraryError turns crewjam's private error text into a reason code.
// crewjam returns plain strings, so this is substring matching; the table is
// ordered most specific first. Anything unrecognised is ReasonInvalidSignature
// when it mentions the signature and ReasonMalformed otherwise.
//
//	ErrBadStatus                                  idp_status
//	"signature element not present"               unsigned
//	"failed to decrypt"                           decrypt_failed
//	"`Destination` does not match"                destination
//	"`InResponseTo` does not match",
//	  "SubjectConfirmation one of the possible"   in_response_to
//	"SubjectConfirmation Recipient"               recipient
//	"Issuer does not match", "issuer is not"      issuer
//	"AudienceRestriction"                         audience
//	"not yet valid"                               not_yet_valid
//	"expired"                                     expired
//	"cannot validate signature", other signature  invalid_signature
func mapLibraryError(err error) *Error {
	var status crewjam.ErrBadStatus
	if errors.As(err, &status) {
		return newError(ReasonIdPStatus, "IdP status %s", status.Status)
	}
	var invalid *crewjam.InvalidResponseError
	if errors.As(err, &invalid) && invalid.PrivateErr != nil {
		// Only the private error is wrapped: InvalidResponseError.Response
		// holds the raw document and must not reach logs.
		if errors.As(invalid.PrivateErr, &status) {
			return newError(ReasonIdPStatus, "IdP status %s", status.Status)
		}
		err = invalid.PrivateErr
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	reason := ReasonMalformed
	switch {
	case strings.Contains(lower, "signature element not present"):
		reason = ReasonUnsigned
	case strings.Contains(lower, "failed to decrypt"):
		reason = ReasonDecryptFailed
	case strings.Contains(msg, "`Destination`"):
		reason = ReasonDestination
	case strings.Contains(msg, "`InResponseTo`"), strings.Contains(msg, "SubjectConfirmation one of the possible"):
		reason = ReasonInResponseTo
	case strings.Contains(msg, "SubjectConfirmation Recipient"):
		reason = ReasonRecipient
	case strings.Contains(msg, "Issuer does not match"), strings.Contains(msg, "issuer is not"):
		reason = ReasonIssuer
	case strings.Contains(msg, "AudienceRestriction"):
		reason = ReasonAudience
	case strings.Contains(lower, "not yet valid"):
		reason = ReasonNotYetValid
	case strings.Contains(lower, "expired"):
		reason = ReasonExpired
	case strings.Contains(lower, "signature"), strings.Contains(lower, "digest"), strings.Contains(lower, "certificate"):
		reason = ReasonInvalidSignature
	}
	return &Error{Reason: reason, Err: errors.New(sanitizeLibraryMessage(msg))}
}

// sanitizeLibraryMessage keeps log lines short; crewjam messages carry URLs and
// IDs but no document content.
func sanitizeLibraryMessage(msg string) string {
	if len(msg) > 300 {
		return msg[:300]
	}
	return msg
}

// shared marks a rule both layers implement with the same semantics: failing
// it after the library passed means the library changed.
type finding struct {
	reason string
	shared bool
	detail string
}

func (v *Verifier) recheck(root *etree.Element, a *crewjam.Assertion, in VerifyInput) (Identity, error) {
	f := v.findings(root, a, in)
	if f == nil {
		return v.identity(root, a), nil
	}
	if !f.shared {
		return Identity{}, newError(f.reason, "%s", f.detail)
	}
	err := newError(ReasonValidatorDisagreement, "library accepted the response but the independent check failed (%s): %s", f.reason, f.detail)
	if v.Log != nil {
		v.Log.Error("saml validator disagreement", "rule", f.reason, "detail", f.detail)
	}
	return Identity{}, err
}

// findings runs the independent checks. Rules that are stricter than crewjam by
// design (single assertion, required audience, required Destination, bearer
// confirmation, the configured skew, NameID and AuthnStatement presence) fail
// with their own reason; the rest are marked shared.
func (v *Verifier) findings(root *etree.Element, a *crewjam.Assertion, in VerifyInput) *finding {
	now, skew := in.Now, v.skew()
	acs := ACSURL(v.BaseURL)

	if n := len(childrenNamed(root, nsAssertion, "Assertion")) + len(childrenNamed(root, nsAssertion, "EncryptedAssertion")); n > 1 {
		return &finding{reason: ReasonMultipleAssertions, detail: fmt.Sprintf("%d assertions", n)}
	}

	// Response element (attributes are only as trustworthy as the signature
	// that covers them; the equality checks below are what matter).
	if code := statusCode(root); code != statusSuccess {
		return &finding{reason: ReasonIdPStatus, shared: true, detail: "status " + code}
	}
	if iss := childText(root, nsAssertion, "Issuer"); iss != "" && iss != v.Cfg.IdPEntityID {
		return &finding{reason: ReasonIssuer, shared: true, detail: "response issuer"}
	}
	switch dest := root.SelectAttrValue("Destination", ""); {
	case dest == "":
		return &finding{reason: ReasonDestination, detail: "Destination is required"}
	case dest != acs:
		return &finding{reason: ReasonDestination, shared: true, detail: "Destination does not match the ACS URL"}
	}
	if root.SelectAttrValue("InResponseTo", "") != in.RequestID {
		return &finding{reason: ReasonInResponseTo, shared: true, detail: "response InResponseTo"}
	}
	if issued, ok := parseXSDTime(root.SelectAttrValue("IssueInstant", "")); !ok || now.After(issued.Add(maxIssueDelay)) {
		return &finding{reason: ReasonExpired, shared: true, detail: "response IssueInstant"}
	}

	// Assertion, as returned by the library from the validated element.
	if a.Issuer.Value != v.Cfg.IdPEntityID {
		return &finding{reason: ReasonIssuer, shared: true, detail: "assertion issuer"}
	}
	if now.After(a.IssueInstant.Add(maxIssueDelay)) {
		return &finding{reason: ReasonExpired, shared: true, detail: "assertion IssueInstant"}
	}
	if a.Subject == nil || len(a.Subject.SubjectConfirmations) == 0 {
		return &finding{reason: ReasonRecipient, detail: "no SubjectConfirmation"}
	}
	for _, sc := range a.Subject.SubjectConfirmations {
		data := sc.SubjectConfirmationData
		if sc.Method != confirmationBearer || data == nil {
			return &finding{reason: ReasonRecipient, detail: "SubjectConfirmation is not bearer with data"}
		}
		if data.Recipient != acs {
			return &finding{reason: ReasonRecipient, shared: true, detail: "Recipient does not match the ACS URL"}
		}
		if data.InResponseTo != in.RequestID {
			return &finding{reason: ReasonInResponseTo, shared: true, detail: "SubjectConfirmationData InResponseTo"}
		}
		if !now.Before(data.NotOnOrAfter.Add(skew)) {
			return &finding{reason: ReasonExpired, detail: "SubjectConfirmationData NotOnOrAfter"}
		}
	}
	var cond crewjam.Conditions
	if a.Conditions != nil {
		cond = *a.Conditions
	}
	if !cond.NotBefore.IsZero() && now.Before(cond.NotBefore.Add(-skew)) {
		return &finding{reason: ReasonNotYetValid, detail: "Conditions NotBefore"}
	}
	if !now.Before(cond.NotOnOrAfter.Add(skew)) { // a missing NotOnOrAfter is the zero time: expired
		return &finding{reason: ReasonExpired, detail: "Conditions NotOnOrAfter"}
	}
	if len(cond.AudienceRestrictions) == 0 {
		return &finding{reason: ReasonAudience, detail: "no AudienceRestriction"}
	}
	matched := false
	for _, r := range cond.AudienceRestrictions {
		if r.Audience.Value == v.spEntityID() {
			matched = true
		}
	}
	if !matched {
		return &finding{reason: ReasonAudience, shared: true, detail: "audience does not contain the SP entity ID"}
	}
	switch {
	case a.Subject.NameID == nil || strings.TrimSpace(a.Subject.NameID.Value) == "":
		return &finding{reason: ReasonNoNameID, detail: "no NameID"}
	case a.Subject.NameID.Format == nameIDTransient:
		return &finding{reason: ReasonTransientNameID, detail: "transient NameID"}
	}
	if len(a.AuthnStatements) == 0 {
		return &finding{reason: ReasonNoAuthnStatement, detail: "no AuthnStatement"}
	}
	return nil
}

func (v *Verifier) identity(root *etree.Element, a *crewjam.Assertion) Identity {
	id := Identity{
		NameID:       strings.TrimSpace(a.Subject.NameID.Value),
		NameIDFormat: a.Subject.NameID.Format,
		Attributes:   map[string][]string{},
		AssertionID:  a.ID,
		ResponseID:   root.SelectAttrValue("ID", ""),
	}
	if a.Conditions != nil {
		id.NotOnOrAfter = a.Conditions.NotOnOrAfter
	}
	for _, st := range a.AuthnStatements {
		if st.SessionNotOnOrAfter != nil && (id.SessionNotOnOrAfter == nil || st.SessionNotOnOrAfter.Before(*id.SessionNotOnOrAfter)) {
			t := *st.SessionNotOnOrAfter
			id.SessionNotOnOrAfter = &t
		}
	}
	values := func(attr crewjam.Attribute) []string {
		out := make([]string, 0, len(attr.Values))
		for _, val := range attr.Values {
			out = append(out, val.Value)
		}
		return out
	}
	for _, st := range a.AttributeStatements {
		for _, attr := range st.Attributes {
			if attr.Name != "" {
				id.Attributes[attr.Name] = append(id.Attributes[attr.Name], values(attr)...)
			}
		}
	}
	for _, st := range a.AttributeStatements { // FriendlyName never shadows a real Name
		for _, attr := range st.Attributes {
			if _, taken := id.Attributes[attr.FriendlyName]; attr.FriendlyName != "" && !taken {
				id.Attributes[attr.FriendlyName] = values(attr)
			}
		}
	}
	return id
}

func childrenNamed(el *etree.Element, ns, tag string) []*etree.Element {
	var out []*etree.Element
	for _, c := range el.ChildElements() {
		if c.Tag == tag && c.NamespaceURI() == ns {
			out = append(out, c)
		}
	}
	return out
}

func childText(el *etree.Element, ns, tag string) string {
	if cs := childrenNamed(el, ns, tag); len(cs) > 0 {
		return strings.TrimSpace(cs[0].Text())
	}
	return ""
}

func statusCode(root *etree.Element) string {
	for _, st := range childrenNamed(root, nsProtocol, "Status") {
		for _, sc := range childrenNamed(st, nsProtocol, "StatusCode") {
			return sc.SelectAttrValue("Value", "")
		}
	}
	return ""
}

func parseXSDTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}
