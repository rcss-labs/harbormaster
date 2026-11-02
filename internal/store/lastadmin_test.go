package store_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// Two concurrent demotions of the two remaining admins must never leave zero admins.
func TestSetAdminChecked_ConcurrentDemotionsKeepOneAdmin(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	b, _ := st.CreateUser(ctx, "bravo", "h", true, false)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int64{a.ID, b.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = st.SetAdminChecked(ctx, id, false)
		}()
	}
	wg.Wait()
	n, err := st.CountAdmins(ctx)
	if err != nil || n != 1 {
		t.Fatalf("admins = %d (%v), want 1; errs %v", n, err, errs)
	}
	refused := 0
	for _, e := range errs {
		if errors.Is(e, store.ErrLastAdmin) {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("errs = %v, want exactly one ErrLastAdmin", errs)
	}
}

func TestDeleteUserChecked_RefusesLastAdmin(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	if err := st.DeleteUserChecked(ctx, a.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}
}

func enableSAMLRaw(t *testing.T, st *store.Store, enabled bool) {
	t.Helper()
	_, err := st.Pool.Exec(t.Context(), `insert into saml_config
		(id, enabled, idp_entity_id, idp_sso_redirect_url, idp_certs, sp_entity_id, updated_at)
		values (1, $1, 'e', 'https://idp/sso', '{}', 'sp', now())
		on conflict (id) do update set enabled = excluded.enabled`, enabled)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetBreakGlassChecked_RefusesPasswordlessUser(t *testing.T) {
	st := storetest.Open(t)
	u, err := st.CreateSAMLUser(t.Context(), store.NewSAMLUser{Username: "jit", IdPEntityID: "e", ExternalID: "n", ExternalIDFormat: "unspecified"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetBreakGlassChecked(t.Context(), u.ID, true); !errors.Is(err, store.ErrNeedsPassword) {
		t.Fatalf("err = %v, want ErrNeedsPassword", err)
	}
	if _, err := st.SetBreakGlassChecked(t.Context(), 9999, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

func TestSetBreakGlassChecked_LinkedUserWithPasswordAllowed(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	if _, err := st.BindSSOIdentity(ctx, store.BindSSO{UserID: u.ID, IdPEntityID: "e", ExternalID: "n", ExternalIDFormat: "unspecified"}); err != nil {
		t.Fatal(err)
	}
	changed, err := st.SetBreakGlassChecked(ctx, u.ID, true)
	if err != nil || !changed {
		t.Fatalf("grant = %v, %v", changed, err)
	}
	changed, err = st.SetBreakGlassChecked(ctx, u.ID, true)
	if err != nil || changed {
		t.Fatalf("repeat grant = %v, %v; want unchanged", changed, err)
	}
	got, _ := st.GetUser(ctx, u.ID)
	if !got.BreakGlass {
		t.Fatalf("break_glass not set: %+v", got)
	}
}

func TestSetBreakGlassChecked_LastWhileEnabled(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	b, _ := st.CreateUser(ctx, "bravo", "h", true, false)
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := st.SetBreakGlassChecked(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	enableSAMLRaw(t, st, true)
	if _, err := st.SetBreakGlassChecked(ctx, a.ID, false); err != nil {
		t.Fatalf("revoking one of two: %v", err)
	}
	if _, err := st.SetBreakGlassChecked(ctx, b.ID, false); !errors.Is(err, store.ErrLastBreakGlassAdmin) {
		t.Fatalf("revoking last err = %v, want ErrLastBreakGlassAdmin", err)
	}
	enableSAMLRaw(t, st, false)
	if changed, err := st.SetBreakGlassChecked(ctx, b.ID, false); err != nil || !changed {
		t.Fatalf("revoking last while disabled = %v, %v", changed, err)
	}
}

func TestSetAdminChecked_DemoteLastBreakGlassAdminWhileEnabled(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	_, _ = st.CreateUser(ctx, "bravo", "h", true, false)
	if _, err := st.SetBreakGlassChecked(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	enableSAMLRaw(t, st, true)
	if _, err := st.SetAdminChecked(ctx, a.ID, false); !errors.Is(err, store.ErrLastBreakGlassAdmin) {
		t.Fatalf("demote err = %v, want ErrLastBreakGlassAdmin", err)
	}
	enableSAMLRaw(t, st, false)
	if changed, err := st.SetAdminChecked(ctx, a.ID, false); err != nil || !changed {
		t.Fatalf("demote while disabled = %v, %v", changed, err)
	}
}

func TestDeleteUserChecked_LastBreakGlassAdminWhileEnabled(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	a, _ := st.CreateUser(ctx, "alpha", "h", true, false)
	b, _ := st.CreateUser(ctx, "bravo", "h", true, false)
	if _, err := st.SetBreakGlassChecked(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	enableSAMLRaw(t, st, true)
	if err := st.DeleteUserChecked(ctx, a.ID); !errors.Is(err, store.ErrLastBreakGlassAdmin) {
		t.Fatalf("delete err = %v, want ErrLastBreakGlassAdmin", err)
	}
	if err := st.DeleteUserChecked(ctx, b.ID); err != nil {
		t.Fatalf("deleting a non-break-glass admin: %v", err)
	}
	enableSAMLRaw(t, st, false)
	if err := st.DeleteUserChecked(ctx, a.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("last admin delete err = %v, want ErrLastAdmin (disabled, only admin left)", err)
	}
}

// Written for Task 4: SetSAMLEnabled does not exist yet. Unskip there.
func TestSetSAMLEnabled_ConcurrentRevokeAndEnable_OneFails(t *testing.T) {
	t.Skip("needs store.SetSAMLEnabled from Task 4; unskip there")
}
