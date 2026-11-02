package saml

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
)

// MaxClockSkew is the upper bound for the admin-configured clock skew.
const MaxClockSkew = 300 * time.Second

func init() {
	// crewjam keeps its skew in a process-wide variable. Set it once to the
	// hard upper bound; the configured (smaller) skew is enforced by our own
	// validation layer in Verifier.
	crewjam.MaxClockSkew = MaxClockSkew
}

var requestIDPattern = regexp.MustCompile(`^_[0-9a-zA-Z._-]+$`)

// NewRequestID returns "_" + hex(32 random bytes): a valid XML ID that is also
// the saml_requests key and the RelayState.
func NewRequestID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "_" + hex.EncodeToString(b[:]), nil
}

// BuildRedirect returns the IdP redirect URL (SAMLRequest plus RelayState set
// to requestID) and the AuthnRequest XML that was deflated into it. The
// request is unsigned (spec D5). The ACS URL and issuer derive from baseURL.
func BuildRedirect(cfg Config, baseURL, requestID string, now time.Time) (*url.URL, []byte, error) {
	if cfg.IdPSSORedirectURL == "" {
		return nil, nil, errors.New("saml: IdP redirect URL is not configured")
	}
	if !requestIDPattern.MatchString(requestID) {
		return nil, nil, errors.New("saml: invalid request ID")
	}
	acs, err := url.Parse(ACSURL(baseURL))
	if err != nil {
		return nil, nil, err
	}
	entityID := cfg.SPEntityID
	if entityID == "" {
		entityID = SPEntityID(baseURL)
	}
	sp := crewjam.ServiceProvider{
		EntityID:          entityID,
		AcsURL:            *acs,
		AuthnNameIDFormat: nameIDFormatURN(cfg.NameIDFormat),
	}
	req, err := sp.MakeAuthenticationRequest(cfg.IdPSSORedirectURL, crewjam.HTTPRedirectBinding, crewjam.HTTPPostBinding)
	if err != nil {
		return nil, nil, err
	}
	format := string(nameIDFormatURN(cfg.NameIDFormat))
	req.ID = requestID
	req.IssueInstant = now.UTC()
	req.NameIDPolicy.Format = &format // crewjam leaves "unspecified" empty; we always state it
	u, err := req.Redirect(requestID, &sp)
	if err != nil {
		return nil, nil, err
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.Element())
	raw, err := doc.WriteToBytes()
	if err != nil {
		return nil, nil, err
	}
	return u, raw, nil
}
