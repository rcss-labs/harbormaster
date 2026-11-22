package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type (
	sessionKey struct{}
	userKey    struct{}
	subjectKey struct{}
)

// SubjectFrom returns the authenticated subject, or the zero (no-permission) Subject.
func SubjectFrom(ctx context.Context) authz.Subject {
	s, _ := ctx.Value(subjectKey{}).(authz.Subject)
	return s
}

// SessionFrom returns the authenticated session, if any.
func SessionFrom(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(store.Session)
	return s, ok
}

// UserFrom returns the authenticated user, if any.
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userKey{}).(store.User)
	return u, ok
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// failInternal logs a store failure met while authenticating and answers the standard 500, so an
// outage is not mistaken for a signed-out user.
func failInternal(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.Error("auth middleware: "+what, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on our side.")
}

// Middleware attaches the credential, user and subject to the request context.
//
// With HM_API_TOKENS on, a request whose Authorization header is "Bearer " followed by a well-formed token takes the
// token path: the cookie is not read, and a token that does not resolve is answered 401 here, on every route, never
// continuing as anonymous or as a session. A resolved token yields its user, a read-only subject and a token
// credential, and no session. With tokens off, or any other Authorization header, the header is ignored.
//
// Otherwise the session cookie is used: a valid one attaches the session, user, subject and a session credential;
// a missing or invalid cookie continues unauthenticated, and RequireAuth decides whether that is allowed.
func (a *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.APITokens {
			if raw, ok := bearerToken(r); ok {
				a.serveToken(w, r, next, raw)
				return
			}
		}
		cookie, err := r.Cookie(CookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, user, err := a.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, store.ErrNotFound) {
			next.ServeHTTP(w, r) // missing or expired session: anonymous, RequireAuth answers 401
			return
		}
		if err != nil {
			failInternal(w, r, "authenticate session", err)
			return
		}
		subject, err := a.store.LoadSubject(r.Context(), user.ID)
		if errors.Is(err, store.ErrNotFound) {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			failInternal(w, r, "load subject", err)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		ctx = context.WithValue(ctx, userKey{}, user)
		ctx = context.WithValue(ctx, subjectKey{}, subject)
		ctx = context.WithValue(ctx, credentialKey{}, Credential{Kind: CredentialSession})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuth rejects unauthenticated requests with 401. A session or an access token credential passes.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CredentialFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF requires the session's CSRF token on every method except GET, HEAD and OPTIONS. A request
// authenticated by an access token needs none: no cookie took part, so there is nothing to forge.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if IsToken(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := SessionFrom(r.Context())
		got := r.Header.Get(CSRFHeader)
		if !ok || got == "" || sess.CSRFToken == "" ||
			subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "csrf", "Missing or invalid CSRF token.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin allows only global admins.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !SubjectFrom(r.Context()).Admin {
			writeError(w, http.StatusForbidden, "forbidden", "Only global admins can do this.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePasswordChanged blocks users with a pending forced password change, except on the
// endpoints needed to change it or sign out.
func RequirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me", "/api/auth/password", "/api/auth/logout":
			next.ServeHTTP(w, r)
			return
		}
		if user, ok := UserFrom(r.Context()); ok && user.MustChangePassword {
			writeError(w, http.StatusForbidden, "password_change_required", "Change your password to continue.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireTokenScope is the token route gate (layer 1 of the read-only design), default deny. A session credential
// passes. A token credential passes only when the route is marked (route is not empty) and the token's scope equals
// the mark; any other token is refused with 403 token_not_allowed, on every unmarked route whatever its method.
// An anonymous request passes through to RequireAuth's decision.
func RequireTokenScope(route Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cred, ok := CredentialFrom(r.Context())
			if ok && cred.Kind != CredentialSession && (route == "" || cred.Scope != route) {
				writeError(w, http.StatusForbidden, "token_not_allowed", "This route is not available to access tokens.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RefuseToken is the gate for routes that need no session (login, setup, SAML start). An access token has no business
// there, so a token credential is refused with 403 token_not_allowed instead of being passed through; sessions and
// anonymous requests continue exactly as before. This is defence in depth: the token path of Middleware already ends
// an unresolvable token with 401 before this runs.
func RefuseToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsToken(r.Context()) {
			writeError(w, http.StatusForbidden, "token_not_allowed", "This route is not available to access tokens.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
