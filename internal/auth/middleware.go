package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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

// Middleware attaches the session, user and subject to the request context when the cookie is valid.
// A missing or invalid cookie continues unauthenticated; RequireAuth decides whether that is allowed.
func (a *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, user, err := a.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		subject, err := a.store.LoadSubject(r.Context(), user.ID)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		ctx = context.WithValue(ctx, userKey{}, user)
		ctx = context.WithValue(ctx, subjectKey{}, subject)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuth rejects unauthenticated requests with 401.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := SessionFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF requires the session's CSRF token on every method except GET, HEAD and OPTIONS.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
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
