package store_test

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// A service exists only under its own account, region, cluster and name: the same names elsewhere are another service.
func TestExistingServices_MatchesTheWholeKey(t *testing.T) {
	fx := newReadFixture(t)
	here := serviceKey("core-services", "payments-api")
	otherAccount, otherRegion, otherCluster, otherName := here, here, here, here
	otherAccount.AccountID = "222222222222"
	otherRegion.Region = "us-east-1"
	otherCluster.Cluster = "background-jobs"
	otherName.Service = "retired"
	other := serviceKey("background-jobs", "image-resizer")

	got, err := fx.s.ExistingServices(t.Context(), []model.ServiceKey{here, otherAccount, otherRegion, otherCluster, otherName, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[here] || !got[other] {
		t.Fatalf("existing = %v, want exactly payments-api and image-resizer", got)
	}
	if empty, err := fx.s.ExistingServices(t.Context(), nil); err != nil || len(empty) != 0 {
		t.Errorf("no keys: %v, %v", empty, err)
	}
}
