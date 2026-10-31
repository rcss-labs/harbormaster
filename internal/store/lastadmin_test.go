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
