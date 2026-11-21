package devseed_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// TestForceApply_SeedsBaseImages: `make run` and e2e need configured base images so base: searches have results.
func TestForceApply_SeedsBaseImages(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	var staging string
	for _, a := range fake.MockupEstate(time.Now).Accounts {
		if a.Name == "staging-example" {
			staging = a.ID
		}
	}
	webFrontend := store.RepoKey{AccountID: "482109375512", Region: "us-east-1", Repository: "web-frontend"}
	authService := store.RepoKey{AccountID: "482109375512", Region: "us-east-1", Repository: "auth-service"}
	stagingWeb := store.RepoKey{AccountID: staging, Region: "us-east-1", Repository: "web"}
	paymentsAPI := store.RepoKey{AccountID: "482109375512", Region: "us-east-1", Repository: "payments-api"}
	want := map[store.RepoKey]string{webFrontend: "node:18-alpine", authService: "node:20-alpine", stagingWeb: "node:18-alpine"}

	for i := 1; i <= 2; i++ {
		if err := devseed.ForceApply(ctx, st); err != nil {
			t.Fatalf("ForceApply #%d: %v", i, err)
		}
		got, err := st.RepoBases(ctx, []store.RepoKey{webFrontend, authService, stagingWeb, paymentsAPI})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after apply #%d bases = %v, want %v", i, got, want)
		}
	}
}
