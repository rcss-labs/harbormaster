package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestMembers_HeartbeatAndLiveWindow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	live, silent := uuid.New(), uuid.New()

	if err := s.HeartbeatMember(ctx, store.Member{InstanceID: live, Roles: []string{"poller"}, Version: "v1", Slices: 3, Weight: 7, FocusObjects: 2}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := s.HeartbeatMember(ctx, store.Member{InstanceID: silent, Roles: []string{"web", "poller", "coordinator"}, Version: "v1"}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `update poll_members set heartbeat_at = now() - interval '45 seconds' where instance_id = $1`, silent); err != nil {
		t.Fatal(err)
	}

	got, err := s.LiveMembers(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].InstanceID != live {
		t.Fatalf("live members = %+v, want only %s", got, live)
	}
	m := got[0]
	if len(m.Roles) != 1 || m.Roles[0] != "poller" || m.Version != "v1" || m.Slices != 3 || m.Weight != 7 || m.FocusObjects != 2 {
		t.Fatalf("member = %+v", m)
	}

	// A second heartbeat updates in place, keeps started_at and refreshes the gauges.
	started := m.StartedAt
	if err := s.HeartbeatMember(ctx, store.Member{InstanceID: live, Roles: []string{"poller"}, Version: "v1", Slices: 4, Weight: 9}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LiveMembers(ctx, 30*time.Second)
	if len(got) != 1 || got[0].Slices != 4 || got[0].Weight != 9 || !got[0].StartedAt.Equal(started) {
		t.Fatalf("after second heartbeat = %+v, want slices 4, weight 9, started_at kept (%v)", got, started)
	}

	// The silent one comes back when it beats again.
	if err := s.HeartbeatMember(ctx, store.Member{InstanceID: silent, Roles: []string{"poller"}, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LiveMembers(ctx, 30*time.Second); len(got) != 2 {
		t.Fatalf("live members = %d, want 2", len(got))
	}
}

func TestMembers_DeleteAndDeleteStale(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	gone, stale, fresh := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{gone, stale, fresh} {
		if err := s.HeartbeatMember(ctx, store.Member{InstanceID: id, Roles: []string{"poller"}, Version: "v1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `update poll_members set heartbeat_at = now() - interval '11 minutes' where instance_id = $1`, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMember(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMember(ctx, gone); err != nil {
		t.Fatalf("deleting a missing member = %v, want nil", err)
	}
	n, err := s.DeleteStaleMembers(ctx, 10*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("DeleteStaleMembers = %d, %v; want 1, nil", n, err)
	}
	got, _ := s.LiveMembers(ctx, time.Hour)
	if len(got) != 1 || got[0].InstanceID != fresh {
		t.Fatalf("remaining = %+v, want only %s", got, fresh)
	}
}
