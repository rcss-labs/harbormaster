package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// ErrBadToken is returned when the setup token does not match.
var ErrBadToken = errors.New("Setup token is wrong.")

// ErrSetupDone is returned once any user exists.
var ErrSetupDone = errors.New("setup already done")

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,31}$`)

// ValidateUsername enforces the username policy (already lower-cased and trimmed).
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return &ValidationError{Msg: "Use 2-32 characters: lowercase letters, digits, dot, dash or underscore, starting with a letter."}
	}
	return nil
}

// Setup guards the first-run "create the first global admin" flow with a one-time token.
type Setup struct {
	store *store.Store
	mu    sync.Mutex
	token string // empty when users already existed at startup
}

// NewSetup generates a setup token when no users exist and logs it for the operator.
func NewSetup(ctx context.Context, st *store.Store, log *slog.Logger) (*Setup, error) {
	s := &Setup{store: st}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if s.token, err = randomToken(); err != nil {
			return nil, err
		}
		// The token is a one-time bootstrap secret that the operator must see; this log line is its delivery channel.
		log.Info("no users exist; open the app and create the first global admin with setup token " + s.token)
	}
	return s, nil
}

// Needed reports whether no user exists yet.
func (s *Setup) Needed(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	return n == 0, err
}

// Complete creates the first admin when token matches and no user exists.
func (s *Setup) Complete(ctx context.Context, token, username, password string) (store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	needed, err := s.Needed(ctx)
	if err != nil {
		return store.User{}, err
	}
	if !needed {
		return store.User{}, ErrSetupDone
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return store.User{}, ErrBadToken
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if err := ValidateUsername(username); err != nil {
		return store.User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return store.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	user, err := s.store.CreateUser(ctx, username, hash, true, false)
	if err != nil {
		return store.User{}, err
	}
	s.token = ""
	if err := s.store.Audit(ctx, store.AuditEntry{
		ActorUserID: &user.ID, ActorUsername: user.Username, Action: "setup_complete",
		Summary: fmt.Sprintf("Created first global admin %s.", user.Username),
	}); err != nil {
		return store.User{}, err
	}
	return user, nil
}
