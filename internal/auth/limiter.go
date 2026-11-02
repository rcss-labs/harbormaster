package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// ErrBadCredentials is the only login failure callers see, whatever the real reason
// (unknown user, wrong password, locked account, throttled IP).
var ErrBadCredentials = errors.New("Wrong username or password.")

// dummyHash is verified when the username is unknown so response time does not reveal which usernames exist.
var dummyHash = func() string {
	h, err := HashPassword("harbormaster-dummy-password")
	if err != nil {
		panic(err)
	}
	return h
}()

// LoginResult is returned by a successful Login.
type LoginResult struct {
	User      store.User
	SessionID string // raw cookie value
	CSRFToken string
}

// Login authenticates username/password, applying per-IP and per-user throttling.
func (a *Service) Login(ctx context.Context, username, password, ip, userAgent string) (LoginResult, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	now := a.now()

	byUser, byIP, err := a.store.FailedAttempts(ctx, username, ip, now.Add(-a.cfg.LoginWindow))
	if err != nil {
		return LoginResult{}, err
	}
	if byIP >= a.cfg.LoginMaxPerIP {
		_, _ = a.verify(dummyHash, password)
		a.recordFailure(ctx, nil, username, ip)
		return LoginResult{}, ErrBadCredentials
	}

	user, err := a.store.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		_, _ = a.verify(dummyHash, password)
		a.recordFailure(ctx, nil, username, ip)
		return LoginResult{}, ErrBadCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}

	// Locked or over the failure limit: still pay for a verify so response time does not reveal the lock.
	if byUser >= a.cfg.LoginMaxPerUser || (user.LockedUntil != nil && user.LockedUntil.After(now)) {
		a.verifyPassword(user, password)
		a.recordFailure(ctx, &user, username, ip)
		return LoginResult{}, ErrBadCredentials
	}

	if !a.verifyPassword(user, password) {
		a.recordFailure(ctx, &user, username, ip)
		if byUser+1 >= a.cfg.LoginMaxPerUser {
			if err := a.store.LockUser(ctx, user.ID, now.Add(a.cfg.LoginWindow)); err != nil {
				return LoginResult{}, err
			}
		}
		return LoginResult{}, ErrBadCredentials
	}

	// Only after the password verified: refusing earlier would reveal break-glass status to anyone.
	ssoOn, err := a.store.SAMLEnabled(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if ssoOn && !user.BreakGlass {
		a.recordFailureReason(ctx, &user, username, ip, "sso_required")
		return LoginResult{}, ErrSSORequired
	}

	if err := a.store.RecordLoginAttempt(ctx, username, ip, true); err != nil {
		return LoginResult{}, err
	}
	if err := a.store.TouchSignIn(ctx, user.ID, now); err != nil {
		return LoginResult{}, err
	}
	sessionID, csrf, err := a.createSession(ctx, user.ID, ip, userAgent, "password", nil)
	if err != nil {
		return LoginResult{}, err
	}
	if ssoOn {
		a.auditDetails(ctx, &user, "sign_in", "Signed in with password (break-glass).", ip, map[string]any{"break_glass": true})
	} else {
		a.audit(ctx, &user, "sign_in", "Signed in.", ip)
	}
	return LoginResult{User: user, SessionID: sessionID, CSRFToken: csrf}, nil
}

// verifyPassword reports whether password matches. A user without a password never matches, whatever
// the verifier says; a dummy verify keeps the timing the same as for a wrong password.
func (a *Service) verifyPassword(user store.User, password string) bool {
	if !user.HasPassword() {
		_, _ = a.verify(dummyHash, password)
		return false
	}
	ok, err := a.verify(user.PasswordHash, password)
	return err == nil && ok
}

func (a *Service) recordFailure(ctx context.Context, user *store.User, username, ip string) {
	a.recordFailureReason(ctx, user, username, ip, "")
}

// recordFailureReason stores the failed attempt and audit entry; bookkeeping errors must not change the login outcome.
func (a *Service) recordFailureReason(ctx context.Context, user *store.User, username, ip, reason string) {
	_ = a.store.RecordLoginAttempt(ctx, username, ip, false)
	entry := store.AuditEntry{
		Action:        "sign_in_failed",
		Summary:       fmt.Sprintf("Failed sign-in for %s.", username),
		ActorUsername: username,
		IP:            ip,
	}
	if reason != "" {
		entry.Details = map[string]any{"reason": reason}
	}
	if user != nil {
		entry.ActorUserID = &user.ID
		entry.ActorUsername = user.Username
	}
	_ = a.store.Audit(ctx, entry)
}

func (a *Service) audit(ctx context.Context, user *store.User, action, summary, ip string) {
	a.auditDetails(ctx, user, action, summary, ip, nil)
}

func (a *Service) auditDetails(ctx context.Context, user *store.User, action, summary, ip string, details map[string]any) {
	entry := store.AuditEntry{Action: action, Summary: summary, IP: ip, Details: details}
	if user != nil {
		entry.ActorUserID = &user.ID
		entry.ActorUsername = user.Username
	}
	_ = a.store.Audit(ctx, entry)
}
