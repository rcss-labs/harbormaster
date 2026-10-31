package deploy_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	homelabAccountID = "482109375512"
	homelabRegion    = "us-east-1"
)

var (
	paymentsKey      = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: homelabRegion, Cluster: "core-services"}, Service: "payments-api"}
	notificationsKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: homelabRegion, Cluster: "core-services"}, Service: "notifications"}
)

type harness struct {
	svc   *deploy.Service
	fake  *fake.Fake
	store *store.Store
	pavel authz.Subject
}

// newHarness builds a deploy.Service over the seeded fake estate and a test store
// that has been polled once, so snapshots exist.
func newHarness(t *testing.T) *harness {
	t.Helper()
	st := storetest.Open(t)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	f := fake.New(fake.MockupEstate(func() time.Time { return now }))
	user, err := st.CreateUser(t.Context(), "pavel", "hash", true, false)
	if err != nil {
		t.Fatal(err)
	}
	target := awsx.Target{AccountID: homelabAccountID, Region: homelabRegion}
	if err := poller.PollOnce(t.Context(), st, f, poller.Config{MaxInFlight: 4}, target); err != nil {
		t.Fatal(err)
	}
	return &harness{
		svc:   &deploy.Service{Store: st, Clients: f, MaxDesiredCount: 50, Now: func() time.Time { return now }},
		fake:  f,
		store: st,
		pavel: authz.Subject{UserID: user.ID, Username: "pavel", Admin: true},
	}
}

// primaryRevision reads the PRIMARY deployment's revision of a fake service.
func (h *harness) primary(t *testing.T, key model.ServiceKey) ecstypes.Deployment {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	for _, s := range h.fake.Estate.Services {
		if s.ClusterName == key.Cluster && aws.ToString(s.Service.ServiceName) == key.Service {
			for _, d := range s.Service.Deployments {
				if aws.ToString(d.Status) == "PRIMARY" {
					return d
				}
			}
		}
	}
	t.Fatalf("no primary deployment for %v", key)
	return ecstypes.Deployment{}
}
