package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestNotificationTargets_ListsEverythingActive(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	for _, a := range []store.AWSAccount{
		{Name: "zeta", AccountID: "222222222222", RoleARN: "arn:r2", ExternalID: "e2", Regions: []string{"eu-west-1"}, Enabled: true},
		{Name: "alpha", AccountID: "111111111111", RoleARN: "arn:r1", ExternalID: "e1", Regions: []string{"us-east-1", "eu-west-1"}, Enabled: false},
	} {
		if _, err := s.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ck := func(account, region, cluster string) model.ClusterKey {
		return model.ClusterKey{AccountID: account, Region: region, Cluster: cluster}
	}
	if _, err := s.UpsertClusters(ctx, []model.Cluster{
		{Key: ck("222222222222", "eu-west-1", "web"), PolledAt: now},
		{Key: ck("111111111111", "us-east-1", "core"), PolledAt: now},
		{Key: ck("111111111111", "eu-west-1", "batch"), PolledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	svcs := []model.Service{
		{Key: model.ServiceKey{ClusterKey: ck("111111111111", "us-east-1", "core"), Service: "b-api"}, Status: "ACTIVE"},
		{Key: model.ServiceKey{ClusterKey: ck("111111111111", "us-east-1", "core"), Service: "a-worker"}, Status: "ACTIVE"},
		{Key: model.ServiceKey{ClusterKey: ck("111111111111", "us-east-1", "core"), Service: "gone"}, Status: "INACTIVE"},
		{Key: model.ServiceKey{ClusterKey: ck("222222222222", "eu-west-1", "web"), Service: "site"}, Status: "ACTIVE"},
	}
	for i := range svcs {
		svcs[i].Raw, svcs[i].PolledAt = []byte(`{}`), now
	}
	if _, err := s.UpsertServices(ctx, svcs); err != nil {
		t.Fatal(err)
	}

	got, err := s.NotificationTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 2 || got.Accounts[0].Name != "alpha" || got.Accounts[1].Name != "zeta" {
		t.Fatalf("accounts = %+v, want alpha then zeta (a disabled account is still listed)", got.Accounts)
	}
	var clusters []string
	for _, c := range got.Clusters {
		clusters = append(clusters, c.AccountName+"/"+c.Key.Region+"/"+c.Key.Cluster)
	}
	wantClusters := []string{"alpha/eu-west-1/batch", "alpha/us-east-1/core", "zeta/eu-west-1/web"}
	if len(clusters) != len(wantClusters) {
		t.Fatalf("clusters = %v, want %v", clusters, wantClusters)
	}
	for i := range wantClusters {
		if clusters[i] != wantClusters[i] {
			t.Fatalf("clusters = %v, want %v", clusters, wantClusters)
		}
	}
	var services []string
	for _, sv := range got.Services {
		services = append(services, sv.AccountName+"/"+sv.Key.Cluster+"/"+sv.Key.Service)
	}
	wantServices := []string{"alpha/core/a-worker", "alpha/core/b-api", "zeta/web/site"}
	if len(services) != len(wantServices) {
		t.Fatalf("services = %v, want %v (the INACTIVE one is left out)", services, wantServices)
	}
	for i := range wantServices {
		if services[i] != wantServices[i] {
			t.Fatalf("services = %v, want %v", services, wantServices)
		}
	}
}
