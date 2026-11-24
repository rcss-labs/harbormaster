package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// MintRunToken stores a read-only run token for user, tied to runID, and returns the raw token. It lives for
// HM_AGENT_RUN_TOKEN_TTL and ends with the user's sessions like any other token. The raw value is returned once and
// never stored or logged. redactEnv says whether environment values are hidden from calls made with it.
func (a *Service) MintRunToken(ctx context.Context, user store.User, runID uuid.UUID, redactEnv bool) (raw string, err error) {
	raw, parsed, err := apitoken.Generate(apitoken.KindRun)
	if err != nil {
		return "", err
	}
	// The token ends wherever the user's sessions of that source end (SAML change, user removal).
	authSource := "password"
	if user.SSOLinked() {
		authSource = "saml"
	}
	_, err = a.store.CreateRunToken(ctx, store.APIToken{
		PublicID:   apitoken.DisplayID(parsed),
		UserID:     user.ID,
		Kind:       string(apitoken.KindRun),
		Name:       "agent run",
		SecretHash: apitoken.Hash(parsed.Secret),
		Scope:      string(ScopeRead),
		AuthSource: authSource,
		ExpiresAt:  a.now().Add(a.cfg.AgentRunTokenTTL),
		RunID:      &runID,
		RedactEnv:  redactEnv,
	})
	if err != nil {
		return "", fmt.Errorf("mint run token: %w", err)
	}
	return raw, nil
}
