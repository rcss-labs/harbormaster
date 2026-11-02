// Package fakeidp is an in-process SAML identity provider for tests, the
// Playwright suite and the `serve --fake-aws` demo.
//
// It is development tooling that can emit deliberately invalid responses (see
// Tamper). It must only be mounted under `serve --fake-aws`; nothing in the
// production code path may import it. The HTTP handler never applies Tamper
// unless a Go caller set it with SetTamper, so a mounted handler cannot be
// steered by a request.
package fakeidp

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/rcss-labs/harbormaster/internal/saml"
)

//go:embed submit.js
var submitJS []byte

const (
	pathPrefix   = "/fake-idp"
	maxBodyBytes = 1 << 20
	nameIDEmail  = string(crewjam.EmailAddressNameIDFormat) // the URN the SP metadata requests
	statusOK     = "urn:oasis:names:tc:SAML:2.0:status:Success"
	emailDomain  = "example.test"
)

var userPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)

// Options configure a Server. BaseURL is where the fake IdP is mounted, for
// example http://127.0.0.1:8481. Now defaults to time.Now.
type Options struct {
	BaseURL string
	Now     func() time.Time
}

// Tamper makes a response deliberately wrong. The zero value is well-formed.
type Tamper struct {
	OmitSignature      bool // no signature on the Response or the Assertion; ignores EncryptFor
	SignWithWrongKey   bool // signed by a throwaway key whose certificate the SP does not trust
	DuplicateAssertion bool // two separately signed assertions with distinct IDs
	OmitAuthnStatement bool
	OmitNameID         bool

	OmitResponseIssuer bool   // drop the Response-level Issuer (the assertion keeps its own)
	ResponseIssuer     string // replaces the Response-level Issuer; the assertion Issuer is unchanged

	Audience     string // Conditions/AudienceRestriction/Audience
	Destination  string // Response@Destination
	Recipient    string // SubjectConfirmationData@Recipient
	InResponseTo string // Response@InResponseTo and SubjectConfirmationData@InResponseTo
	NameIDFormat string
	// Status replaces the top-level status code. A non-success status also
	// drops the assertion, as a real IdP would.
	Status string

	NotOnOrAfter        *time.Time // Conditions and SubjectConfirmationData NotOnOrAfter
	NotBefore           *time.Time // Conditions@NotBefore
	SessionNotOnOrAfter *time.Time // AuthnStatement@SessionNotOnOrAfter
	EncryptFor          *x509.Certificate
}

// Server is the fake IdP.
type Server struct {
	baseURL string
	now     func() time.Time
	idp     crewjam.IdentityProvider

	mu     sync.RWMutex
	tamper Tamper

	wrongOnce sync.Once
	wrongCert *x509.Certificate
	wrongKey  saml.SPKey
	wrongErr  error
}

// New generates a fresh signing key pair and returns a ready Server.
func New(opts Options) (*Server, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("fakeidp: BaseURL must be an absolute http(s) URL")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	certPEM, keyPEM, err := saml.GenerateSPKey(u.Hostname(), now())
	if err != nil {
		return nil, err
	}
	pair, err := saml.ParseSPKey(0, certPEM, keyPEM, nil)
	if err != nil {
		return nil, err
	}
	entity, _ := url.Parse(base + pathPrefix)
	sso, _ := url.Parse(base + pathPrefix + "/sso")
	return &Server{
		baseURL: base,
		now:     now,
		idp: crewjam.IdentityProvider{
			Key:             pair.Key,
			Certificate:     pair.Cert,
			MetadataURL:     *entity,
			SSOURL:          *sso,
			SignatureMethod: dsig.RSASHA256SignatureMethod,
		},
	}, nil
}

// EntityID is the IdP entity ID, BaseURL + "/fake-idp".
func (s *Server) EntityID() string { return s.idp.MetadataURL.String() }

func (s *Server) ssoURL() string { return s.idp.SSOURL.String() }

// SetTamper makes every response issued over HTTP use t until it is reset with
// the zero value. It exists for Go tests that drive the full browser-style flow.
func (s *Server) SetTamper(t Tamper) {
	s.mu.Lock()
	s.tamper = t
	s.mu.Unlock()
}

func (s *Server) currentTamper() Tamper {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tamper
}

// Metadata returns the IdP EntityDescriptor XML.
func (s *Server) Metadata() []byte {
	ed := s.idp.Metadata()
	ed.ValidUntil = s.now().Add(crewjam.DefaultValidDuration)
	out, err := xml.MarshalIndent(ed, "", "  ")
	if err != nil {
		panic("fakeidp: marshal metadata: " + err.Error()) // static structure; cannot fail
	}
	return append([]byte(xml.Header), out...)
}

// Handler serves /fake-idp/metadata, /fake-idp/sso and /fake-idp/submit.js.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pathPrefix+"/metadata", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(s.Metadata())
	})
	mux.HandleFunc(pathPrefix+"/submit.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(submitJS)
	})
	mux.HandleFunc(pathPrefix+"/sso", s.serveSSO)
	return mux
}

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Fake identity provider</title></head>
<body>
<h1>Fake identity provider</h1>
<form method="post" action="` + pathPrefix + `/sso">
<input type="hidden" name="SAMLRequest" value="{{.SAMLRequest}}">
<input type="hidden" name="RelayState" value="{{.RelayState}}">
<label>Sign in as <input type="text" name="user" value="{{.User}}" autocomplete="off"></label>
<button type="submit">Continue</button>
</form>
</body></html>
`))

// The page carries no inline script: the CSP is script-src 'self'.
var postPage = template.Must(template.New("post").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Signing in</title></head>
<body>
<form method="post" action="{{.URL}}" id="SAMLResponseForm">
<input type="hidden" name="SAMLResponse" value="{{.SAMLResponse}}">
<input type="hidden" name="RelayState" value="{{.RelayState}}">
<noscript><button type="submit">Continue</button></noscript>
</form>
<script src="` + pathPrefix + `/submit.js"></script>
</body></html>
`))

func (s *Server) serveSSO(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	samlRequest, relay, user := r.Form.Get("SAMLRequest"), r.Form.Get("RelayState"), r.Form.Get("user")
	reqXML, err := inflateRequest(samlRequest)
	if err != nil {
		http.Error(w, "bad SAMLRequest", http.StatusBadRequest)
		return
	}
	var ar crewjam.AuthnRequest
	if err := xml.Unmarshal(reqXML, &ar); err != nil || ar.AssertionConsumerServiceURL == "" {
		http.Error(w, "bad SAMLRequest", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	issue := r.Method == http.MethodPost || r.Form.Get("auto") == "1"
	if !issue {
		if user != "" && !userPattern.MatchString(user) {
			user = ""
		}
		_ = formPage.Execute(w, map[string]string{"SAMLRequest": samlRequest, "RelayState": relay, "User": user})
		return
	}
	if !userPattern.MatchString(user) {
		http.Error(w, "bad user", http.StatusBadRequest)
		return
	}
	resp, err := s.Response(reqXML, user, ar.AssertionConsumerServiceURL, s.currentTamper())
	if err != nil {
		http.Error(w, "cannot build response", http.StatusBadRequest)
		return
	}
	_ = postPage.Execute(w, map[string]string{
		"URL": ar.AssertionConsumerServiceURL, "SAMLResponse": resp, "RelayState": relay,
	})
}

func inflateRequest(param string) ([]byte, error) {
	if param == "" {
		return nil, errors.New("missing SAMLRequest")
	}
	compressed, err := base64.StdEncoding.DecodeString(param)
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(compressed)), maxBodyBytes))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// wrongSigner returns a key pair that no SP trusts, generated on first use.
func (s *Server) wrongSigner() (saml.SPKey, error) {
	s.wrongOnce.Do(func() {
		certPEM, keyPEM, err := saml.GenerateSPKey("untrusted.example.test", s.now())
		if err != nil {
			s.wrongErr = err
			return
		}
		s.wrongKey, s.wrongErr = saml.ParseSPKey(0, certPEM, keyPEM, nil)
	})
	return s.wrongKey, s.wrongErr
}

func randomID(prefix string) string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic("fakeidp: crypto/rand: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

func capitalize(user string) string {
	if user == "" {
		return user
	}
	return strings.ToUpper(user[:1]) + user[1:]
}

// Response builds the base64 SAMLResponse the fake IdP would post to acsURL for
// authnRequestXML (the raw, inflated AuthnRequest). Pass the zero Tamper for a
// well-formed response.
func (s *Server) Response(authnRequestXML []byte, user, acsURL string, t Tamper) (string, error) {
	if !userPattern.MatchString(user) {
		return "", errors.New("fakeidp: user must match ^[a-z][a-z0-9.-]*$")
	}
	if t.OmitSignature && t.EncryptFor != nil {
		return "", errors.New("fakeidp: OmitSignature and EncryptFor cannot be combined")
	}
	lower := bytes.ToLower(authnRequestXML)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return "", errors.New("fakeidp: DTDs are not accepted")
	}
	var ar crewjam.AuthnRequest
	if err := xml.Unmarshal(authnRequestXML, &ar); err != nil {
		return "", fmt.Errorf("fakeidp: parse AuthnRequest: %w", err)
	}
	if ar.Issuer == nil || ar.Issuer.Value == "" || ar.ID == "" {
		return "", errors.New("fakeidp: AuthnRequest has no Issuer or ID")
	}

	idp := s.idp // per-call copy so the signing key can be swapped without races
	if t.SignWithWrongKey {
		wrong, err := s.wrongSigner()
		if err != nil {
			return "", err
		}
		idp.Key, idp.Certificate = wrong.Key, wrong.Cert
	}

	now := s.now()
	spDescriptor := crewjam.SPSSODescriptor{
		AssertionConsumerServices: []crewjam.IndexedEndpoint{{Binding: crewjam.HTTPPostBinding, Location: acsURL, Index: 0}},
	}
	if t.EncryptFor != nil {
		spDescriptor.KeyDescriptors = []crewjam.KeyDescriptor{{
			Use: "encryption",
			KeyInfo: crewjam.KeyInfo{X509Data: crewjam.X509Data{X509Certificates: []crewjam.X509Certificate{
				{Data: base64.StdEncoding.EncodeToString(t.EncryptFor.Raw)},
			}}},
		}}
	}
	spMetadata := &crewjam.EntityDescriptor{EntityID: ar.Issuer.Value, SPSSODescriptors: []crewjam.SPSSODescriptor{spDescriptor}}
	ir := &crewjam.IdpAuthnRequest{
		IDP:                     &idp,
		HTTPRequest:             &http.Request{RemoteAddr: "127.0.0.1"},
		RequestBuffer:           authnRequestXML,
		Request:                 ar,
		ServiceProviderMetadata: spMetadata,
		SPSSODescriptor:         &spMetadata.SPSSODescriptors[0],
		ACSEndpoint:             &spMetadata.SPSSODescriptors[0].AssertionConsumerServices[0],
		Now:                     now,
	}

	email := user + "@" + emailDomain
	attr := func(name, value string) crewjam.Attribute {
		return crewjam.Attribute{Name: name, Values: []crewjam.AttributeValue{{Type: "xs:string", Value: value}}}
	}
	session := &crewjam.Session{
		ID: randomID("s-"), CreateTime: now, ExpireTime: now.Add(8 * time.Hour), Index: randomID("i-"),
		NameID: email, NameIDFormat: nameIDEmail,
		CustomAttributes: []crewjam.Attribute{
			attr("email", email), attr("displayName", capitalize(user)),
			attr("givenName", capitalize(user)), attr("sn", "Example"),
		},
	}
	if err := (crewjam.DefaultAssertionMaker{}).MakeAssertion(ir, session); err != nil {
		return "", err
	}
	applyAssertionTamper(ir.Assertion, t)

	status := statusOK
	if t.Status != "" {
		status = t.Status
	}
	destination := acsURL
	if t.Destination != "" {
		destination = t.Destination
	}
	inResponseTo := ar.ID
	if t.InResponseTo != "" {
		inResponseTo = t.InResponseTo
	}

	var assertionEls []*etree.Element
	if status == statusOK {
		count := 1
		if t.DuplicateAssertion {
			count = 2
		}
		for i := 0; i < count; i++ {
			el, err := s.assertionElement(ir, t, i > 0)
			if err != nil {
				return "", err
			}
			assertionEls = append(assertionEls, el)
		}
	}

	response := &crewjam.Response{
		Destination:  destination,
		ID:           randomID("id-"),
		InResponseTo: inResponseTo,
		IssueInstant: now,
		Version:      "2.0",
		Issuer:       &crewjam.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: idp.MetadataURL.String()},
		Status:       crewjam.Status{StatusCode: crewjam.StatusCode{Value: status}},
	}
	if t.OmitResponseIssuer {
		response.Issuer = nil
	} else if t.ResponseIssuer != "" {
		response.Issuer.Value = t.ResponseIssuer
	}
	build := func() *etree.Element {
		el := response.Element()
		for _, a := range assertionEls {
			el.AddChild(a)
		}
		return el
	}
	responseEl := build()
	if !t.OmitSignature {
		sc, err := signingContext(&idp)
		if err != nil {
			return "", err
		}
		signed, err := sc.SignEnveloped(responseEl)
		if err != nil {
			return "", err
		}
		children := signed.ChildElements()
		response.Signature = children[len(children)-1]
		responseEl = build()
	}
	doc := etree.NewDocument()
	doc.SetRoot(responseEl)
	raw, err := doc.WriteToBytes()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func applyAssertionTamper(a *crewjam.Assertion, t Tamper) {
	if t.OmitNameID {
		a.Subject.NameID = nil
	} else if t.NameIDFormat != "" {
		a.Subject.NameID.Format = t.NameIDFormat
	}
	for i := range a.Subject.SubjectConfirmations {
		scd := a.Subject.SubjectConfirmations[i].SubjectConfirmationData
		if t.Recipient != "" {
			scd.Recipient = t.Recipient
		}
		if t.InResponseTo != "" {
			scd.InResponseTo = t.InResponseTo
		}
		if t.NotOnOrAfter != nil {
			scd.NotOnOrAfter = *t.NotOnOrAfter
		}
	}
	if t.Audience != "" {
		a.Conditions.AudienceRestrictions = []crewjam.AudienceRestriction{{Audience: crewjam.Audience{Value: t.Audience}}}
	}
	if t.NotBefore != nil {
		a.Conditions.NotBefore = *t.NotBefore
	}
	if t.NotOnOrAfter != nil {
		a.Conditions.NotOnOrAfter = *t.NotOnOrAfter
	}
	if t.OmitAuthnStatement {
		a.AuthnStatements = nil
	} else if t.SessionNotOnOrAfter != nil {
		for i := range a.AuthnStatements {
			a.AuthnStatements[i].SessionNotOnOrAfter = t.SessionNotOnOrAfter
		}
	}
}

// assertionElement returns the signed (and optionally encrypted) assertion
// element. freshID gives the assertion a new ID so a duplicate is not identical.
func (s *Server) assertionElement(ir *crewjam.IdpAuthnRequest, t Tamper, freshID bool) (*etree.Element, error) {
	copied := *ir.Assertion
	copied.Signature = nil
	if freshID {
		copied.ID = randomID("id-")
	}
	ir.Assertion = &copied
	if t.OmitSignature {
		return copied.Element(), nil
	}
	ir.AssertionEl = nil
	if err := ir.MakeAssertionEl(); err != nil {
		return nil, err
	}
	return ir.AssertionEl, nil
}

func signingContext(idp *crewjam.IdentityProvider) (*dsig.SigningContext, error) {
	keyPair := tls.Certificate{
		Certificate: [][]byte{idp.Certificate.Raw},
		PrivateKey:  idp.Key,
		Leaf:        idp.Certificate,
	}
	sc := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(keyPair))
	sc.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if err := sc.SetSignatureMethod(idp.SignatureMethod); err != nil {
		return nil, err
	}
	return sc, nil
}
