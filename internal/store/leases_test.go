package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const leaseTTL = 30 * time.Second

func sliceKey(account, region string) store.LeaseKey {
	return store.LeaseKey{Kind: store.LeaseSlice, AccountID: account, Region: region}
}

// ensureAccounts creates the account rows of the targets: a slice lease exists only for a connected account.
func ensureAccounts(t testing.TB, s *store.Store, targets ...awsx.Target) {
	t.Helper()
	for _, tg := range targets {
		if _, err := s.GetAccountByAccountID(context.Background(), tg.AccountID); err == nil {
			continue
		}
		if _, err := s.CreateAccount(context.Background(), store.AWSAccount{Name: "t" + tg.AccountID, AccountID: tg.AccountID,
			RoleARN: "arn:aws:iam::" + tg.AccountID + ":role/x", ExternalID: "e", Regions: []string{tg.Region}, Enabled: true}); err != nil {
			t.Fatalf("create account %s: %v", tg.AccountID, err)
		}
	}
}

// ensureSlice creates the lease row of one slice.
func ensureSlice(t testing.TB, s *store.Store, account, region string) store.LeaseKey {
	t.Helper()
	ensureAccounts(t, s, awsx.Target{AccountID: account, Region: region})
	if err := s.EnsureSliceLeases(context.Background(), []awsx.Target{{AccountID: account, Region: region}}); err != nil {
		t.Fatalf("ensure slice leases: %v", err)
	}
	return sliceKey(account, region)
}

// expireLease makes a held lease expired in the database, as if its owner had stopped renewing 1 s ago.
func expireLease(t testing.TB, s *store.Store, k store.LeaseKey) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(),
		`update poll_leases set expires_at = now() - interval '1 second' where kind = $1 and account_id = $2 and region = $3`,
		k.Kind, k.AccountID, k.Region)
	if err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

func mustClaim(t testing.TB, s *store.Store, k store.LeaseKey, owner uuid.UUID) store.Lease {
	t.Helper()
	l, ok, err := s.ClaimLease(context.Background(), k, owner, leaseTTL)
	if err != nil || !ok {
		t.Fatalf("claim %v as %s = ok %v, err %v; want a claim", k, owner, ok, err)
	}
	return l
}

func TestClaimLease_FreeRowIsClaimedWithEpochPlusOne(t *testing.T) {
	s := storetest.Open(t)
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	owner := uuid.New()

	l, ok, err := s.ClaimLease(context.Background(), k, owner, leaseTTL)
	if err != nil || !ok {
		t.Fatalf("ClaimLease = ok %v, err %v; want a claim", ok, err)
	}
	if l.Epoch != 1 || l.OwnerID != owner || l.Key != k {
		t.Fatalf("lease = %+v, want epoch 1 owned by %s for %v", l, owner, k)
	}
	if rem := time.Until(l.ExpiresAt); rem < leaseTTL-5*time.Second || rem > leaseTTL+5*time.Second {
		t.Fatalf("expires in %v, want about %v", rem, leaseTTL)
	}
}

func TestClaimLease_MissingRowIsNotClaimed(t *testing.T) {
	s := storetest.Open(t)
	_, ok, err := s.ClaimLease(context.Background(), sliceKey("111111111111", "us-east-1"), uuid.New(), leaseTTL)
	if err != nil || ok {
		t.Fatalf("ClaimLease on a missing row = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestClaimLease_LiveOwnerIsNotTakenOver(t *testing.T) {
	s := storetest.Open(t)
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	first := mustClaim(t, s, k, uuid.New())

	_, ok, err := s.ClaimLease(context.Background(), k, uuid.New(), leaseTTL)
	if err != nil || ok {
		t.Fatalf("second claim = ok %v, err %v; want false, nil", ok, err)
	}
	if err := s.CheckLease(context.Background(), first, 0); err != nil {
		t.Fatalf("first owner lost its lease: %v", err)
	}
}

func TestClaimLease_ExpiredOwnerIsTakenOverAndEpochRises(t *testing.T) {
	s := storetest.Open(t)
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	first := mustClaim(t, s, k, uuid.New())
	expireLease(t, s, k)

	second := mustClaim(t, s, k, uuid.New())
	if second.Epoch != first.Epoch+1 {
		t.Fatalf("epoch = %d, want %d", second.Epoch, first.Epoch+1)
	}
	if err := s.CheckLease(context.Background(), first, 0); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("old owner's CheckLease = %v, want ErrLeaseLost", err)
	}
}

func TestClaimLease_ConcurrentClaimsYieldOneOwner(t *testing.T) {
	s := storetest.Open(t)
	k := ensureSlice(t, s, "111111111111", "us-east-1")

	const claimers = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	winners := make(chan store.Lease, claimers)
	for range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, ok, err := s.ClaimLease(context.Background(), k, uuid.New(), leaseTTL)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				winners <- l
			}
		}()
	}
	close(start)
	wg.Wait()
	close(winners)
	var won []store.Lease
	for l := range winners {
		won = append(won, l)
	}
	if len(won) != 1 {
		t.Fatalf("%d claimers won the lease, want exactly 1: %+v", len(won), won)
	}
	if won[0].Epoch != 1 {
		t.Fatalf("epoch = %d, want 1 (a lost claim must not raise it)", won[0].Epoch)
	}
}

func TestRenewLease_ExtendsAndRejectsStaleEpoch(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	a := mustClaim(t, s, k, uuid.New())

	if err := s.RenewLease(ctx, a, 2*leaseTTL); err != nil {
		t.Fatalf("renew by owner: %v", err)
	}
	rows, err := s.ListLeases(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListLeases = %v, %v", rows, err)
	}
	if rem := time.Until(rows[0].ExpiresAt); rem < leaseTTL+10*time.Second {
		t.Fatalf("renewal left %v, want about %v", rem, 2*leaseTTL)
	}
	if rows[0].RenewedAt == nil {
		t.Fatal("renewed_at not set")
	}

	expireLease(t, s, k)
	b := mustClaim(t, s, k, uuid.New())
	if err := s.RenewLease(ctx, a, leaseTTL); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("stale owner renew = %v, want ErrLeaseLost", err)
	}
	if err := s.RenewLease(ctx, b, leaseTTL); err != nil {
		t.Fatalf("new owner renew: %v", err)
	}
	// Same owner, older epoch: also lost (the epoch is what fences, not the owner id).
	stale := b
	stale.Epoch = b.Epoch - 1
	if err := s.RenewLease(ctx, stale, leaseTTL); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("old epoch renew = %v, want ErrLeaseLost", err)
	}
}

func TestReleaseLease_FreesAndIsSafeWhenAlreadyLost(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	a := mustClaim(t, s, k, uuid.New())

	if err := s.ReleaseLease(ctx, a); err != nil {
		t.Fatalf("release: %v", err)
	}
	rows, _ := s.ListLeases(ctx)
	if len(rows) != 1 || !rows[0].Free || rows[0].OwnerID != uuid.Nil || rows[0].Epoch != a.Epoch {
		t.Fatalf("after release rows = %+v, want one free row with the epoch unchanged", rows)
	}
	if err := s.ReleaseLease(ctx, a); err != nil {
		t.Fatalf("release of an already released lease = %v, want nil", err)
	}

	b := mustClaim(t, s, k, uuid.New())
	if b.Epoch != a.Epoch+1 {
		t.Fatalf("epoch after release and claim = %d, want %d", b.Epoch, a.Epoch+1)
	}
	// A stale owner's release must not free the new owner's lease.
	if err := s.ReleaseLease(ctx, a); err != nil {
		t.Fatalf("stale release = %v, want nil", err)
	}
	if err := s.CheckLease(ctx, b, 0); err != nil {
		t.Fatalf("new owner lost its lease to a stale release: %v", err)
	}
}

func TestCheckLease_NeedsRemainingTime(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	k := ensureSlice(t, s, "111111111111", "us-east-1")
	l, ok, err := s.ClaimLease(ctx, k, uuid.New(), 4*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok %v err %v", ok, err)
	}
	if err := s.CheckLease(ctx, l, 0); err != nil {
		t.Fatalf("CheckLease(0) = %v, want nil", err)
	}
	if err := s.CheckLease(ctx, l, time.Second); err != nil {
		t.Fatalf("CheckLease(1s) with 4s left = %v, want nil", err)
	}
	if err := s.CheckLease(ctx, l, 5*time.Second); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("CheckLease(5s) with 4s left = %v, want ErrLeaseLost", err)
	}
}

func TestEnsureSliceLeases_AddsMissingAndDropsOnlyFreeVanished(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a := awsx.Target{AccountID: "111111111111", Region: "us-east-1"}
	b := awsx.Target{AccountID: "111111111111", Region: "eu-west-1"}
	c := awsx.Target{AccountID: "222222222222", Region: "us-east-1"}
	ensureAccounts(t, s, a, b, c)

	if err := s.EnsureSliceLeases(ctx, []awsx.Target{a, b, c}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{a, b, c}); err != nil {
		t.Fatalf("second ensure must be idempotent: %v", err)
	}
	if rows, _ := s.ListLeases(ctx); len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}

	held := mustClaim(t, s, sliceKey(c.AccountID, c.Region), uuid.New())
	// b and c vanish; b is free so it goes, c is held so it stays.
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{a}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListLeases(ctx)
	got := leaseKeys(rows)
	want := []string{"slice|111111111111|us-east-1", "slice|222222222222|us-east-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows after ensure = %v, want %v (held row of a removed target stays)", got, want)
	}

	// Once released it is free and the next ensure drops it.
	if err := s.ReleaseLease(ctx, held); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{a}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListLeases(ctx)
	if got := leaseKeys(rows); len(got) != 1 || got[0] != "slice|111111111111|us-east-1" {
		t.Fatalf("rows after release and ensure = %v", got)
	}
}

func TestEnsureSliceLease_AddsOnlyThatRowAndDropsNothing(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	other := awsx.Target{AccountID: "222222222222", Region: "us-east-1"}
	ensureAccounts(t, s, other)
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{other}); err != nil {
		t.Fatal(err)
	}
	one := awsx.Target{AccountID: "111111111111", Region: "eu-west-1"}
	for range 2 { // idempotent
		if err := s.EnsureSliceLease(ctx, one); err != nil {
			t.Fatalf("EnsureSliceLease: %v", err)
		}
	}
	rows, _ := s.ListLeases(ctx)
	want := []string{"slice|111111111111|eu-west-1", "slice|222222222222|us-east-1"}
	if got := leaseKeys(rows); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v (the other free row must stay)", got, want)
	}
}

func TestEnsureSliceLeases_NeverTouchesTheCoordinatorRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.EnsureCoordinatorLease(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCoordinatorLease(ctx); err != nil {
		t.Fatalf("second ensure must be idempotent: %v", err)
	}
	if err := s.EnsureSliceLeases(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListLeases(ctx)
	if got := leaseKeys(rows); len(got) != 1 || got[0] != "coordinator||" {
		t.Fatalf("rows = %v, want only the coordinator row", got)
	}
	l := mustClaim(t, s, store.LeaseKey{Kind: store.LeaseCoordinator}, uuid.New())
	if l.Epoch != 1 {
		t.Fatalf("coordinator epoch = %d, want 1", l.Epoch)
	}
}

func leaseKeys(rows []store.LeaseRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Key.Kind+"|"+r.Key.AccountID+"|"+r.Key.Region)
	}
	sort.Strings(out)
	return out
}

func TestSliceWeights_CountsClusters(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	clusters := []model.Cluster{
		{Key: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "a"}},
		{Key: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "b"}},
		{Key: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"}},
	}
	if _, err := s.UpsertClusters(ctx, clusters); err != nil {
		t.Fatalf("seed clusters: %v", err)
	}
	got, err := s.SliceWeights(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[awsx.Target]int{
		{AccountID: "111111111111", Region: "us-east-1"}: 2,
		{AccountID: "111111111111", Region: "eu-west-1"}: 1,
	}
	if len(got) != len(want) {
		t.Fatalf("weights = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("weight %v = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// tableColumns lists "table.column type" for every column of the test schema.
func tableColumns(t testing.TB, s *store.Store) []string {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(),
		`select table_name || '.' || column_name || ' ' || data_type from information_schema.columns
		 where table_schema = current_schema() order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestMigration0212_IsAdditive(t *testing.T) {
	s := storetest.OpenEmpty(t)
	ctx := context.Background()
	files, err := filepath.Glob("migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob migrations: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	var lease string
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(filepath.Base(f), "0212_") {
			lease = string(body)
			continue
		}
		if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if lease == "" {
		t.Fatal("migration 0212 is missing")
	}
	before := tableColumns(t, s)
	if _, err := s.Pool.Exec(ctx, lease); err != nil {
		t.Fatalf("apply 0212: %v", err)
	}
	after := tableColumns(t, s)

	seen := map[string]bool{}
	for _, c := range after {
		seen[c] = true
	}
	for _, c := range before {
		if !seen[c] {
			t.Errorf("0212 removed or changed column %q", c)
		}
	}
	existing := map[string]bool{}
	for _, c := range before {
		existing[c] = true
	}
	added := map[string]bool{}
	for _, c := range after {
		if !existing[c] {
			added[strings.SplitN(c, ".", 2)[0]] = true
		}
	}
	if len(added) != 2 || !added["poll_leases"] || !added["poll_members"] {
		t.Errorf("0212 touched tables %v, want exactly poll_leases and poll_members", added)
	}
}

// A stale target list must not bring a slice back after its account was disconnected, and disconnecting removes the
// slice's lease row so its owner's later writes fail the fence.
func TestEnsureSliceLeases_SkipsAnAccountThatWasDisconnected(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	gone := awsx.Target{AccountID: "111111111111", Region: "us-east-1"}
	kept := awsx.Target{AccountID: "222222222222", Region: "us-east-1"}
	ensureAccounts(t, s, gone, kept)
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{gone, kept}); err != nil {
		t.Fatal(err)
	}
	owner := mustClaim(t, s, sliceKey(gone.AccountID, gone.Region), uuid.New())
	account, err := s.GetAccountByAccountID(ctx, gone.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.ListLeases(ctx); strings.Join(leaseKeys(rows), ",") != "slice|222222222222|us-east-1" {
		t.Fatalf("lease rows after disconnecting = %v, want only the other account's", leaseKeys(rows))
	}
	if err := s.RenewLease(ctx, owner, leaseTTL); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("renewing the deleted account's lease = %v, want ErrLeaseLost", err)
	}
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{gone, kept}); err != nil { // the stale list
		t.Fatal(err)
	}
	if rows, _ := s.ListLeases(ctx); strings.Join(leaseKeys(rows), ",") != "slice|222222222222|us-east-1" {
		t.Fatalf("lease rows after a stale ensure = %v, want only the other account's", leaseKeys(rows))
	}
}
