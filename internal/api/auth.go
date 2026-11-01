package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const maxBodyBytes = 1 << 20 // 1 MiB cap on JSON request bodies

// decodeJSON reads the request body into v; oversized, malformed or trailing-data bodies become a 422.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeBody(w, r, v, false)
}

// decodeStrictJSON is decodeJSON that also rejects keys v has no field for, so a misspelled
// field is an error instead of being silently ignored (used by the change endpoints).
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeBody(w, r, v, true)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, strict bool) error {
	// Requiring JSON blocks cross-site text/plain form posts to the CSRF-exempt login and setup routes.
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: "invalid", Message: "Send JSON (Content-Type: application/json)."}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(v); err != nil {
		return invalid("Invalid request body.")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalid("Invalid request body.")
	}
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		s.fail(w, invalid("Enter your username and password."))
		return
	}
	result, err := s.Auth.Login(r.Context(), body.Username, body.Password, s.clientIP(r), r.UserAgent())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.revokePresentedSession(r)
	s.startedSession(w, r, result)
}

// revokePresentedSession deletes the session the login request arrived with, so a fresh sign-in
// does not leave the previous session row valid until it expires.
func (s *Server) revokePresentedSession(r *http.Request) {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	if err := s.Auth.Logout(r.Context(), cookie.Value); err != nil {
		s.log().Warn("revoke previous session failed", "error", err)
	}
}

// startedSession sets the cookie and answers login with {"user": MeDTO}.
func (s *Server) startedSession(w http.ResponseWriter, r *http.Request, result auth.LoginResult) {
	me, ok := s.meForNewSession(w, r, result)
	if !ok {
		return
	}
	s.Auth.SetCookie(w, result.SessionID)
	writeJSON(w, http.StatusOK, LoginResponse{User: me})
}

func (s *Server) meForNewSession(w http.ResponseWriter, r *http.Request, result auth.LoginResult) (MeDTO, bool) {
	subject, err := s.Store.LoadSubject(r.Context(), result.User.ID)
	if err != nil {
		s.fail(w, err)
		return MeDTO{}, false
	}
	me, err := s.buildMe(r.Context(), result.User, result.CSRFToken, subject)
	if err != nil {
		s.fail(w, err)
		return MeDTO{}, false
	}
	return me, true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		if err := s.Auth.Logout(r.Context(), cookie.Value); err != nil {
			s.fail(w, err)
			return
		}
	}
	if user, ok := auth.UserFrom(r.Context()); ok {
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			ActorUserID: &user.ID, ActorUsername: user.Username,
			Action: "sign_out", Summary: "Signed out.", IP: s.clientIP(r),
		})
	}
	s.Auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Current == "" || body.New == "" {
		s.fail(w, invalid("Enter your current and new password."))
		return
	}
	user, _ := auth.UserFrom(r.Context())
	// r.Context() carries the current session (auth.Middleware) so it survives the change.
	if err := s.Auth.ChangePassword(r.Context(), user.ID, body.Current, body.New); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupNeeded(w http.ResponseWriter, r *http.Request) {
	needed, err := s.Setup.Needed(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SetupStatusDTO{Needed: needed})
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	user, err := s.Setup.Complete(r.Context(), body.Token, body.Username, body.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.Auth.StartSession(r.Context(), user, s.clientIP(r), r.UserAgent())
	if err != nil {
		s.fail(w, err)
		return
	}
	me, ok := s.meForNewSession(w, r, result)
	if !ok {
		return
	}
	s.Auth.SetCookie(w, result.SessionID)
	writeJSON(w, http.StatusOK, me)
}
