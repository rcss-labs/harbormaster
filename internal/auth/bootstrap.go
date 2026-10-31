package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Bootstrap creates the "admin" user from a Secrets Manager secret when no users exist.
// The secret value is never logged.
func Bootstrap(ctx context.Context, st *store.Store, secrets awsx.SecretReader, secretARN string, log *slog.Logger) error {
	if secretARN == "" {
		return nil
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	value, err := secrets.ReadSecret(ctx, secretARN)
	if err != nil {
		return fmt.Errorf("read bootstrap admin secret: %w", err)
	}
	if err := ValidatePassword(value); err != nil {
		return fmt.Errorf("bootstrap admin secret is not a valid password: %w", err)
	}
	hash, err := HashPassword(value)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, "admin", hash, true, false); err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	log.Info("created bootstrap admin user", "username", "admin")
	return nil
}
