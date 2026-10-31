package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func newTestAccount(accountID string) store.AWSAccount {
	return store.AWSAccount{
		Name:       "prod-" + accountID,
		AccountID:  accountID,
		RoleARN:    "arn:aws:iam::" + accountID + ":role/harbormaster",
		ExternalID: "external-" + accountID,
		Regions:    []string{"us-east-1"},
		Enabled:    true,
	}
}

func TestCreateAccount_DuplicateAccountIDConflicts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	account := newTestAccount("482109375512")

	if _, err := s.CreateAccount(ctx, account); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.CreateAccount(ctx, account); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second create error = %v, want ErrConflict", err)
	}
}

func TestDeleteAccount_RemovesSnapshots(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	created, err := s.CreateAccount(ctx, newTestAccount("482109375512"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Pool.Exec(ctx,
		`insert into clusters (account_id, region, cluster_name, content_hash, raw, polled_at)
		 values ($1, 'us-east-1', 'core-services', 'hash', '{}', now())`, created.AccountID); err != nil {
		t.Fatalf("insert cluster: %v", err)
	}

	if err := s.DeleteAccount(ctx, created.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	var clusters int
	if err := s.Pool.QueryRow(ctx, `select count(*) from clusters where account_id = $1`, created.AccountID).Scan(&clusters); err != nil {
		t.Fatal(err)
	}
	if clusters != 0 {
		t.Fatalf("clusters left after account delete = %d, want 0", clusters)
	}
	if _, err := s.GetAccount(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAccount after delete error = %v, want ErrNotFound", err)
	}
}
