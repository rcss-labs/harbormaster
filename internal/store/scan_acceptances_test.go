package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestCreateScanAcceptances_RefusesDuplicateActivePerService(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	a, b := serviceKey("core-services", "payments-api"), serviceKey("core-services", "notifications")
	openFindingFixture(t, fx, "sha256:a")
	base := store.ScanAcceptance{Digest: "sha256:a", VulnID: "CVE-0000-0001", PackageName: "libc", PackageVersion: "1.0",
		Reason: "not reachable", ExpiresAt: fx.now.Add(time.Hour)}

	if ids, err := s.CreateScanAcceptances(ctx, base, []model.ServiceKey{a}); err != nil || len(ids) != 1 {
		t.Fatalf("first accept for A: ids=%v err=%v", ids, err)
	}
	if _, err := s.CreateScanAcceptances(ctx, base, []model.ServiceKey{a}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second accept for A: err=%v, want ErrConflict", err)
	}
	// A conflict on one service writes nothing for the others in the same call.
	if _, err := s.CreateScanAcceptances(ctx, base, []model.ServiceKey{b, a}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("accept for B and A: err=%v, want ErrConflict", err)
	}
	if ids, err := s.CreateScanAcceptances(ctx, base, []model.ServiceKey{b}); err != nil || len(ids) != 1 {
		t.Fatalf("accept for B: ids=%v err=%v", ids, err)
	}
	active, err := s.ActiveScanAcceptances(ctx, "sha256:a", fx.now)
	if err != nil || len(active) != 2 {
		t.Fatalf("active acceptances = %d err=%v, want 2", len(active), err)
	}
}

func TestDeleteUser_KeepsAcceptanceWithoutTheUser(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	user, err := s.CreateUser(ctx, "alice", "hash", false, false)
	if err != nil {
		t.Fatal(err)
	}
	openFindingFixture(t, fx, "sha256:a")
	ids, err := s.CreateScanAcceptances(ctx, store.ScanAcceptance{Digest: "sha256:a", VulnID: "CVE-0000-0001", PackageName: "libc",
		PackageVersion: "1.0", Reason: "not reachable", AcceptedBy: &user.ID, ExpiresAt: fx.now.Add(time.Hour)},
		[]model.ServiceKey{serviceKey("core-services", "payments-api")})
	if err != nil || len(ids) != 1 {
		t.Fatalf("accept: ids=%v err=%v", ids, err)
	}
	withUser, err := s.GetScanAcceptance(ctx, ids[0])
	if err != nil || withUser.AcceptedByUsername != "alice" {
		t.Fatalf("acceptance before delete: username=%q err=%v, want alice", withUser.AcceptedByUsername, err)
	}

	if err := s.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetScanAcceptance(ctx, ids[0])
	if err != nil {
		t.Fatalf("acceptance after deleting its user: %v", err)
	}
	if got.AcceptedBy != nil || got.AcceptedByUsername != "" || got.Reason != "not reachable" {
		t.Fatalf("acceptance after delete: by=%v username=%q reason=%q", got.AcceptedBy, got.AcceptedByUsername, got.Reason)
	}
}
