package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestRecordRevision_RoundTrip(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	sam, err := s.CreateUser(ctx, "sam", "hash", false, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	registered := "arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:43"
	previous := "arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:42"

	if err := s.RecordRevision(ctx, registered, sam.ID, time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("record revision: %v", err)
	}

	registrars, err := s.RevisionRegistrars(ctx, []string{registered, previous})
	if err != nil {
		t.Fatalf("registrars: %v", err)
	}
	if len(registrars) != 1 || registrars[registered] != "sam" {
		t.Fatalf("registrars = %v, want {%s: sam}", registrars, registered)
	}
}

func TestRecordRevision_SameARNTwiceKeepsFirst(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	sam, err := s.CreateUser(ctx, "sam", "hash", false, false)
	if err != nil {
		t.Fatalf("create sam: %v", err)
	}
	dana, err := s.CreateUser(ctx, "dana", "hash", false, false)
	if err != nil {
		t.Fatalf("create dana: %v", err)
	}
	arn := "arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:43"
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	if err := s.RecordRevision(ctx, arn, sam.ID, at); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := s.RecordRevision(ctx, arn, dana.ID, at.Add(time.Minute)); err != nil {
		t.Fatalf("second record: %v", err)
	}

	registrars, err := s.RevisionRegistrars(ctx, []string{arn})
	if err != nil {
		t.Fatalf("registrars: %v", err)
	}
	if registrars[arn] != "sam" {
		t.Fatalf("registrar = %q, want sam", registrars[arn])
	}
}
