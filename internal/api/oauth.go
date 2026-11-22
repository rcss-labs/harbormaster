package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/oauthsrv"
)

// oauthErrorDTO is the body of a refused OAuth request. RedirectTo is set only when the client and its redirect URI
// were validated; otherwise the error is for the user's eyes only and the browser goes nowhere.
type oauthErrorDTO struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	RedirectTo       string `json:"redirect_to,omitempty"`
}

// oauthAuthorizeViewDTO is the body of GET /api/oauth/authorize: what the consent page shows.
type oauthAuthorizeViewDTO struct {
	oauthsrv.AuthorizeView
	Username string `json:"username"`
}

type oauthRedirectDTO struct {
	RedirectTo string `json:"redirect_to"`
}

func (s *Server) writeOAuthAuthorizeError(w http.ResponseWriter, req oauthsrv.AuthorizeRequest, aerr *oauthsrv.AuthorizeError) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusBadRequest, oauthErrorDTO{
		Error: aerr.Code, ErrorDescription: aerr.Description, RedirectTo: s.OAuth.ErrorRedirect(req, aerr),
	})
}

func authorizeRequestFromQuery(r *http.Request) oauthsrv.AuthorizeRequest {
	q := r.URL.Query()
	return oauthsrv.AuthorizeRequest{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		Resource:            q.Get("resource"),
	}
}

// validateOAuthAuthorize answers GET /api/oauth/authorize for the consent page: the view, or a 400 whose redirect_to
// is present only for errors that may be sent back to a validated client.
func (s *Server) validateOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	subject := auth.SubjectFrom(r.Context())
	req := authorizeRequestFromQuery(r)
	view, aerr := s.OAuth.Validate(r.Context(), req, user, subject)
	if aerr != nil {
		s.writeOAuthAuthorizeError(w, req, aerr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, oauthAuthorizeViewDTO{AuthorizeView: view, Username: user.Username})
}

// decideOAuthAuthorize answers POST /api/oauth/authorize: approve stores a code and returns the client's redirect
// with it; deny returns the redirect with access_denied.
func (s *Server) decideOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Request  oauthsrv.AuthorizeRequest `json:"request"`
		Decision string                    `json:"decision"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	var (
		redirectTo string
		aerr       *oauthsrv.AuthorizeError
	)
	switch body.Decision {
	case "approve":
		user, _ := auth.UserFrom(r.Context())
		authSource := "password"
		if sess, ok := auth.SessionFrom(r.Context()); ok && sess.AuthSource != "" {
			authSource = sess.AuthSource
		}
		redirectTo, aerr = s.OAuth.Approve(r.Context(), body.Request, user, authSource)
	case "deny":
		redirectTo, aerr = s.OAuth.Deny(r.Context(), body.Request)
	default:
		s.fail(w, invalid(`decision must be "approve" or "deny".`))
		return
	}
	if aerr != nil {
		s.writeOAuthAuthorizeError(w, body.Request, aerr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, oauthRedirectDTO{RedirectTo: redirectTo})
}
