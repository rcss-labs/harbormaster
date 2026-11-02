package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ErrSSORequired is returned by Login when SSO is on and the user is not break-glass.
var ErrSSORequired = errors.New("Use single sign-on to sign in to this account.")

// ErrNoPassword is returned by ChangePassword and StartLink for a user without a password.
var ErrNoPassword = errors.New("This account has no password.")

// Reason codes owned by the auth layer (they extend the saml.Reason* set; ReasonOf reads them).
const (
	ReasonSSOIdentityTaken    = "sso_identity_taken"
	ReasonLinkUserGone        = "link_user_gone"
	ReasonLinkUserHasIdentity = "link_user_has_identity"
	ReasonUserLocked          = "user_locked"
)

// SSOOutcome says how LoginSSO resolved the identity.
type SSOOutcome string

const (
	SSOSignIn      SSOOutcome = "sign_in"      // identity already linked
	SSOLinkPending SSOOutcome = "link_pending" // admin pending link consumed
	SSOCreate      SSOOutcome = "create"       // JIT user created
)

// Outcomes of PreviewSSO.
const (
	PreviewWouldCreate = "would_create"
	PreviewWouldSignIn = "would_sign_in"
	PreviewWouldLink   = "would_link"
)

// SSOPreview is what a sign-in with an identity would do, without doing it.
type SSOPreview struct {
	Username     string
	Outcome      string // would_create | would_sign_in | would_link
	ExistingUser *string
}

// maxSSOAttempts bounds the retry when two requests race to create or bind the same identity.
const maxSSOAttempts = 3

func ssoFailure(reason string, format string, args ...any) error {
	return &saml.Error{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// PreviewSSO resolves id exactly as LoginSSO would (linked, active pending link, JIT) but writes nothing.
func (a *Service) PreviewSSO(ctx context.Context, id saml.Identity, cfg saml.Config) (SSOPreview, error) {
	user, err := a.store.GetUserByExternalID(ctx, cfg.IdPEntityID, id.NameID)
	if err == nil {
		return SSOPreview{Username: user.Username, Outcome: PreviewWouldSignIn, ExistingUser: &user.Username}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return SSOPreview{}, err
	}
	user, err = a.store.GetUserByPendingLink(ctx, cfg.IdPEntityID, id.NameID, a.now())
	if err == nil {
		return SSOPreview{Username: user.Username, Outcome: PreviewWouldLink, ExistingUser: &user.Username}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return SSOPreview{}, err
	}
	username, err := a.deriveUsername(ctx, id, cfg)
	if err != nil {
		return SSOPreview{}, err
	}
	return SSOPreview{Username: username, Outcome: PreviewWouldCreate}, nil
}

func (a *Service) deriveUsername(ctx context.Context, id saml.Identity, cfg saml.Config) (string, error) {
	var lookupErr error
	username, err := saml.DeriveUsername(id, cfg, func(candidate string) bool {
		_, err := a.store.GetUserByUsername(ctx, candidate)
		if err == nil {
			return true
		}
		if !errors.Is(err, store.ErrNotFound) {
			lookupErr = err
			return true // stop probing; the lookup error is returned below
		}
		return false
	})
	if lookupErr != nil {
		return "", lookupErr
	}
	return username, err
}

// LoginSSO signs in the user a verified identity resolves to, in spec D6 order: linked identity,
// active pending link, then a new password-less user. It never matches an existing user by username
// or email. Replay protection and the "SSO is on" check belong to the caller (the ACS).
func (a *Service) LoginSSO(ctx context.Context, id saml.Identity, cfg saml.Config, ip, userAgent string) (LoginResult, SSOOutcome, error) {
	if id.NameID == "" {
		return LoginResult{}, "", ssoFailure(saml.ReasonNoNameID, "empty NameID")
	}
	if id.SessionNotOnOrAfter != nil && !a.now().Before(*id.SessionNotOnOrAfter) {
		return LoginResult{}, "", ssoFailure(saml.ReasonExpired, "SessionNotOnOrAfter already passed")
	}
	for range maxSSOAttempts {
		res, outcome, retry, err := a.loginSSOOnce(ctx, id, cfg, ip, userAgent)
		if !retry {
			return res, outcome, err
		}
	}
	return LoginResult{}, "", ssoFailure(saml.ReasonUserCreateFailed, "concurrent sign-ins did not settle")
}

// loginSSOOnce reports retry=true when it lost a race that a second look-up resolves.
func (a *Service) loginSSOOnce(ctx context.Context, id saml.Identity, cfg saml.Config, ip, userAgent string) (LoginResult, SSOOutcome, bool, error) {
	now := a.now()
	email, displayName := id.Email(cfg), id.DisplayName(cfg)

	user, err := a.store.GetUserByExternalID(ctx, cfg.IdPEntityID, id.NameID)
	switch {
	case err == nil:
		if err := refuseLocked(user, now); err != nil {
			return LoginResult{}, "", false, err
		}
		if err := a.store.UpdateSAMLProfile(ctx, user.ID, email, displayName); err != nil {
			return LoginResult{}, "", errors.Is(err, store.ErrNotFound), err
		}
		res, err := a.startSSOSession(ctx, user, id, ip, userAgent, false)
		return res, SSOSignIn, false, err
	case !errors.Is(err, store.ErrNotFound):
		return LoginResult{}, "", false, err
	}

	pending, err := a.store.GetUserByPendingLink(ctx, cfg.IdPEntityID, id.NameID, now)
	switch {
	case err == nil:
		if err := refuseLocked(pending, now); err != nil {
			return LoginResult{}, "", false, err
		}
		bound, err := a.store.BindSSOIdentity(ctx, store.BindSSO{
			UserID: pending.ID, IdPEntityID: cfg.IdPEntityID, ExternalID: id.NameID,
			ExternalIDFormat: id.NameIDFormat, Email: email, DisplayName: displayName,
		})
		if errors.Is(err, store.ErrAlreadyLinked) || errors.Is(err, store.ErrSSOIdentityTaken) || errors.Is(err, store.ErrNotFound) {
			return LoginResult{}, "", true, nil
		}
		if err != nil {
			return LoginResult{}, "", false, err
		}
		details := map[string]any{"method": "pending", "has_password": bound.HasPassword()}
		if pending.PendingLinkSetByUserID != nil {
			details["pending_set_by_user_id"] = *pending.PendingLinkSetByUserID
		}
		a.auditDetails(ctx, &bound, "sso_linked", fmt.Sprintf("Linked %s to SSO identity %s.", bound.Username, id.NameID), ip, details)
		res, err := a.startSSOSession(ctx, bound, id, ip, userAgent, false)
		return res, SSOLinkPending, false, err
	case !errors.Is(err, store.ErrNotFound):
		return LoginResult{}, "", false, err
	}

	username, err := a.deriveUsername(ctx, id, cfg)
	if err != nil {
		return LoginResult{}, "", false, err
	}
	created, err := a.store.CreateSAMLUser(ctx, store.NewSAMLUser{
		Username: username, IdPEntityID: cfg.IdPEntityID, ExternalID: id.NameID,
		ExternalIDFormat: id.NameIDFormat, Email: email, DisplayName: displayName,
	})
	if errors.Is(err, store.ErrConflict) {
		return LoginResult{}, "", true, nil // a concurrent sign-in created the user or took the username
	}
	if err != nil {
		return LoginResult{}, "", false, err
	}
	a.auditDetails(ctx, &created, "sso_user_created", fmt.Sprintf("Created user %s from SSO.", created.Username), ip,
		map[string]any{"email": email, "name_id_format": id.NameIDFormat})
	res, err := a.startSSOSession(ctx, created, id, ip, userAgent, true)
	return res, SSOCreate, false, err
}

func refuseLocked(user store.User, now time.Time) error {
	if user.LockedUntil != nil && user.LockedUntil.After(now) {
		return ssoFailure(ReasonUserLocked, "user is locked")
	}
	return nil
}

func (a *Service) startSSOSession(ctx context.Context, user store.User, id saml.Identity, ip, userAgent string, jit bool) (LoginResult, error) {
	if err := a.store.TouchSignIn(ctx, user.ID, a.now()); err != nil {
		return LoginResult{}, err
	}
	sessionID, csrf, err := a.createSession(ctx, user.ID, ip, userAgent, "saml", id.SessionNotOnOrAfter)
	if err != nil {
		return LoginResult{}, err
	}
	a.auditDetails(ctx, &user, "sso_sign_in", "Signed in with SSO.", ip,
		map[string]any{"name_id_format": id.NameIDFormat, "jit": jit})
	return LoginResult{User: user, SessionID: sessionID, CSRFToken: csrf}, nil
}

// StartLink checks that userID may start a self-link: it has a password (re-entered here as
// re-authentication) and no SSO identity yet. The password check shares Login's per-user failure
// limit and lockout: a wrong password is recorded and audited like a failed password sign-in, and a
// locked user is refused even with the right password. ip is the caller's address for that bookkeeping.
func (a *Service) StartLink(ctx context.Context, userID int64, password, ip string) (store.User, error) {
	user, err := a.store.GetUser(ctx, userID)
	if err != nil {
		return store.User{}, err
	}
	if !user.HasPassword() {
		return store.User{}, ErrNoPassword
	}
	now := a.now()
	byUser, _, err := a.store.FailedAttempts(ctx, user.Username, ip, now.Add(-a.cfg.LoginWindow))
	if err != nil {
		return store.User{}, err
	}
	locked := byUser >= a.cfg.LoginMaxPerUser || (user.LockedUntil != nil && user.LockedUntil.After(now))
	ok := a.verifyPassword(user, password)
	if locked || !ok {
		a.recordFailureReason(ctx, &user, user.Username, ip, "link_start")
		if !locked && byUser+1 >= a.cfg.LoginMaxPerUser {
			if err := a.store.LockUser(ctx, user.ID, now.Add(a.cfg.LoginWindow)); err != nil {
				return store.User{}, err
			}
		}
		return store.User{}, ErrWrongPassword
	}
	if user.SSOLinked() {
		return store.User{}, store.ErrAlreadyLinked
	}
	return user, nil
}

// LinkSSO binds id to userID, the user stored on the link request row (never the caller's cookie).
// Passwords are untouched. Failures are *saml.Error with ReasonSSOIdentityTaken, ReasonLinkUserGone or
// ReasonLinkUserHasIdentity and are audited as sso_link_failed.
func (a *Service) LinkSSO(ctx context.Context, userID int64, id saml.Identity, cfg saml.Config, ip string) (store.User, error) {
	user, err := a.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, a.linkFailed(ctx, nil, ReasonLinkUserGone, id, ip)
	}
	if err != nil {
		return store.User{}, err
	}
	if user.SSOLinked() {
		return store.User{}, a.linkFailed(ctx, &user, ReasonLinkUserHasIdentity, id, ip)
	}
	// BindSSOIdentity refuses a NameID that is another user's active pending link, inside its transaction.
	bound, err := a.store.BindSSOIdentity(ctx, store.BindSSO{
		UserID: userID, IdPEntityID: cfg.IdPEntityID, ExternalID: id.NameID,
		ExternalIDFormat: id.NameIDFormat, Email: id.Email(cfg), DisplayName: id.DisplayName(cfg),
	})
	switch {
	case errors.Is(err, store.ErrSSOIdentityTaken):
		return store.User{}, a.linkFailed(ctx, &user, ReasonSSOIdentityTaken, id, ip)
	case errors.Is(err, store.ErrAlreadyLinked):
		return store.User{}, a.linkFailed(ctx, &user, ReasonLinkUserHasIdentity, id, ip)
	case errors.Is(err, store.ErrNotFound):
		return store.User{}, a.linkFailed(ctx, nil, ReasonLinkUserGone, id, ip)
	case err != nil:
		return store.User{}, err
	}
	a.auditDetails(ctx, &bound, "sso_linked", fmt.Sprintf("Linked %s to SSO identity %s.", bound.Username, id.NameID), ip,
		map[string]any{"method": "self", "has_password": bound.HasPassword()})
	return bound, nil
}

func (a *Service) linkFailed(ctx context.Context, user *store.User, reason string, id saml.Identity, ip string) error {
	a.auditDetails(ctx, user, "sso_link_failed", fmt.Sprintf("SSO link failed (%s).", reason), ip,
		map[string]any{"reason": reason, "name_id": id.NameID})
	return ssoFailure(reason, "link refused")
}
