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
		_, _ = a.verify(user.PasswordHash, password)
		a.recordFailure(ctx, &user, username, ip)
		return LoginResult{}, ErrBadCredentials
	}

	ok, verr := a.verify(user.PasswordHash, password)
	if verr != nil || !ok {
		a.recordFailure(ctx, &user, username, ip)
		if byUser+1 >= a.cfg.LoginMaxPerUser {
			if err := a.store.LockUser(ctx, user.ID, now.Add(a.cfg.LoginWindow)); err != nil {
				return LoginResult{}, err
			}
		}
		return LoginResult{}, ErrBadCredentials
	}

	if err := a.store.RecordLoginAttempt(ctx, username, ip, true); err != nil {
		return LoginResult{}, err
	}
	if err := a.store.TouchSignIn(ctx, user.ID, now); err != nil {
		return LoginResult{}, err
	}
	sessionID, csrf, err := a.createSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return LoginResult{}, err
	}
	a.audit(ctx, &user, "sign_in", "Signed in.", ip)
	return LoginResult{User: user, SessionID: sessionID, CSRFToken: csrf}, nil
}

// recordFailure stores the failed attempt and audit entry; bookkeeping errors must not change the login outcome.
func (a *Service) recordFailure(ctx context.Context, user *store.User, username, ip string) {
	_ = a.store.RecordLoginAttempt(ctx, username, ip, false)
	entry := store.AuditEntry{
		Action:        "sign_in_failed",
		Summary:       fmt.Sprintf("Failed sign-in for %s.", username),
		ActorUsername: username,
		IP:            ip,
	}
	if user != nil {
		entry.ActorUserID = &user.ID
		entry.ActorUsername = user.Username
	}
	_ = a.store.Audit(ctx, entry)
}

func (a *Service) audit(ctx context.Context, user *store.User, action, summary, ip string) {
	entry := store.AuditEntry{Action: action, Summary: summary, IP: ip}
	if user != nil {
		entry.ActorUserID = &user.ID
		entry.ActorUsername = user.Username
	}
	_ = a.store.Audit(ctx, entry)
}
