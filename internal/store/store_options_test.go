package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestOpenWithOptions_AppliesMaxConns(t *testing.T) {
	url := os.Getenv("HM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	s, err := store.OpenWithOptions(context.Background(), url, store.OpenOptions{MaxConns: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Pool.Config().MaxConns; got != 7 {
		t.Errorf("MaxConns = %d, want 7", got)
	}
	d, err := store.OpenWithOptions(context.Background(), url, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Pool.Config().MaxConns; got <= 0 {
		t.Errorf("zero MaxConns left the pool at %d, want pgx's default (positive)", got)
	}
}
