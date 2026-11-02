package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	samlBindingCookie = "hm_saml_req"
	samlACSPath       = "/api/auth/saml/acs"
	maxACSBodyBytes   = 256 << 10
	maxAssertionTTL   = 24 * time.Hour
	testResultTTL     = 10 * time.Minute
	loginBucketSize   = 60 // GET /api/auth/saml/login and POST /api/auth/saml/link: 60 per IP, refilled at 1/s
	maxReturnToLength = 2048

	// reasonRateLimited is audited when the per-IP failure limit refuses an ACS POST before any parsing.
	// It is not part of the validator's closed reason set; users see the generic sso_failed.
	reasonRateLimited = "rate_limited"
)

// samlService is the SAML state shared by the public routes. New fills the fields a caller leaves
// nil; tests pre-set verify to observe or count verifications.
type samlService struct {
	now    func() time.Time
	cipher *saml.KeyCipher // nil when HM_SESSION_KEY is empty
	bucket *ipBucket
	verify func(v *saml.Verifier, in saml.VerifyInput) (saml.Identity, error)
	// fetcher downloads IdP metadata. The default is https-only with the private/link-local address
	// filter; only the local demo mode (HM_FAKE_AWS) relaxes it. Cookie flags never decide SSRF protection.
	fetcher *saml.MetadataFetcher
}

func newSAMLService(svc *samlService, s *Server) *samlService {
	if svc == nil {
		svc = &samlService{}
	}
	if svc.now == nil {
		svc.now = s.Now
	}
	if svc.cipher == nil && s.Cfg.SessionKey != "" {
		if cipher, err := saml.NewKeyCipher(s.Cfg.SessionKey); err == nil {
			svc.cipher = cipher
		}
	}
	if svc.bucket == nil {
		svc.bucket = newIPBucket(loginBucketSize, 1, s.Now)
	}
	if svc.fetcher == nil {
		svc.fetcher = &saml.MetadataFetcher{AllowInsecure: s.Cfg.FakeAWS}
	}
	if svc.verify == nil {
		svc.verify = func(v *saml.Verifier, in saml.VerifyInput) (saml.Identity, error) { return v.Verify(in) }
	}
	return svc
}

// samlConfig loads the stored configuration as the saml package's Config. store.ErrNotFound when SSO was never configured.
func (s *Server) samlConfig(ctx context.Context) (saml.Config, store.SAMLConfigRow, error) {
	row, err := s.Store.GetSAMLConfig(ctx)
	if err != nil {
		return saml.Config{}, store.SAMLConfigRow{}, err
	}
	cfg, err := samlConfigFromRow(row)
	if err != nil {
		return saml.Config{}, store.SAMLConfigRow{}, err
	}
	return cfg, row, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// samlKeys decrypts the stored SP keys, active first. Keys that cannot be decrypted are skipped;
// saml.ErrUndecryptable is returned when keys exist but none could be used.
func (s *Server) samlKeys(ctx context.Context) ([]saml.SPKey, error) {
	rows, err := s.Store.ListSPKeys(ctx)
	if err != nil {
		return nil, err
	}
	var keys []saml.SPKey
	for _, row := range rows {
		if s.SAML.cipher == nil {
			break
		}
		keyPEM, err := s.SAML.cipher.Open(row.KeyEnc)
		if err != nil {
			continue
		}
		key, err := saml.ParseSPKey(row.ID, row.CertPEM, keyPEM, row.RetiredAt)
		if err != nil {
			continue
		}
		keys = append(keys, key)
	}
	if len(rows) > 0 && len(keys) == 0 {
		return nil, saml.ErrUndecryptable
	}
	return keys, nil
}

// AuthOptionsDTO is the body of GET /api/auth/options.
type AuthOptionsDTO struct {
	SSO           *AuthOptionsSSODTO `json:"sso"`
	PasswordLogin string             `json:"password_login"`
}

// AuthOptionsSSODTO describes the SSO button.
type AuthOptionsSSODTO struct {
	Enabled bool   `json:"enabled"`
	Label   string `json:"label"`
}

func (s *Server) authOptions(w http.ResponseWriter, r *http.Request) {
	_, row, err := s.samlConfig(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, AuthOptionsDTO{PasswordLogin: "all"})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	options := AuthOptionsDTO{SSO: &AuthOptionsSSODTO{Enabled: row.Enabled, Label: row.ButtonLabel}, PasswordLogin: "all"}
	if row.Enabled {
		options.PasswordLogin = "break_glass"
	}
	writeJSON(w, http.StatusOK, options)
}

// safeReturnTo keeps raw only when it is a same-origin relative path: it starts with a single "/",
// holds no backslash or control character (browsers strip tabs and newlines inside URLs) and parses
// without a scheme or host. Anything else is "/".
func safeReturnTo(raw string) string {
	if raw == "" || len(raw) > maxReturnToLength || raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return "/"
	}
	for _, c := range raw {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return "/"
	}
	return raw
}

func (s *Server) samlRequestTTL() time.Duration {
	return cmp.Or(s.Cfg.SAMLRequestTTL, 10*time.Minute)
}

func (s *Server) bindingCookie(value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name: samlBindingCookie, Value: value, Path: samlACSPath, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	if s.Cfg.CookieSecure {
		cookie.Secure = true
		cookie.SameSite = http.SameSiteNoneMode
	}
	return cookie
}

// startFlow stores a request row (userID only for purpose "link"), sets the binding cookie and
// returns the IdP redirect URL.
func (s *Server) startFlow(w http.ResponseWriter, r *http.Request, cfg saml.Config, purpose string, userID *int64, returnTo string) (string, error) {
	now := s.SAML.now()
	id, err := saml.NewRequestID()
	if err != nil {
		return "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	cookieValue := base64.RawURLEncoding.EncodeToString(secret[:])
	redirect, _, err := saml.BuildRedirect(cfg, s.Cfg.BaseURL, id, now)
	if err != nil {
		return "", err
	}
	ttl := s.samlRequestTTL()
	err = s.Store.CreateSAMLRequest(r.Context(), store.SAMLRequest{
		ID: id, Purpose: purpose, UserID: userID, ReturnTo: returnTo,
		BindingHash: sha256Hex(cookieValue), CreatedAt: now, ExpiresAt: now.Add(ttl),
	})
	if err != nil {
		return "", err
	}
	http.SetCookie(w, s.bindingCookie(cookieValue, int(ttl.Seconds())))
	return redirect.String(), nil
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Server) rateLimited(w http.ResponseWriter, r *http.Request) bool {
	if s.SAML.bucket.Allow(s.clientIP(r)) {
		return false
	}
	s.fail(w, errRateLimited)
	return true
}

func (s *Server) samlLogin(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, r) {
		return
	}
	cfg, row, err := s.samlConfig(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	purpose, returnTo := "login", safeReturnTo(r.URL.Query().Get("return_to"))
	if r.URL.Query().Get("test") == "1" {
		// auth.Middleware runs on public routes too, so the session is available here.
		if _, ok := auth.SessionFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		if !auth.SubjectFrom(r.Context()).Admin {
			writeError(w, http.StatusForbidden, "forbidden", "Only global admins can do this.")
			return
		}
		purpose, returnTo = "test", "/"
	} else if !row.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	location, err := s.startFlow(w, r, cfg, purpose, nil, returnTo)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

func (s *Server) samlLinkStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if s.rateLimited(w, r) {
		return
	}
	cfg, _, err := s.samlConfig(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	if _, err := s.Auth.StartLink(r.Context(), user.ID, body.Password, s.clientIP(r)); err != nil {
		switch {
		case errors.Is(err, auth.ErrNoPassword):
			err = invalid(auth.ErrNoPassword.Error())
		case errors.Is(err, store.ErrAlreadyLinked):
			err = &APIError{Status: http.StatusConflict, Code: "already_linked", Message: "This account is already linked to an SSO identity."}
		}
		s.fail(w, err)
		return
	}
	location, err := s.startFlow(w, r, cfg, "link", &user.ID, "/")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": location})
}

func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := s.samlConfig(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	keys, err := s.samlKeys(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(keys) == 0 || keys[0].RetiredAt != nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	body, err := saml.SPMetadata(cfg, s.Cfg.BaseURL, keys[0], keys[1:], s.SAML.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Content-Disposition", `attachment; filename="harbormaster-sp-metadata.xml"`)
	_, _ = w.Write(body)
}

// ssoTestResult is what the ACS stores in saml_requests.result for a test sign-in. The JSON names
// are those of the admin API's SSOTestResultDTO, which reads it back.
type ssoTestResult struct {
	OK           bool                `json:"ok"`
	Reason       string              `json:"reason"`
	Message      string              `json:"message"`
	NameID       string              `json:"name_id"`
	NameIDFormat string              `json:"name_id_format"`
	Attributes   map[string][]string `json:"attributes"`
	Username     string              `json:"username"`
	Outcome      string              `json:"outcome"`
	ExistingUser *string             `json:"existing_user"`
}

// testFailureMessages are the admin-facing explanations shown by Test sign-in.
var testFailureMessages = map[string]string{
	saml.ReasonTransientNameID:  "Your IdP issues a transient NameID; choose persistent, email or unspecified in the IdP app.",
	saml.ReasonUnsigned:         "The response is not signed. Sign the response or the assertion in the IdP app.",
	saml.ReasonInvalidSignature: "The signature does not match any configured IdP certificate. Re-import the IdP metadata.",
	saml.ReasonIdPStatus:        "The IdP declined the sign-in.",
	saml.ReasonAudience:         "The audience does not match the SP entity ID. Check the IdP app's audience setting.",
	saml.ReasonDestination:      "The destination does not match the ACS URL. Check the IdP app's ACS URL.",
	saml.ReasonRecipient:        "The recipient does not match the ACS URL. Check the IdP app's ACS URL.",
	saml.ReasonIssuer:           "The issuer does not match the configured IdP entity ID.",
	saml.ReasonExpired:          "The response has expired. Check the clock skew setting and the clocks of both systems.",
	saml.ReasonNotYetValid:      "The response is not valid yet. Check the clock skew setting and the clocks of both systems.",
	saml.ReasonNoNameID:         "The response has no NameID.",
	saml.ReasonDecryptFailed:    "The assertion could not be decrypted. Update the IdP with the current SP metadata.",
}

func testFailureMessage(reason string) string {
	if m, ok := testFailureMessages[reason]; ok {
		return m
	}
	return fmt.Sprintf("The response was rejected (%s). See the audit log.", reason)
}

// acsState is what the ACS knows about the request while it runs; fields fill in as it progresses.
type acsState struct {
	ip       string
	row      *store.SAMLRequest
	identity *saml.Identity
}

func (s *Server) samlACS(w http.ResponseWriter, r *http.Request) {
	// Cleared on every outcome. The session cookie is never read here: auth.Middleware skips this path.
	http.SetCookie(w, s.bindingCookie("", -1))
	st := &acsState{ip: s.clientIP(r)}
	ctx := r.Context()
	now := s.SAML.now()

	// The per-IP failure limit is checked before anything is parsed.
	_, byIP, err := s.Store.FailedAttempts(ctx, "sso:"+st.ip, st.ip, now.Add(-s.Cfg.LoginWindow))
	if err != nil {
		s.acsFail(w, r, st, saml.ReasonInternal, err, false)
		return
	}
	if byIP >= s.Cfg.LoginMaxPerIP {
		s.acsFail(w, r, st, reasonRateLimited, nil, false)
		return
	}

	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/x-www-form-urlencoded" {
		s.acsFail(w, r, st, saml.ReasonMalformed, nil, false)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxACSBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.acsFail(w, r, st, saml.ReasonMalformed, nil, false)
		return
	}
	samlResponse, relayState := r.PostForm.Get("SAMLResponse"), r.PostForm.Get("RelayState")
	if samlResponse == "" {
		s.acsFail(w, r, st, saml.ReasonMalformed, nil, false)
		return
	}

	row, err := s.Store.ConsumeSAMLRequest(ctx, relayState, now)
	if errors.Is(err, store.ErrNotFound) {
		s.acsFail(w, r, st, saml.ReasonUnknownRequest, nil, false)
		return
	}
	if err != nil {
		s.acsFail(w, r, st, saml.ReasonInternal, err, false)
		return
	}
	st.row = &row

	cfg, configRow, err := s.samlConfig(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound) && row.Purpose == "login":
		s.acsFail(w, r, st, saml.ReasonSSOOff, nil, false)
		return
	case err != nil:
		s.acsFail(w, r, st, saml.ReasonInternal, err, false)
		return
	}
	// Only a real sign-in needs SSO to be on; test and link work while it is off.
	if row.Purpose == "login" && !configRow.Enabled {
		s.acsFail(w, r, st, saml.ReasonSSOOff, nil, false)
		return
	}

	cookie, err := r.Cookie(samlBindingCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(sha256Hex(cookie.Value)), []byte(row.BindingHash)) != 1 {
		s.acsFail(w, r, st, saml.ReasonBindingMismatch, nil, false)
		return
	}

	keys, err := s.samlKeys(ctx)
	if err != nil {
		s.log().Warn("saml: SP keys unavailable", "error", err)
	}
	verifier := &saml.Verifier{Cfg: cfg, BaseURL: s.Cfg.BaseURL, Log: s.log()}
	identity, err := s.SAML.verify(verifier, saml.VerifyInput{ResponseB64: samlResponse, RequestID: row.ID, Now: now, Keys: keys})
	if err != nil {
		s.acsFail(w, r, st, saml.ReasonOf(err), err, false)
		return
	}
	st.identity = &identity

	// Replay protection comes before any user lookup, user creation, link or stored test result.
	if err := s.rememberAssertion(ctx, identity, cfg.ClockSkew, now); err != nil {
		s.acsFail(w, r, st, saml.ReasonOf(err), err, false)
		return
	}

	switch row.Purpose {
	case "test":
		s.acsTest(w, r, st, cfg, row, identity)
	case "link":
		s.acsLink(w, r, st, cfg, row, identity)
	default:
		s.acsLogin(w, r, st, cfg, row, identity)
	}
}

// rememberAssertion records the assertion and response IDs until the assertion can no longer be
// valid (NotOnOrAfter plus skew, at most 24 h); a repeat is a replay.
func (s *Server) rememberAssertion(ctx context.Context, id saml.Identity, skew time.Duration, now time.Time) error {
	if id.AssertionID == "" {
		return &saml.Error{Reason: saml.ReasonMalformed, Err: errors.New("assertion has no ID")}
	}
	expires := id.NotOnOrAfter.Add(skew)
	if id.NotOnOrAfter.IsZero() || expires.Before(now) {
		expires = now.Add(skew + 2*time.Minute)
	}
	if limit := now.Add(maxAssertionTTL); expires.After(limit) {
		expires = limit
	}
	for _, key := range []string{id.AssertionID, id.ResponseID} {
		if key == "" {
			continue
		}
		err := s.Store.RememberAssertionID(ctx, key, expires)
		if errors.Is(err, store.ErrConflict) {
			return &saml.Error{Reason: saml.ReasonReplay, Err: errors.New("assertion or response ID seen before")}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) acsLogin(w http.ResponseWriter, r *http.Request, st *acsState, cfg saml.Config, row store.SAMLRequest, identity saml.Identity) {
	result, _, err := s.Auth.LoginSSO(r.Context(), identity, cfg, st.ip, r.UserAgent())
	if err != nil {
		s.acsFail(w, r, st, saml.ReasonOf(err), err, false)
		return
	}
	s.Auth.SetCookie(w, result.SessionID)
	redirectTo(w, safeReturnTo(row.ReturnTo))
}

func (s *Server) acsLink(w http.ResponseWriter, r *http.Request, st *acsState, cfg saml.Config, row store.SAMLRequest, identity saml.Identity) {
	if row.UserID == nil { // the table constraint makes this impossible; fail closed
		s.acsFail(w, r, st, saml.ReasonInternal, errors.New("link request without user"), false)
		return
	}
	if _, err := s.Auth.LinkSSO(r.Context(), *row.UserID, identity, cfg, st.ip); err != nil {
		switch saml.ReasonOf(err) {
		case auth.ReasonSSOIdentityTaken, auth.ReasonLinkUserGone, auth.ReasonLinkUserHasIdentity:
			s.acsFail(w, r, st, saml.ReasonOf(err), err, true) // LinkSSO already audited sso_link_failed
		default:
			s.acsFail(w, r, st, saml.ReasonOf(err), err, false)
		}
		return
	}
	redirectTo(w, withQuery(safeReturnTo(row.ReturnTo), "sso_link", "ok"))
}

func (s *Server) acsTest(w http.ResponseWriter, r *http.Request, st *acsState, cfg saml.Config, row store.SAMLRequest, identity saml.Identity) {
	preview, err := s.Auth.PreviewSSO(r.Context(), identity, cfg)
	if err != nil {
		s.acsFail(w, r, st, saml.ReasonOf(err), err, false)
		return
	}
	result := ssoTestResult{
		OK: true, NameID: identity.NameID, NameIDFormat: identity.NameIDFormat, Attributes: identity.Attributes,
		Username: preview.Username, Outcome: preview.Outcome, ExistingUser: preview.ExistingUser,
	}
	if err := s.storeTestResult(r.Context(), row, result); err != nil {
		s.acsFail(w, r, st, saml.ReasonInternal, err, false)
		return
	}
	// The sso_test audit entry is written when the admin reads the result (GET /api/sso/tests/{id}),
	// because only that request knows who the admin is.
	redirectTo(w, "/settings/sso?test="+url.QueryEscape(row.ID))
}

func (s *Server) storeTestResult(ctx context.Context, row store.SAMLRequest, result ssoTestResult) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.Store.PutSAMLTestResult(ctx, row, encoded, s.SAML.now().Add(testResultTTL))
}

// acsFail records and audits a failed ACS POST and redirects the browser. cause is logged, never shown.
// auditedAlready is set when the auth service wrote the audit entry (link failures).
func (s *Server) acsFail(w http.ResponseWriter, r *http.Request, st *acsState, reason string, cause error, auditedAlready bool) {
	ctx := r.Context()
	if reason == saml.ReasonInternal {
		s.log().Error("saml: internal error at the ACS", "error", cause)
	} else if cause != nil {
		s.log().Warn("saml: sign-in refused", "reason", reason, "error", cause)
	}
	_ = s.Store.RecordLoginAttempt(ctx, "sso:"+st.ip, st.ip, false)

	isLink := st.row != nil && st.row.Purpose == "link"
	if !auditedAlready {
		s.auditACSFailure(ctx, st, reason, isLink)
	}

	switch {
	case st.row == nil || st.row.Purpose == "login":
		redirectTo(w, "/login?error="+saml.UserMessageFor(reason))
	case isLink:
		redirectTo(w, "/?sso_link="+url.QueryEscape(reason))
	case reason == saml.ReasonBindingMismatch:
		// Without the binding cookie nothing proves this browser started the test; store no result.
		redirectTo(w, "/login?error="+saml.UserMessageFor(reason))
	default: // test
		failure := ssoTestResult{Reason: reason, Message: testFailureMessage(reason), Attributes: map[string][]string{}}
		if st.identity != nil {
			failure.NameID, failure.NameIDFormat, failure.Attributes = st.identity.NameID, st.identity.NameIDFormat, st.identity.Attributes
		}
		if err := s.storeTestResult(ctx, *st.row, failure); err != nil {
			s.log().Error("saml: store test failure", "error", err)
			redirectTo(w, "/login?error="+saml.UserMessageFor(reason))
			return
		}
		redirectTo(w, "/settings/sso?test="+url.QueryEscape(st.row.ID))
	}
}

func (s *Server) auditACSFailure(ctx context.Context, st *acsState, reason string, isLink bool) {
	nameID := "-"
	if st.identity != nil && st.identity.NameID != "" {
		nameID = st.identity.NameID
	}
	details := map[string]any{"reason": reason}
	if st.row != nil {
		details["in_response_to"] = st.row.ID
	}
	entry := store.AuditEntry{IP: st.ip, Details: details}
	if isLink {
		entry.Action, entry.Summary = "sso_link_failed", fmt.Sprintf("SSO link failed (%s).", reason)
		if st.identity != nil {
			details["name_id"] = st.identity.NameID
		}
		if st.row.UserID != nil {
			if user, err := s.Store.GetUser(ctx, *st.row.UserID); err == nil {
				entry.ActorUserID, entry.ActorUsername = &user.ID, user.Username
			}
		}
	} else {
		entry.Action, entry.Summary = "sso_sign_in_failed", fmt.Sprintf("SSO sign-in failed (%s).", reason)
		entry.ActorUsername = "sso:" + nameID
	}
	_ = s.Store.Audit(ctx, entry)
}

func redirectTo(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

// withQuery adds key=value to the relative path.
func withQuery(path, key, value string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + key + "=" + url.QueryEscape(value)
}
