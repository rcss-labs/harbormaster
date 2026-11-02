package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	pendingLinkTTL      = 14 * 24 * time.Hour // spec D11, owner decision 8
	maxIdPCerts         = 5
	certExpiryWarning   = 30 * 24 * time.Hour
	maxNameIDLength     = 256
	maxButtonLabelRunes = 80
	nameIDMessage       = "Enter a NameID of 1 to 256 characters."
	sessionKeyMessage   = "Set HM_SESSION_KEY before configuring single sign-on."
	metadataEitherOr    = "Send either a metadata URL or metadata XML."
)

// samlConfigFromRow adapts the stored row to the saml package's Config.
func samlConfigFromRow(row store.SAMLConfigRow) (saml.Config, error) {
	cfg := saml.Config{
		Enabled: row.Enabled, IdPEntityID: row.IdPEntityID, IdPSSORedirectURL: row.IdPSSORedirectURL,
		IdPSSOPostURL: row.IdPSSOPostURL, SPEntityID: row.SPEntityID, NameIDFormat: row.NameIDFormat,
		AttrEmail: deref(row.AttrEmail), AttrDisplayName: deref(row.AttrDisplayName), AttrUsername: deref(row.AttrUsername),
		ClockSkew: time.Duration(row.ClockSkewSeconds) * time.Second, ButtonLabel: row.ButtonLabel,
	}
	for _, certPEM := range row.IdPCertsPEM {
		block, _ := pem.Decode([]byte(certPEM))
		if block == nil {
			return saml.Config{}, errors.New("stored IdP certificate is not valid PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return saml.Config{}, errors.New("stored IdP certificate cannot be parsed")
		}
		cfg.IdPCerts = append(cfg.IdPCerts, cert)
	}
	return cfg, nil
}

func certPEM(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

func parseCertPEM(p string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func certDTO(c *x509.Certificate) CertDTO {
	return CertDTO{Fingerprint: saml.Fingerprint(c), Subject: c.Subject.String(), NotAfter: c.NotAfter.UTC()}
}

// certFingerprints lists the fingerprints of the PEM certificates (unparseable entries are skipped).
func certFingerprints(pems []string) []string {
	out := []string{}
	for _, p := range pems {
		if c, err := parseCertPEM(p); err == nil {
			out = append(out, saml.Fingerprint(c))
		}
	}
	return out
}

// validateIdPCerts parses and normalises admin-supplied PEM certificates.
func validateIdPCerts(pems []string, now time.Time) ([]string, error) {
	if len(pems) == 0 {
		return nil, invalid("Add at least one identity provider certificate.")
	}
	if len(pems) > maxIdPCerts {
		return nil, invalid(fmt.Sprintf("Use at most %d identity provider certificates.", maxIdPCerts))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(pems))
	for i, p := range pems {
		c, err := parseCertPEM(p)
		if err != nil {
			return nil, invalid(fmt.Sprintf("Certificate %d is not a valid X.509 certificate.", i+1))
		}
		if now.After(c.NotAfter) {
			return nil, invalid(fmt.Sprintf("Certificate %d expired on %s.", i+1, c.NotAfter.UTC().Format("2006-01-02")))
		}
		if now.Before(c.NotBefore) {
			return nil, invalid(fmt.Sprintf("Certificate %d is not valid until %s.", i+1, c.NotBefore.UTC().Format("2006-01-02")))
		}
		if fp := saml.Fingerprint(c); !seen[fp] {
			seen[fp] = true
			out = append(out, certPEM(c))
		}
	}
	return out, nil
}

// defaultSAMLRow is the starting point for a first save.
func (s *Server) defaultSAMLRow() store.SAMLConfigRow {
	return store.SAMLConfigRow{
		SPEntityID: saml.SPEntityID(s.Cfg.BaseURL), NameIDFormat: "unspecified",
		ClockSkewSeconds: 120, ButtonLabel: "Sign in with SSO", IdPCertsPEM: []string{},
	}
}

// buildSSOConfigDTO assembles SSOConfigDTO; unconfigured installs get the SP values and defaults.
func (s *Server) buildSSOConfigDTO(ctx context.Context) (SSOConfigDTO, error) {
	dto := SSOConfigDTO{
		SPEntityID: saml.SPEntityID(s.Cfg.BaseURL), ACSURL: saml.ACSURL(s.Cfg.BaseURL), MetadataPath: "/api/auth/saml/metadata",
		IdPCerts: []CertDTO{}, Warnings: []string{}, SPKeyState: "missing",
		NameIDFormat: "unspecified", ClockSkewSeconds: 120, ButtonLabel: "Sign in with SSO",
	}
	now := s.Now()
	row, err := s.Store.GetSAMLConfig(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return SSOConfigDTO{}, err
	default:
		dto.Configured, dto.Enabled = true, row.Enabled
		dto.IdPEntityID, dto.IdPSSORedirectURL = row.IdPEntityID, row.IdPSSORedirectURL
		dto.MetadataURL, dto.MetadataFetchedAt = row.MetadataURL, row.MetadataFetchedAt
		dto.SPEntityID = row.SPEntityID
		dto.NameIDFormat, dto.ButtonLabel, dto.ClockSkewSeconds = row.NameIDFormat, row.ButtonLabel, row.ClockSkewSeconds
		dto.AttrEmail, dto.AttrDisplayName, dto.AttrUsername = row.AttrEmail, row.AttrDisplayName, row.AttrUsername
		for _, p := range row.IdPCertsPEM {
			c, err := parseCertPEM(p)
			if err != nil {
				dto.Warnings = append(dto.Warnings, "A stored identity provider certificate cannot be read.")
				continue
			}
			dto.IdPCerts = append(dto.IdPCerts, certDTO(c))
			dto.Warnings = appendExpiryWarning(dto.Warnings, "Identity provider certificate", c, now)
		}
	}

	keys, err := s.samlKeys(ctx)
	switch {
	case errors.Is(err, saml.ErrUndecryptable):
		dto.SPKeyState = "undecryptable"
		dto.Warnings = append(dto.Warnings, "The service provider key cannot be decrypted. Check HM_SESSION_KEY or generate a new certificate.")
	case err != nil:
		return SSOConfigDTO{}, err
	case len(keys) > 0 && keys[0].RetiredAt == nil:
		dto.SPKeyState = "ok"
		cert := certDTO(keys[0].Cert)
		dto.SPCert = &cert
		dto.Warnings = appendExpiryWarning(dto.Warnings, "Service provider certificate", keys[0].Cert, now)
	case dto.Configured:
		dto.Warnings = append(dto.Warnings, "There is no service provider certificate. Generate one.")
	}

	if dto.BreakGlassAdmins, err = s.Store.CountBreakGlassAdmins(ctx); err != nil {
		return SSOConfigDTO{}, err
	}
	if dto.PasswordUsersBlockedOnEnable, err = s.Store.CountPasswordUsersNotBreakGlass(ctx); err != nil {
		return SSOConfigDTO{}, err
	}
	return dto, nil
}

func appendExpiryWarning(warnings []string, what string, c *x509.Certificate, now time.Time) []string {
	switch left := c.NotAfter.Sub(now); {
	case left <= 0:
		return append(warnings, fmt.Sprintf("%s %s expired on %s.", what, c.Subject.CommonName, c.NotAfter.UTC().Format("2006-01-02")))
	case left < certExpiryWarning:
		return append(warnings, fmt.Sprintf("%s %s expires in %d days.", what, c.Subject.CommonName, int(left.Hours()/24)))
	}
	return warnings
}

func (s *Server) writeSSOConfig(w http.ResponseWriter, r *http.Request) {
	dto, err := s.buildSSOConfigDTO(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) getSSO(w http.ResponseWriter, r *http.Request) { s.writeSSOConfig(w, r) }

// ensureSPKey generates the SP key pair on the first save. It needs HM_SESSION_KEY to encrypt the key.
func (s *Server) ensureSPKey(r *http.Request) error {
	if s.SAML.cipher == nil {
		return invalid(sessionKeyMessage)
	}
	existing, err := s.Store.ListSPKeys(r.Context())
	if err != nil || len(existing) > 0 {
		return err
	}
	_, err = s.newSPKey(r)
	return err
}

// newSPKey generates, encrypts and stores a fresh SP key pair (retiring the active one) and returns its certificate.
func (s *Server) newSPKey(r *http.Request) (*x509.Certificate, error) {
	if s.SAML.cipher == nil {
		return nil, invalid(sessionKeyMessage)
	}
	host := "harbormaster"
	if u, err := url.Parse(s.Cfg.BaseURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	certPEMText, keyPEM, err := saml.GenerateSPKey(host, s.Now())
	if err != nil {
		return nil, err
	}
	sealed, err := s.SAML.cipher.Seal(keyPEM)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.InsertSPKey(r.Context(), certPEMText, sealed); err != nil {
		return nil, err
	}
	return parseCertPEM(certPEMText)
}

type ssoPutBody struct {
	IdPEntityID       string `json:"idp_entity_id"`
	IdPSSORedirectURL string `json:"idp_sso_redirect_url"`
	// IdPCerts replaces the stored certificates when present; omit it to keep them (the PEM is never sent back to the browser).
	IdPCerts         []string `json:"idp_certs"`
	NameIDFormat     string   `json:"name_id_format"`
	AttrEmail        *string  `json:"attr_email"`
	AttrDisplayName  *string  `json:"attr_display_name"`
	AttrUsername     *string  `json:"attr_username"`
	ClockSkewSeconds int      `json:"clock_skew_seconds"`
	ButtonLabel      string   `json:"button_label"`
}

func blankToNil(p *string) *string {
	if p == nil {
		return nil
	}
	if v := strings.TrimSpace(*p); v != "" {
		return &v
	}
	return nil
}

func equalStrPtr(a, b *string) bool { return deref(a) == deref(b) && (a == nil) == (b == nil) }

func (s *Server) validRedirectURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && s.Cfg.FakeAWS)
}

func (s *Server) putSSO(w http.ResponseWriter, r *http.Request) {
	var body ssoPutBody
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if s.SAML.cipher == nil {
		s.fail(w, invalid(sessionKeyMessage))
		return
	}
	old, err := s.Store.GetSAMLConfig(r.Context())
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	row := s.defaultSAMLRow()
	if exists {
		row = old
	}
	row.IdPEntityID = strings.TrimSpace(body.IdPEntityID)
	row.IdPSSORedirectURL = strings.TrimSpace(body.IdPSSORedirectURL)
	if row.IdPEntityID == "" || len(row.IdPEntityID) > 1024 {
		s.fail(w, invalid("Enter the identity provider entity ID."))
		return
	}
	if !s.validRedirectURL(row.IdPSSORedirectURL) {
		s.fail(w, invalid("The sign-in URL must be an https URL."))
		return
	}
	if body.IdPCerts != nil || !exists {
		if row.IdPCertsPEM, err = validateIdPCerts(body.IdPCerts, s.Now()); err != nil {
			s.fail(w, err)
			return
		}
	}
	if body.NameIDFormat != "" {
		if !slices.Contains([]string{"unspecified", "email", "persistent"}, body.NameIDFormat) {
			s.fail(w, invalid("Choose unspecified, email or persistent as the NameID format."))
			return
		}
		row.NameIDFormat = body.NameIDFormat
	}
	if body.ClockSkewSeconds < 0 || body.ClockSkewSeconds > 300 {
		s.fail(w, invalid("Clock skew must be between 0 and 300 seconds."))
		return
	}
	row.ClockSkewSeconds = body.ClockSkewSeconds
	if label := strings.TrimSpace(body.ButtonLabel); label != "" {
		if len([]rune(label)) > maxButtonLabelRunes {
			s.fail(w, invalid(fmt.Sprintf("The button label can have at most %d characters.", maxButtonLabelRunes)))
			return
		}
		row.ButtonLabel = label
	}
	row.AttrEmail, row.AttrDisplayName, row.AttrUsername = blankToNil(body.AttrEmail), blankToNil(body.AttrDisplayName), blankToNil(body.AttrUsername)
	actor, _ := auth.UserFrom(r.Context())
	row.UpdatedByUserID, row.UpdatedAt = &actor.ID, s.Now()

	if err := s.ensureSPKey(r); err != nil {
		s.fail(w, err)
		return
	}
	change, err := s.Store.UpsertSAMLConfig(r.Context(), row)
	if err != nil {
		s.fail(w, err)
		return
	}
	if fields := changedSSOFields(old, row, exists); len(fields) > 0 {
		s.auditAdmin(r, "sso_config_update", "sso", "Changed single sign-on settings: "+strings.Join(fields, ", ")+".", map[string]any{
			"fields": fields, "idp_entity_id_changed": exists && old.IdPEntityID != row.IdPEntityID,
			"sessions_ended": change.SessionsEnded, "identities_detached": change.IdentitiesDetached,
		})
	}
	s.writeSSOConfig(w, r)
}

// changedSSOFields names the settings that differ; certificates are compared by fingerprint.
func changedSSOFields(old, updated store.SAMLConfigRow, exists bool) []string {
	fields := []string{}
	add := func(changed bool, name string) {
		if changed || !exists {
			fields = append(fields, name)
		}
	}
	add(old.IdPEntityID != updated.IdPEntityID, "idp_entity_id")
	add(old.IdPSSORedirectURL != updated.IdPSSORedirectURL, "idp_sso_redirect_url")
	add(!slices.Equal(certFingerprints(old.IdPCertsPEM), certFingerprints(updated.IdPCertsPEM)), "idp_certs")
	add(old.NameIDFormat != updated.NameIDFormat, "name_id_format")
	add(!equalStrPtr(old.AttrEmail, updated.AttrEmail), "attr_email")
	add(!equalStrPtr(old.AttrDisplayName, updated.AttrDisplayName), "attr_display_name")
	add(!equalStrPtr(old.AttrUsername, updated.AttrUsername), "attr_username")
	add(old.ClockSkewSeconds != updated.ClockSkewSeconds, "clock_skew_seconds")
	add(old.ButtonLabel != updated.ButtonLabel, "button_label")
	return fields
}

func (s *Server) importSSOMetadata(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL *string `json:"url"`
		XML *string `json:"xml"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if (body.URL == nil) == (body.XML == nil) {
		s.fail(w, invalid(metadataEitherOr))
		return
	}
	if s.SAML.cipher == nil {
		s.fail(w, invalid(sessionKeyMessage))
		return
	}
	var xmlBytes []byte
	var sourceURL *string
	source := "file"
	if body.URL != nil {
		fetched, err := s.SAML.fetcher.Fetch(r.Context(), *body.URL)
		if err != nil {
			s.fail(w, invalid(err.Error()))
			return
		}
		xmlBytes, source = fetched, "url"
		trimmed := strings.TrimSpace(*body.URL)
		sourceURL = &trimmed
	} else {
		xmlBytes = []byte(*body.XML)
	}
	now := s.Now()
	md, err := saml.ParseIdPMetadata(xmlBytes, now)
	if err != nil {
		s.fail(w, invalid(err.Error()))
		return
	}
	old, err := s.Store.GetSAMLConfig(r.Context())
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	row := s.defaultSAMLRow()
	if exists {
		row = old
	}
	row.IdPEntityID, row.IdPSSORedirectURL, row.IdPSSOPostURL = md.EntityID, md.SSORedirectURL, md.SSOPostURL
	row.IdPCertsPEM = row.IdPCertsPEM[:0:0]
	for _, c := range md.SigningCerts {
		row.IdPCertsPEM = append(row.IdPCertsPEM, certPEM(c))
	}
	xmlText := string(xmlBytes)
	row.MetadataURL, row.MetadataXML, row.MetadataFetchedAt = sourceURL, &xmlText, &now
	actor, _ := auth.UserFrom(r.Context())
	row.UpdatedByUserID, row.UpdatedAt = &actor.ID, now

	if err := s.ensureSPKey(r); err != nil {
		s.fail(w, err)
		return
	}
	change, err := s.Store.UpsertSAMLConfig(r.Context(), row)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "sso_metadata_import", "sso", "Imported IdP metadata from "+source+".", map[string]any{
		"idp_entity_id": md.EntityID, "cert_fingerprints": certFingerprints(row.IdPCertsPEM),
		"sessions_ended": change.SessionsEnded, "identities_detached": change.IdentitiesDetached,
	})
	s.writeSSOConfig(w, r)
}

func (s *Server) setSSOEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil { // ErrNotFound answers 404: nothing to enable
		s.fail(w, err)
		return
	}
	change, err := s.Store.SetSAMLEnabled(r.Context(), body.Enabled)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch {
	case before.Enabled == body.Enabled:
	case body.Enabled:
		s.auditAdmin(r, "sso_enabled", "sso", "Turned single sign-on on.", nil)
	default:
		s.auditAdmin(r, "sso_disabled", "sso", "Turned single sign-on off.", map[string]any{"sessions_ended": change.SessionsEnded})
	}
	s.writeSSOConfig(w, r)
}

func (s *Server) rotateSSOKey(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.GetSAMLConfig(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	cert, err := s.newSPKey(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "sso_key_rotated", "sso", "Generated a new SP certificate.", map[string]any{"fingerprint": saml.Fingerprint(cert)})
	s.writeSSOConfig(w, r)
}

// takeSSOTest hands the admin the one-time result of their Test sign-in. The ACS cannot attribute the
// test (it never reads the session), so the audit entry is written here with the admin as actor.
func (s *Server) takeSSOTest(w http.ResponseWriter, r *http.Request) {
	raw, err := s.Store.TakeSAMLTestResult(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	result := SSOTestResultDTO{Attributes: map[string][]string{}}
	if err := json.Unmarshal(raw, &result); err != nil {
		s.fail(w, fmt.Errorf("decode stored test result: %w", err))
		return
	}
	if result.Attributes == nil {
		result.Attributes = map[string][]string{}
	}
	outcome := "ok"
	details := map[string]any{"reason": result.Reason}
	if !result.OK {
		outcome = result.Reason
	}
	switch result.Outcome {
	case "would_create":
		details["would_create"] = true
	case "would_sign_in", "would_link":
		details["would_sign_in"] = true
	}
	s.auditAdmin(r, "sso_test", "sso", "Ran an SSO sign-in test ("+outcome+").", details)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) setBreakGlass(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body struct {
		BreakGlass bool `json:"break_glass"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	changed, err := s.Store.SetBreakGlassChecked(r.Context(), id, body.BreakGlass)
	if errors.Is(err, store.ErrNeedsPassword) {
		s.fail(w, invalid("Set a password for "+target.Username+" first."))
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if changed {
		if body.BreakGlass {
			s.auditAdmin(r, "break_glass_grant", "user:"+target.Username, "Made "+target.Username+" a break-glass account.", nil)
		} else {
			s.auditAdmin(r, "break_glass_revoke", "user:"+target.Username, "Removed break-glass from "+target.Username+".", nil)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setPendingLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body struct {
		NameID string `json:"name_id"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	nameID := strings.TrimSpace(body.NameID)
	if nameID == "" || len([]rune(nameID)) > maxNameIDLength {
		s.fail(w, invalid(nameIDMessage))
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	expires := s.Now().Add(pendingLinkTTL)
	if err := s.Store.SetPendingLink(r.Context(), id, nameID, actor.ID, expires); err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "sso_link_pending_set", "user:"+target.Username,
		"Set a pending SSO link for "+target.Username+" to "+nameID+".",
		map[string]any{"name_id": nameID, "expires_at": expires.UTC().Format(time.RFC3339)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearPendingLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.ClearPendingLink(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "sso_link_pending_cleared", "user:"+target.Username,
		"Cleared the pending SSO link of "+target.Username+".", map[string]any{"name_id": target.PendingLinkNameID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unlinkSSO(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The temporary password is only stored when the user has none (users_has_credential); passwords are never cleared.
	temp := auth.GenerateTemporaryPassword()
	hash, err := auth.HashPassword(temp)
	if err != nil {
		s.fail(w, err)
		return
	}
	passwordSet, sessionsEnded, err := s.Store.UnlinkSSO(r.Context(), id, hash)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "sso_unlinked", "user:"+target.Username,
		"Unlinked "+target.Username+" from SSO identity "+target.ExternalID+".",
		map[string]any{"name_id": target.ExternalID, "password_set": passwordSet, "sessions_ended": sessionsEnded})
	if passwordSet {
		writeJSON(w, http.StatusOK, map[string]string{"temporary_password": temp})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{})
}
