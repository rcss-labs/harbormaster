package api

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,31}$`)

const lastAdminMessage = "Harbormaster needs at least one global admin."

// UserDTO is a user row on the admin users screen.
type UserDTO struct {
	ID                 int64      `json:"id"`
	Username           string     `json:"username"`
	Admin              bool       `json:"admin"`
	MustChangePassword bool       `json:"must_change_password"`
	LastSignInAt       *time.Time `json:"last_sign_in_at"`
	CanSee             []TagDTO   `json:"can_see"`
}

// buildUserDTOs maps users to DTOs; CanSee lists tags the user holds View on (all tags for admins),
// ordered by tag name.
func (s *Server) buildUserDTOs(r *http.Request, users []store.User) ([]UserDTO, error) {
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		return nil, err
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	perms, err := s.Store.AllPermissions(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]UserDTO, 0, len(users))
	for _, u := range users {
		dto := UserDTO{ID: u.ID, Username: u.Username, Admin: u.Admin, MustChangePassword: u.MustChangePassword,
			LastSignInAt: u.LastSignInAt, CanSee: []TagDTO{}}
		for _, t := range tags {
			if u.Admin || perms[u.ID][t.ID].Has(authz.View) {
				dto.CanSee = append(dto.CanSee, TagDTO{ID: int64(t.ID), Name: t.Name, Color: t.Color})
			}
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.buildUserDTOs(r, users)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ItemsResponse[UserDTO]{Items: items})
}

// auditAdmin records an admin action attributed to the caller.
func (s *Server) auditAdmin(r *http.Request, action, target, summary string, details map[string]any) {
	actor, _ := auth.UserFrom(r.Context())
	if err := s.Store.Audit(r.Context(), store.AuditEntry{
		ActorUserID: &actor.ID, ActorUsername: actor.Username,
		Action: action, Target: target, Summary: summary, Details: details, IP: s.clientIP(r),
	}); err != nil {
		s.log().Error("audit write failed", "action", action, "error", err)
	}
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Admin    bool   `json:"admin"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if !usernamePattern.MatchString(body.Username) {
		s.fail(w, invalid("Use 2–32 lowercase letters, digits, dots, hyphens or underscores."))
		return
	}
	temp := auth.GenerateTemporaryPassword()
	hash, err := auth.HashPassword(temp)
	if err != nil {
		s.fail(w, err)
		return
	}
	user, err := s.Store.CreateUser(r.Context(), body.Username, hash, body.Admin, true)
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, &APIError{Status: http.StatusConflict, Code: "conflict", Message: body.Username + " already exists."})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	summary := "Created user " + user.Username + "."
	if user.Admin {
		summary = "Created user " + user.Username + " as global admin."
	}
	s.auditAdmin(r, "user_create", "user:"+user.Username, summary, nil)
	dtos, err := s.buildUserDTOs(r, []store.User{user})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateUserResponse{User: dtos[0], TemporaryPassword: temp})
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	if id == actor.ID {
		s.fail(w, invalid("You cannot delete yourself."))
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteUserChecked(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			s.fail(w, invalid(lastAdminMessage))
			return
		}
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "user_delete", "user:"+target.Username, "Deleted user "+target.Username+".", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
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
	temp, err := s.Auth.AdminResetPassword(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "password_reset", "user:"+target.Username, "Reset the password of "+target.Username+".", nil)
	writeJSON(w, http.StatusOK, map[string]string{"temporary_password": temp})
}

func (s *Server) setUserAdmin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body struct {
		Admin bool `json:"admin"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	changed, err := s.Store.SetAdminChecked(r.Context(), id, body.Admin)
	if err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			s.fail(w, invalid(lastAdminMessage))
			return
		}
		s.fail(w, err)
		return
	}
	if changed {
		if body.Admin {
			s.auditAdmin(r, "admin_grant", "user:"+target.Username, "Made "+target.Username+" a global admin.", nil)
		} else {
			s.auditAdmin(r, "admin_revoke", "user:"+target.Username, "Removed "+target.Username+" as global admin.", nil)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
