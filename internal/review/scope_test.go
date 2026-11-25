package review

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// cover is the coverage of a scope for the identity as it is now.
func (f *fixture) cover(scope ...store.NotificationSelector) Covered {
	f.t.Helper()
	ctx := context.Background()
	subject, err := f.st.LoadSubject(ctx, f.identity.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	got, err := Coverage(ctx, f.st, f.st, store.Review{Scope: scope}, subject)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// estate adds the account, the clusters and the service the fixture lacks.
func (f *fixture) estate() {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.st.CreateAccount(ctx, store.AWSAccount{Name: "Production", AccountID: checkoutKey.AccountID,
		RoleARN: "arn:aws:iam::" + checkoutKey.AccountID + ":role/x", ExternalID: "e", Regions: []string{"us-east-1", "eu-west-1"}, Enabled: true}); err != nil {
		f.t.Fatal(err)
	}
	f.services(batchKey)
	var clusters []model.Cluster
	for _, k := range []model.ServiceKey{checkoutKey, ledgerKey, batchKey} {
		clusters = append(clusters, model.Cluster{Key: k.ClusterKey, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(f.st).UpsertClusters(ctx, clusters); err != nil {
		f.t.Fatal(err)
	}
}

func clusterEntry(k model.ServiceKey) store.NotificationSelector {
	return store.NotificationSelector{Kind: "cluster", AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster}
}

func TestCoverage_EmptyScopeIsEverythingTheIdentitySees(t *testing.T) {
	f := newFixture(t)
	f.estate()
	got := f.cover()
	if got.Services != 3 || len(got.Entries) != 0 || got.Label != "Everything the agent can see" || !slices.Equal(got.Tags, []int64{int64(f.dev)}) {
		t.Errorf("covered = %+v", got)
	}
	f.tagService(batchKey, f.tag("finance"))
	if got = f.cover(); got.Services != 2 {
		t.Errorf("services = %d, want the two the identity sees", got.Services)
	}
}

func TestCoverage_TagClusterAndAccountEntries(t *testing.T) {
	f := newFixture(t)
	f.estate()
	payments := f.tag("payments")
	f.grant(f.dev, payments)
	f.tagService(checkoutKey, payments)
	tagEntry := agentclient.ScopeEntry{Kind: "tag", TagID: fmt.Sprint(payments), Name: "payments"}
	ledgerCluster := agentclient.ScopeEntry{Kind: "cluster", ClusterID: ledgerKey.ClusterKey.ID(), Name: "ledger-prod"}
	for name, c := range map[string]struct {
		scope    []store.NotificationSelector
		services int
		entries  []agentclient.ScopeEntry
		label    string
	}{
		"a tag":       {[]store.NotificationSelector{{Kind: "harbormaster", TagID: payments}}, 1, []agentclient.ScopeEntry{tagEntry}, "Tag: payments"},
		"a cluster":   {[]store.NotificationSelector{clusterEntry(ledgerKey)}, 1, []agentclient.ScopeEntry{ledgerCluster}, "Cluster: ledger-prod"},
		"an account":  {[]store.NotificationSelector{{Kind: "account", AccountID: checkoutKey.AccountID}}, 2, []agentclient.ScopeEntry{{Kind: "account", AccountID: checkoutKey.AccountID, Name: "Production"}}, "Account: Production"},
		"one region":  {[]store.NotificationSelector{{Kind: "account", AccountID: checkoutKey.AccountID, Region: "eu-west-1"}}, 1, []agentclient.ScopeEntry{{Kind: "account", AccountID: checkoutKey.AccountID, Region: "eu-west-1", Name: "Production"}}, "Account: Production (eu-west-1)"},
		"no name yet": {[]store.NotificationSelector{{Kind: "account", AccountID: batchKey.AccountID}}, 1, []agentclient.ScopeEntry{{Kind: "account", AccountID: batchKey.AccountID}}, "Account: " + batchKey.AccountID + " (missing)"},
		"a service counts once": {[]store.NotificationSelector{{Kind: "harbormaster", TagID: payments}, clusterEntry(ledgerKey), {Kind: "account", AccountID: checkoutKey.AccountID}}, 2,
			[]agentclient.ScopeEntry{{Kind: "account", AccountID: checkoutKey.AccountID, Name: "Production"}, ledgerCluster, tagEntry},
			"Account: Production, Cluster: ledger-prod, Tag: payments"},
	} {
		t.Run(name, func(t *testing.T) {
			got := f.cover(c.scope...)
			if got.Services != c.services || !slices.Equal(got.Entries, c.entries) || got.Label != c.label {
				t.Errorf("covered = %+v", got)
			}
		})
	}
	// A cluster of the same name elsewhere is another one.
	elsewhere := clusterEntry(ledgerKey)
	elsewhere.Region = "us-east-1"
	if got := f.cover(elsewhere); got.Services != 0 {
		t.Errorf("covered = %+v", got)
	}
}

// An entry that holds nothing the identity sees is not named to the model nor in the label.
func TestCoverage_DropsWhatTheIdentityCannotView(t *testing.T) {
	f := newFixture(t)
	f.estate()
	finance := f.tag("finance")
	f.tagService(batchKey, finance)
	f.tagService(ledgerKey, finance)
	got := f.cover(clusterEntry(checkoutKey), clusterEntry(batchKey), store.NotificationSelector{Kind: "harbormaster", TagID: finance})
	want := []agentclient.ScopeEntry{{Kind: "cluster", ClusterID: checkoutKey.ClusterKey.ID(), Name: "payments-prod"}}
	if got.Services != 1 || !slices.Equal(got.Entries, want) || got.Label != "Cluster: payments-prod, 2 entries the agent cannot see" || slices.Contains(got.Tags, int64(finance)) {
		t.Errorf("covered = %+v", got)
	}
	if got = f.cover(clusterEntry(batchKey)); got.Services != 0 || len(got.Entries) != 0 || got.Label != "1 entry the agent cannot see" {
		t.Errorf("covered = %+v", got)
	}
	// The subject alone decides: none, and nothing is covered.
	none, err := Coverage(context.Background(), f.st, f.st, store.Review{}, authz.Subject{})
	if err != nil || none.Services != 0 || len(none.Tags) != 0 {
		t.Errorf("covered = %+v, %v", none, err)
	}
}

func TestCoverage_DeletedTagMatchesNothing(t *testing.T) {
	f := newFixture(t)
	f.estate()
	payments := f.tag("payments")
	f.grant(f.dev, payments)
	f.tagService(checkoutKey, payments)
	if err := f.st.DeleteTag(context.Background(), payments); err != nil {
		t.Fatal(err)
	}
	gone := store.NotificationSelector{Kind: "harbormaster", TagID: payments}
	if got := f.cover(gone); got.Services != 0 || len(got.Entries) != 0 || slices.Contains(got.Tags, int64(payments)) {
		t.Errorf("covered = %+v", got)
	}
	got := f.cover(gone, clusterEntry(ledgerKey))
	if got.Services != 1 || len(got.Entries) != 1 || got.Entries[0].Kind != "cluster" {
		t.Errorf("covered = %+v", got)
	}
}

func TestCoverage_LabelIsCutAndNamesMissingEntries(t *testing.T) {
	f := newFixture(t)
	f.estate()
	payments := f.tag("payments")
	if err := f.st.DeleteTag(context.Background(), payments); err != nil {
		t.Fatal(err)
	}
	gone := clusterEntry(checkoutKey)
	gone.Cluster = "retired-prod"
	got := f.cover(clusterEntry(checkoutKey), gone, store.NotificationSelector{Kind: "harbormaster", TagID: payments})
	if want := fmt.Sprintf("Cluster: payments-prod, Cluster: retired-prod (missing), Tag: %d (missing)", payments); got.Label != want || len(got.Entries) != 1 {
		t.Errorf("label = %q, want %q; entries %+v", got.Label, want, got.Entries)
	}

	f.exec(`update aws_accounts set name = $1`, strings.Repeat("é", 400)+"\u202e\nforged")
	got = f.cover(store.NotificationSelector{Kind: "account", AccountID: checkoutKey.AccountID})
	if n := utf8.RuneCountInString(got.Label); n != 300 || !strings.HasSuffix(got.Label, "…") || !utf8.ValidString(got.Label) {
		t.Errorf("label of %d characters: %q", n, got.Label)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if name := got.Entries[0].Name; len(name) > 255 || len(name) < 250 || !utf8.ValidString(name) {
		t.Errorf("name of %d bytes: %q", len(name), name)
	}
	f.exec(`update aws_accounts set name = $1`, "Prod\u202e\nforged")
	if got = f.cover(store.NotificationSelector{Kind: "account", AccountID: checkoutKey.AccountID}); got.Label != "Account: Prod forged" || got.Entries[0].Name != "Prod forged" {
		t.Errorf("label %q, name %q", got.Label, got.Entries[0].Name)
	}
}

// A failed lookup is an error, never "nothing to see".
func TestCoverage_AFailedLookupIsAnError(t *testing.T) {
	f := newFixture(t)
	f.exec(`alter table tags rename to tags_gone`)
	if got, err := Coverage(context.Background(), f.st, f.st, store.Review{}, authz.Subject{}); err == nil {
		t.Errorf("covered = %+v", got)
	}
}
