package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestSetPermission_ZeroDeletes(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	u, _ := s.CreateUser(ctx, "alice", "h", false, false)

	if err := s.SetPermission(ctx, u.ID, tag.ID, authz.ActionSet(authz.View|authz.Logs)); err != nil {
		t.Fatal(err)
	}
	perms, _ := s.PermissionsOf(ctx, u.ID)
	if perms[tag.ID] != authz.ActionSet(authz.View|authz.Logs) {
		t.Fatalf("perms = %v", perms)
	}
	// A non-zero set is normalised: any action implies View.
	if err := s.SetPermission(ctx, u.ID, tag.ID, authz.ActionSet(authz.Logs)); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.PermissionsOf(ctx, u.ID)
	if perms[tag.ID] != authz.ActionSet(authz.View|authz.Logs) {
		t.Fatalf("normalised perms = %v", perms)
	}
	// Zero removes the row.
	if err := s.SetPermission(ctx, u.ID, tag.ID, 0); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.PermissionsOf(ctx, u.ID)
	if len(perms) != 0 {
		t.Fatalf("perms after zero = %v, want empty", perms)
	}
	// Zero on a missing row is not an error.
	if err := s.SetPermission(ctx, u.ID, tag.ID, 0); err != nil {
		t.Fatalf("zero on missing row: %v", err)
	}
}

func TestAllPermissionsAndUsersWithView(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	bob, _ := s.CreateUser(ctx, "bob", "h", false, false)
	alice, _ := s.CreateUser(ctx, "alice", "h", false, false)
	_ = s.SetPermission(ctx, bob.ID, tag.ID, authz.ActionSet(authz.View))
	_ = s.SetPermission(ctx, alice.ID, tag.ID, authz.ActionSet(authz.View|authz.Deploy))

	all, err := s.AllPermissions(ctx)
	if err != nil || len(all) != 2 || all[alice.ID][tag.ID] != authz.ActionSet(authz.View|authz.Deploy) {
		t.Fatalf("AllPermissions = %v, %v", all, err)
	}
	users, err := s.UsersWithView(ctx, tag.ID)
	if err != nil || len(users) != 2 || users[0].Username != "alice" || users[1].Username != "bob" {
		t.Fatalf("UsersWithView = %v, %v", users, err)
	}
}

func TestLoadSubject_HasPermsAndAdminFlag(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	u, _ := s.CreateUser(ctx, "alice", "h", true, false)
	_ = s.SetPermission(ctx, u.ID, tag.ID, authz.ActionSet(authz.View|authz.Operate))

	sub, err := s.LoadSubject(ctx, u.ID)
	if err != nil {
		t.Fatalf("LoadSubject: %v", err)
	}
	if sub.UserID != u.ID || sub.Username != "alice" || !sub.Admin {
		t.Errorf("subject = %+v", sub)
	}
	if sub.Perms[tag.ID] != authz.ActionSet(authz.View|authz.Operate) {
		t.Errorf("perms = %v", sub.Perms)
	}
	plain, _ := s.CreateUser(ctx, "bob", "h", false, false)
	sub, err = s.LoadSubject(ctx, plain.ID)
	if err != nil || sub.Perms == nil || len(sub.Perms) != 0 {
		t.Errorf("no-perm subject = %+v, %v; want non-nil empty Perms", sub, err)
	}
	if _, err := s.LoadSubject(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing user error = %v, want ErrNotFound", err)
	}
}

// Rows written before the Accept risk permission existed (up to 63, Full included) read back
// unchanged, and a value with the Accept risk bit survives a round trip.
func TestSetPermission_AcceptRiskBitIsStoredAndOldValuesKeepTheirValue(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	u, _ := s.CreateUser(ctx, "alice", "h", false, false)

	full := authz.Presets["Full"]
	if err := s.SetPermission(ctx, u.ID, tag.ID, full); err != nil {
		t.Fatal(err)
	}
	perms, _ := s.PermissionsOf(ctx, u.ID)
	if perms[tag.ID] != full || perms[tag.ID].Has(authz.AcceptRisk) {
		t.Fatalf("Full read back as %d", perms[tag.ID])
	}
	withRisk := full | authz.ActionSet(authz.AcceptRisk)
	if err := s.SetPermission(ctx, u.ID, tag.ID, withRisk); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.PermissionsOf(ctx, u.ID)
	if perms[tag.ID] != withRisk {
		t.Fatalf("with Accept risk read back as %d, want %d", perms[tag.ID], withRisk)
	}
}
