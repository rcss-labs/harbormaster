package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// MintRunToken stores a read-only run token for a person, tied to runID, and returns the raw token. It lives for
// HM_AGENT_RUN_TOKEN_TTL and ends with the user's sessions like any other token. The raw value is returned once and
// never stored or logged. redactEnv says whether environment values are hidden from calls made with it. It refuses any
// user that is not a human; background runs use MintAgentRunToken.
func (a *Service) MintRunToken(ctx context.Context, user store.User, runID uuid.UUID, redactEnv bool) (raw string, err error) {
	if user.Kind != store.UserKindHuman {
		return "", errors.New("mint run token: only a person can chat")
	}
	// The token ends wherever the user's sessions of that source end (SAML change, user removal).
	authSource := "password"
	if user.SSOLinked() {
		authSource = "saml"
	}
	return a.mintRunToken(ctx, user.ID, authSource, runID, redactEnv)
}

// MintAgentRunToken stores a read-only run token for the agent identity and returns it with the identity. It loads the
// identity itself (creating it on first use), so the caller cannot pass another user. The identity has no SSO link, so
// the source is "password" (the column allows only that and "saml"); the token lives minutes and is deleted when the
// run ends.
func (a *Service) MintAgentRunToken(ctx context.Context, runID uuid.UUID, redactEnv bool) (raw string, identity store.User, err error) {
	identity, err = a.store.EnsureAgentIdentity(ctx)
	if err != nil {
		return "", store.User{}, fmt.Errorf("mint agent run token: %w", err)
	}
	if !identity.IsAgent() {
		return "", store.User{}, errors.New("mint agent run token: not the agent identity")
	}
	raw, err = a.mintRunToken(ctx, identity.ID, "password", runID, redactEnv)
	if err != nil {
		return "", store.User{}, err
	}
	return raw, identity, nil
}

func (a *Service) mintRunToken(ctx context.Context, userID int64, authSource string, runID uuid.UUID, redactEnv bool) (string, error) {
	raw, parsed, err := apitoken.Generate(apitoken.KindRun)
	if err != nil {
		return "", err
	}
	_, err = a.store.CreateRunToken(ctx, store.APIToken{
		PublicID:   apitoken.DisplayID(parsed),
		UserID:     userID,
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
