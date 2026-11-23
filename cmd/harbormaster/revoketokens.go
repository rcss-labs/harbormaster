package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const revokeTokensUsage = "usage: harbormaster revoke-tokens --all | --user <username>"

// runRevokeTokens is the operator's "revoke everything" after an incident. It deletes rows directly, so it works
// whether or not HM_API_TOKENS or HM_OAUTH are on: switching them off alone leaves the rows, and switching back on
// would revive them. Without --all or --user it refuses, so a mass revoke is never an accident.
func runRevokeTokens(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	flags := flag.NewFlagSet("revoke-tokens", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	all := flags.Bool("all", false, "revoke every access token, OAuth grant and pending authorization code")
	username := flags.String("user", "", "revoke only this user's tokens, grants and codes")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || *all == (*username != "") {
		return errUsage{revokeTokensUsage}
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	dbURL, err := resolveDatabaseURL(ctx, cfg)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if *all {
		revoked, err := st.DeleteAllAPICredentials(ctx)
		if err != nil {
			return err
		}
		if err := st.Audit(ctx, store.AuditEntry{
			ActorUsername: "cli", Action: "tokens_revoke_all",
			Summary: fmt.Sprintf("revoked all access credentials: %d tokens, %d grants, %d codes", revoked.Tokens, revoked.Grants, revoked.Codes),
			Details: map[string]any{"tokens": revoked.Tokens, "grants": revoked.Grants, "codes": revoked.Codes},
		}); err != nil {
			return fmt.Errorf("credentials were revoked but the audit entry failed: %w", err)
		}
		fmt.Fprintf(stdout, "revoked %d tokens, %d grants, %d codes\n", revoked.Tokens, revoked.Grants, revoked.Codes)
		return nil
	}

	user, err := st.GetUserByUsername(ctx, *username)
	if err != nil {
		return fmt.Errorf("find user %q: %w", *username, err)
	}
	tokens, err := st.DeleteAPITokensForUser(ctx, user.ID, "")
	if err != nil {
		return err
	}
	if err := st.Audit(ctx, store.AuditEntry{
		ActorUsername: "cli", Action: "tokens_revoke_user", Target: user.Username,
		Summary: fmt.Sprintf("revoked all access credentials of %s: %d tokens", user.Username, tokens),
		Details: map[string]any{"tokens": tokens},
	}); err != nil {
		return fmt.Errorf("credentials were revoked but the audit entry failed: %w", err)
	}
	fmt.Fprintf(stdout, "revoked %d tokens of %s (and their grants and codes)\n", tokens, user.Username)
	return nil
}
