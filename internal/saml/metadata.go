package saml

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	crewjam "github.com/crewjam/saml"
)

const (
	maxMetadataBytes   = 1 << 20
	maxSigningCerts    = 5
	maxRedirects       = 3
	fetchTimeout       = 10 * time.Second
	retiredKeyLifetime = 7 * 24 * time.Hour
)

type IdPMetadata struct {
	EntityID       string
	SSORedirectURL string
	SSOPostURL     string
	SigningCerts   []*x509.Certificate
}

var errInvalidMetadata = errors.New("Metadata is not valid SAML metadata.")

// ParseIdPMetadata parses a single-IdP EntityDescriptor. Error text is shown to
// admins verbatim (HTTP 422) and never echoes document content. The metadata's
// own signature, if any, is not verified: trust comes from the admin importing
// it over https or by hand.
func ParseIdPMetadata(xmlBytes []byte, now time.Time) (IdPMetadata, error) {
	if len(xmlBytes) > maxMetadataBytes {
		return IdPMetadata{}, errors.New("Metadata is larger than 1 MiB.")
	}
	lower := bytes.ToLower(xmlBytes)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return IdPMetadata{}, errInvalidMetadata
	}
	var ed crewjam.EntityDescriptor
	if err := xml.Unmarshal(xmlBytes, &ed); err != nil {
		return IdPMetadata{}, errInvalidMetadata
	}
	if !ed.ValidUntil.IsZero() && now.After(ed.ValidUntil) {
		return IdPMetadata{}, fmt.Errorf("Metadata expired on %s.", ed.ValidUntil.UTC().Format("2006-01-02"))
	}
	if len(ed.IDPSSODescriptors) == 0 {
		return IdPMetadata{}, errors.New("Metadata has no IDPSSODescriptor.")
	}
	if len(ed.IDPSSODescriptors) > 1 {
		return IdPMetadata{}, errors.New("Metadata has more than one IDPSSODescriptor.")
	}
	if strings.TrimSpace(ed.EntityID) == "" {
		return IdPMetadata{}, errors.New("Metadata has no entityID.")
	}
	idp := ed.IDPSSODescriptors[0]
	out := IdPMetadata{EntityID: strings.TrimSpace(ed.EntityID)}

	for _, s := range idp.SingleSignOnServices {
		switch s.Binding {
		case crewjam.HTTPRedirectBinding:
			if out.SSORedirectURL == "" {
				out.SSORedirectURL = strings.TrimSpace(s.Location)
			}
		case crewjam.HTTPPostBinding:
			if out.SSOPostURL == "" {
				out.SSOPostURL = strings.TrimSpace(s.Location)
			}
		}
	}
	if out.SSORedirectURL == "" {
		return IdPMetadata{}, errors.New("Metadata has no HTTP-Redirect SingleSignOnService.")
	}
	if !isHTTPURL(out.SSORedirectURL) {
		return IdPMetadata{}, errors.New("Metadata has an HTTP-Redirect SingleSignOnService that is not an http(s) URL.")
	}
	if out.SSOPostURL != "" && !isHTTPURL(out.SSOPostURL) {
		out.SSOPostURL = ""
	}

	var raw []string
	for _, kd := range idp.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}
		for _, c := range kd.KeyInfo.X509Data.X509Certificates {
			raw = append(raw, c.Data)
		}
	}
	if len(raw) == 0 {
		return IdPMetadata{}, errors.New("Metadata has no signing certificate.")
	}
	if len(raw) > maxSigningCerts {
		return IdPMetadata{}, fmt.Errorf("Metadata has more than %d signing certificates.", maxSigningCerts)
	}
	seen := map[string]bool{}
	for i, data := range raw {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(data), ""))
		if err != nil {
			return IdPMetadata{}, fmt.Errorf("Certificate %d is not a valid X.509 certificate.", i+1)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return IdPMetadata{}, fmt.Errorf("Certificate %d is not a valid X.509 certificate.", i+1)
		}
		if now.After(cert.NotAfter) {
			return IdPMetadata{}, fmt.Errorf("Certificate %d expired on %s.", i+1, cert.NotAfter.UTC().Format("2006-01-02"))
		}
		if now.Before(cert.NotBefore) {
			return IdPMetadata{}, fmt.Errorf("Certificate %d is not valid until %s.", i+1, cert.NotBefore.UTC().Format("2006-01-02"))
		}
		if fp := Fingerprint(cert); !seen[fp] {
			seen[fp] = true
			out.SigningCerts = append(out.SigningCerts, cert)
		}
	}
	return out, nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// MetadataFetcher downloads IdP metadata with SSRF protection.
//
// In production mode (AllowInsecure=false) only https URLs are fetched, the
// connection is refused for loopback, link-local (including IMDS and the ECS
// task metadata endpoint), RFC 1918, CGNAT, unspecified and multicast
// addresses (checked on the address actually dialled, so DNS rebinding and
// redirects cannot bypass it), proxies from the environment are ignored, and
// Client's transport is replaced. In AllowInsecure mode (local dev and tests)
// http is allowed, no address filter applies, and Client is used as given.
type MetadataFetcher struct {
	Client        *http.Client
	AllowInsecure bool
	MaxBytes      int64 // default 1 MiB
}

var errBlockedAddress = errors.New("metadata host resolves to a blocked address")

func (f *MetadataFetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, errors.New("Metadata URL is not valid.")
	}
	if u.User != nil {
		return nil, errors.New("Metadata URL must not contain credentials.")
	}
	if u.Scheme != "https" && !(f.AllowInsecure && u.Scheme == "http") {
		return nil, errors.New("Fetching metadata needs an https URL.")
	}
	limit := f.MaxBytes
	if limit <= 0 {
		limit = maxMetadataBytes
	}

	client := f.client()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("Metadata URL is not valid.")
	}
	req.Header.Set("Accept", "application/samlmetadata+xml, application/xml, text/xml")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return nil, errors.New("Metadata URL points to a private or local address.")
		}
		return nil, errors.New("Could not fetch metadata from that URL.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Fetching metadata failed with HTTP status %d.", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("Could not read metadata from that URL.")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("Metadata is larger than the size limit.")
	}
	return body, nil
}

func (f *MetadataFetcher) client() *http.Client {
	c := &http.Client{Timeout: fetchTimeout}
	if f.Client != nil {
		cp := *f.Client
		c = &cp
		if c.Timeout == 0 {
			c.Timeout = fetchTimeout
		}
	}
	if f.AllowInsecure {
		if f.Client == nil {
			c.Transport = &http.Transport{Proxy: nil}
		}
	} else {
		dialer := &net.Dialer{Timeout: 5 * time.Second, Control: blockedAddressControl}
		c.Transport = &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			ForceAttemptHTTP2:   true,
		}
	}
	c.CheckRedirect = f.checkRedirect
	return c
}

func (f *MetadataFetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" && !(f.AllowInsecure && req.URL.Scheme == "http") {
		return errors.New("redirect to a non-https URL")
	}
	return nil
}

// blockedAddressControl runs on every outgoing connection attempt with the
// already-resolved address.
func blockedAddressControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || isBlockedIP(ip) {
		return errBlockedAddress
	}
	return nil
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	zeroNet   = netip.MustParsePrefix("0.0.0.0/8")
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	nat64Loc  = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func isBlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	// Prefixes that embed an IPv4 address are refused outright.
	if nat64.Contains(ip) || nat64Loc.Contains(ip) || sixToFour.Contains(ip) {
		return true
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() ||
		ip.IsUnspecified() || cgnat.Contains(ip) || zeroNet.Contains(ip)
}

// Config is the SSO configuration the SAML package needs. The row-to-Config
// adapter lives in internal/api.
type Config struct {
	Enabled                                       bool
	IdPEntityID, IdPSSORedirectURL, IdPSSOPostURL string
	IdPCerts                                      []*x509.Certificate
	SPEntityID, NameIDFormat                      string
	AttrEmail, AttrDisplayName, AttrUsername      string
	ClockSkew                                     time.Duration
	ButtonLabel                                   string
}

func trimBase(baseURL string) string { return strings.TrimRight(baseURL, "/") }

func ACSURL(baseURL string) string      { return trimBase(baseURL) + "/api/auth/saml/acs" }
func MetadataURL(baseURL string) string { return trimBase(baseURL) + "/api/auth/saml/metadata" }
func SPEntityID(baseURL string) string  { return MetadataURL(baseURL) }

func nameIDFormatURN(format string) crewjam.NameIDFormat {
	switch format {
	case "email", string(crewjam.EmailAddressNameIDFormat):
		return crewjam.EmailAddressNameIDFormat
	case "persistent", string(crewjam.PersistentNameIDFormat):
		return crewjam.PersistentNameIDFormat
	default:
		return crewjam.UnspecifiedNameIDFormat
	}
}

// SPMetadata renders the SP EntityDescriptor: the active certificate and any
// certificate retired less than seven days ago as encryption KeyDescriptors.
func SPMetadata(cfg Config, baseURL string, active SPKey, retired []SPKey, now time.Time) ([]byte, error) {
	if active.Cert == nil {
		return nil, errors.New("no active SP certificate")
	}
	entityID := cfg.SPEntityID
	if entityID == "" {
		entityID = SPEntityID(baseURL)
	}
	keyDescriptor := func(c *x509.Certificate) crewjam.KeyDescriptor {
		return crewjam.KeyDescriptor{
			Use: "encryption",
			KeyInfo: crewjam.KeyInfo{X509Data: crewjam.X509Data{X509Certificates: []crewjam.X509Certificate{
				{Data: base64.StdEncoding.EncodeToString(c.Raw)},
			}}},
			EncryptionMethods: []crewjam.EncryptionMethod{
				{Algorithm: "http://www.w3.org/2001/04/xmlenc#aes128-cbc"},
				{Algorithm: "http://www.w3.org/2001/04/xmlenc#aes256-cbc"},
				{Algorithm: "http://www.w3.org/2001/04/xmlenc#rsa-oaep-mgf1p"},
			},
		}
	}
	kds := []crewjam.KeyDescriptor{keyDescriptor(active.Cert)}
	for _, r := range retired {
		if r.Cert == nil || (r.RetiredAt != nil && now.Sub(*r.RetiredAt) >= retiredKeyLifetime) {
			continue
		}
		kds = append(kds, keyDescriptor(r.Cert))
	}
	no, yes := false, true
	ed := crewjam.EntityDescriptor{
		EntityID: entityID,
		SPSSODescriptors: []crewjam.SPSSODescriptor{{
			SSODescriptor: crewjam.SSODescriptor{
				RoleDescriptor: crewjam.RoleDescriptor{
					ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
					KeyDescriptors:             kds,
				},
				NameIDFormats: []crewjam.NameIDFormat{nameIDFormatURN(cfg.NameIDFormat)},
			},
			AuthnRequestsSigned:  &no,
			WantAssertionsSigned: &yes,
			AssertionConsumerServices: []crewjam.IndexedEndpoint{{
				Binding:   crewjam.HTTPPostBinding,
				Location:  ACSURL(baseURL),
				Index:     0,
				IsDefault: &yes,
			}},
		}},
	}
	out, err := xml.MarshalIndent(ed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render SP metadata: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}
