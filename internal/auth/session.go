package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// CookieName is the session cookie.
const CookieName = "hm_session"

const touchInterval = time.Minute

// ErrUnauthenticated means the session is missing, expired or its user is gone. It wraps store.ErrNotFound.
var ErrUnauthenticated = fmt.Errorf("unauthenticated: %w", store.ErrNotFound)

// ErrWrongPassword is returned by ChangePassword when the current password does not match.
var ErrWrongPassword = errors.New("Current password is wrong.")

// Service owns sessions and sign-in.
type Service struct {
	store  *store.Store
	cfg    config.Config
	now    func() time.Time
	verify func(hash, pw string) (bool, error)
}

// NewService builds the auth service.
func NewService(st *store.Store, cfg config.Config) *Service {
	return &Service{store: st, cfg: cfg, now: time.Now, verify: VerifyPassword}
}

// hashSessionID maps the raw cookie value to the stored identifier:
// HMAC-SHA256 keyed by key when set, plain SHA-256 otherwise.
func hashSessionID(key, id string) string {
	if key == "" {
		sum := sha256.Sum256([]byte(id))
		return hex.EncodeToString(sum[:])
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(id))
	return hex.EncodeToString(mac.Sum(nil))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (a *Service) createSession(ctx context.Context, userID int64, ip, userAgent string) (sessionID, csrf string, err error) {
	if sessionID, err = randomToken(); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(); err != nil {
		return "", "", err
	}
	now := a.now()
	err = a.store.CreateSession(ctx, store.Session{
		IDHash:     hashSessionID(a.cfg.SessionKey, sessionID),
		UserID:     userID,
		CSRFToken:  csrf,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  minTime(now.Add(a.cfg.SessionIdle), now.Add(a.cfg.SessionAbsolute)),
		IP:         ip,
		UserAgent:  userAgent,
	})
	return sessionID, csrf, err
}

// StartSession signs user in without a password check (used right after first-run setup):
// it records the sign-in, creates a session and audits it.
func (a *Service) StartSession(ctx context.Context, user store.User, ip, userAgent string) (LoginResult, error) {
	if err := a.store.TouchSignIn(ctx, user.ID, a.now()); err != nil {
		return LoginResult{}, err
	}
	sessionID, csrf, err := a.createSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return LoginResult{}, err
	}
	a.audit(ctx, &user, "sign_in", "Signed in.", ip)
	return LoginResult{User: user, SessionID: sessionID, CSRFToken: csrf}, nil
}

// Logout ends the session identified by the raw cookie value.
func (a *Service) Logout(ctx context.Context, sessionID string) error {
	return a.store.DeleteSession(ctx, hashSessionID(a.cfg.SessionKey, sessionID))
}

// Authenticate resolves a raw session ID to its session and user, enforcing idle and absolute
// timeouts and refreshing last-seen at most once a minute.
func (a *Service) Authenticate(ctx context.Context, sessionID string) (store.Session, store.User, error) {
	if sessionID == "" {
		return store.Session{}, store.User{}, ErrUnauthenticated
	}
	sess, err := a.store.GetSession(ctx, hashSessionID(a.cfg.SessionKey, sessionID))
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.Session{}, store.User{}, err
	}
	now := a.now()
	if now.After(sess.LastSeenAt.Add(a.cfg.SessionIdle)) || now.After(sess.CreatedAt.Add(a.cfg.SessionAbsolute)) {
		_ = a.store.DeleteSession(ctx, sess.IDHash)
		return store.Session{}, store.User{}, ErrUnauthenticated
	}
	user, err := a.store.GetUser(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, store.User{}, ErrUnauthenticated
	}
	if err != nil {
		return store.Session{}, store.User{}, err
	}
	if now.Sub(sess.LastSeenAt) >= touchInterval {
		expires := minTime(now.Add(a.cfg.SessionIdle), sess.CreatedAt.Add(a.cfg.SessionAbsolute))
		if err := a.store.TouchSession(ctx, sess.IDHash, now, expires); err != nil {
			return store.Session{}, store.User{}, err
		}
		sess.LastSeenAt, sess.ExpiresAt = now, expires
	}
	return sess, user, nil
}

// ChangePassword verifies the current password, sets the new one and ends every session of the
// user except the one carried in ctx (see SessionFrom).
func (a *Service) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	user, err := a.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if ok, verr := VerifyPassword(user.PasswordHash, current); verr != nil || !ok {
		return ErrWrongPassword
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := a.store.SetPassword(ctx, userID, hash, false); err != nil {
		return err
	}
	if sess, ok := SessionFrom(ctx); ok && sess.UserID == userID {
		err = a.store.DeleteSessionsForUserExcept(ctx, userID, sess.IDHash)
	} else {
		err = a.store.DeleteSessionsForUser(ctx, userID)
	}
	if err != nil {
		return err
	}
	a.audit(ctx, &user, "password_change", "Changed password.", "")
	return nil
}

// AdminResetPassword sets a new temporary password that must be changed at next sign-in and ends all sessions.
func (a *Service) AdminResetPassword(ctx context.Context, userID int64) (string, error) {
	temp := GenerateTemporaryPassword()
	hash, err := HashPassword(temp)
	if err != nil {
		return "", err
	}
	if err := a.store.SetPassword(ctx, userID, hash, true); err != nil {
		return "", err
	}
	if err := a.store.DeleteSessionsForUser(ctx, userID); err != nil {
		return "", err
	}
	return temp, nil
}

// SetCookie writes the session cookie.
func (a *Service) SetCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.cfg.SessionAbsolute / time.Second),
	})
}

// ClearCookie expires the session cookie.
func (a *Service) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
