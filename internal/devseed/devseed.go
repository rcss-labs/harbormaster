// Package devseed loads the design mockup's users, tags, permissions, accounts and audit
// history into an empty database so `serve --fake-aws` demonstrates the whole product.
package devseed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Password is the shared password of every seeded user (also used by the e2e checks).
const Password = "harbormaster-e2e-1"

// seedMarker is stored in audit details so a second Apply can tell the history is already seeded.
const seedMarker = "devseed"

const (
	viewLogsOperateTerminal = authz.ActionSet(authz.View | authz.Logs | authz.Operate | authz.Terminal)
	viewLogs                = authz.ActionSet(authz.View | authz.Logs)
)

// Apply seeds the database. It is idempotent: existing tags, users, accounts and audit
// history are left alone, so applying twice changes nothing.
func Apply(ctx context.Context, st *store.Store) error {
	estate := fake.MockupEstate(time.Now)

	for _, a := range estate.Accounts {
		if err := ensureAccount(ctx, st, a); err != nil {
			return err
		}
	}

	// Staging before Prod so ids are Dev 1, Staging 2, Prod 3 on a fresh database.
	staging, err := ensureTag(ctx, st, "Staging", "amber")
	if err != nil {
		return err
	}
	prod, err := ensureTag(ctx, st, "Prod", "red")
	if err != nil {
		return err
	}
	dev, err := st.DevTag(ctx)
	if err != nil {
		return err
	}

	hash, err := auth.HashPassword(Password)
	if err != nil {
		return err
	}
	users := []struct {
		name  string
		admin bool
		perms map[authz.TagID]authz.ActionSet
	}{
		{"pavel", true, nil},
		{"dana", false, map[authz.TagID]authz.ActionSet{dev.ID: authz.Presets["Full"], staging.ID: authz.Presets["Deployer"], prod.ID: viewLogs}},
		{"omer", false, map[authz.TagID]authz.ActionSet{dev.ID: authz.Presets["Operator"], staging.ID: authz.Presets["Viewer"]}},
		{"oncall", false, map[authz.TagID]authz.ActionSet{staging.ID: viewLogsOperateTerminal, prod.ID: viewLogsOperateTerminal}},
	}
	ids := map[string]int64{}
	for _, u := range users {
		user, err := st.GetUserByUsername(ctx, u.name)
		if errors.Is(err, store.ErrNotFound) {
			user, err = st.CreateUser(ctx, u.name, hash, u.admin, false)
		}
		if err != nil {
			return fmt.Errorf("seed user %s: %w", u.name, err)
		}
		ids[u.name] = user.ID
		for tag, actions := range u.perms {
			if err := st.SetPermission(ctx, user.ID, tag, actions); err != nil {
				return fmt.Errorf("seed permission for %s: %w", u.name, err)
			}
		}
	}

	if err := st.SetClusterTag(ctx, model.ClusterKey{AccountID: accountIDByName(estate, "homelab-admin"), Region: "us-east-1", Cluster: "core-services"}, prod.ID); err != nil {
		return fmt.Errorf("tag core-services: %w", err)
	}
	if err := st.SetClusterTag(ctx, model.ClusterKey{AccountID: accountIDByName(estate, "staging-example"), Region: "us-east-1", Cluster: "staging-web"}, staging.ID); err != nil {
		return fmt.Errorf("tag staging-web: %w", err)
	}

	return seedAudit(ctx, st, ids["pavel"])
}

func accountIDByName(estate *fake.Estate, name string) string {
	for _, a := range estate.Accounts {
		if a.Name == name {
			return a.ID
		}
	}
	return ""
}

func ensureAccount(ctx context.Context, st *store.Store, a fake.Account) error {
	if _, err := st.GetAccountByAccountID(ctx, a.ID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, err := st.CreateAccount(ctx, store.AWSAccount{
		Name: a.Name, AccountID: a.ID,
		RoleARN:    "arn:aws:iam::" + a.ID + ":role/harbormaster-access",
		ExternalID: "harbormaster-dev-external-id",
		Regions:    a.Regions, Enabled: true,
	})
	if err != nil {
		return fmt.Errorf("seed account %s: %w", a.Name, err)
	}
	return nil
}

func ensureTag(ctx context.Context, st *store.Store, name, color string) (store.Tag, error) {
	tags, err := st.ListTags(ctx)
	if err != nil {
		return store.Tag{}, err
	}
	for _, t := range tags {
		if t.Name == name {
			return t, nil
		}
	}
	return st.CreateTag(ctx, name, color)
}

// seedAudit writes the mockup's activity history once.
func seedAudit(ctx context.Context, st *store.Store, pavelID int64) error {
	existing, err := st.ListAudit(ctx, 500, 0)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Details["source"] == seedMarker {
			return nil
		}
	}
	now := time.Now()
	entries := []struct {
		ago            time.Duration
		action, target string
		summary        string
	}{
		{2 * time.Minute, "auth.login", "", "Signed in."},
		{3 * time.Hour, "permission.set", "oncall", "Gave oncall Terminal on Prod."},
		{5 * time.Hour, "cluster.tag", "staging-web", "Tagged cluster staging-web as Staging (was Dev)."},
		{48 * time.Hour, "user.create", "omer", "Created user omer with Operator on Dev."},
		{6 * 24 * time.Hour, "tag.create", "Prod", "Created tag Prod."},
	}
	for _, e := range entries {
		err := st.Audit(ctx, store.AuditEntry{
			TS: now.Add(-e.ago), ActorUserID: &pavelID, ActorUsername: "pavel",
			Action: e.action, Target: e.target, Summary: e.summary,
			Details: map[string]any{"source": seedMarker},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
