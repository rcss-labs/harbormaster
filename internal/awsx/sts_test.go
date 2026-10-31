package awsx

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type stubSTS struct{ calls atomic.Int32 }

func (s *stubSTS) AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	s.calls.Add(1)
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String("AKIATEST"),
		SecretAccessKey: aws.String("secret"),
		SessionToken:    aws.String("token"),
		Expiration:      aws.Time(time.Now().Add(time.Hour)),
	}}, nil
}

type stubRoles struct{ err error }

func (s stubRoles) Role(_ context.Context, accountID string) (string, string, error) {
	if s.err != nil {
		return "", "", s.err
	}
	return "arn:aws:iam::" + accountID + ":role/hm", "ext-" + accountID, nil
}

func newTestReal(stub *stubSTS, roles RoleSource) *Real {
	r := NewReal(aws.Config{Region: "us-east-1"}, roles)
	r.newSTS = func(aws.Config) stscreds.AssumeRoleAPIClient { return stub }
	return r
}

func TestReal_CachesCredentialsPerAccount(t *testing.T) {
	ctx := context.Background()
	stub := &stubSTS{}
	r := newTestReal(stub, stubRoles{})
	a := Target{AccountID: "111111111111", Region: "us-east-1"}

	for i := 0; i < 2; i++ {
		if _, err := r.ECS(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("same account: want 1 AssumeRole, got %d", got)
	}
	if _, err := r.ECS(ctx, Target{AccountID: "222222222222", Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Fatalf("different account: want 2, got %d", got)
	}
	before, _ := r.ECS(ctx, a)
	r.Invalidate(a.AccountID)
	after, err := r.ECS(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("Invalidate must drop the cached client")
	}
	if got := stub.calls.Load(); got != 3 {
		t.Fatalf("after Invalidate: want 3, got %d", got)
	}
}

func TestReal_CredentialsSharesCache(t *testing.T) {
	ctx := context.Background()
	stub := &stubSTS{}
	r := newTestReal(stub, stubRoles{})
	tgt := Target{AccountID: "111111111111", Region: "eu-west-1"}

	p, err := r.Credentials(ctx, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Retrieve(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ECS(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("want 1 AssumeRole, got %d", got)
	}
}

func TestReal_UnknownAccount(t *testing.T) {
	stub := &stubSTS{}
	r := newTestReal(stub, stubRoles{err: fmt.Errorf("lookup: %w", ErrAccountUnknown)})
	_, err := r.ECS(context.Background(), Target{AccountID: "999999999999", Region: "us-east-1"})
	if !errors.Is(err, ErrAccountUnknown) {
		t.Fatalf("want ErrAccountUnknown, got %v", err)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Fatalf("want 0 AssumeRole, got %d", got)
	}
}

// invalidatingSTS calls Invalidate from inside its first AssumeRole, as if an
// account edit landed while credentials were being fetched.
type invalidatingSTS struct {
	stubSTS
	onFirst func()
}

func (s *invalidatingSTS) AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, o ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	if s.calls.Load() == 0 {
		s.onFirst()
	}
	return s.stubSTS.AssumeRole(ctx, in, o...)
}

func TestReal_InvalidateDuringAssumeRoleDiscardsStaleClient(t *testing.T) {
	ctx := context.Background()
	stub := &invalidatingSTS{}
	r := NewReal(aws.Config{Region: "us-east-1"}, stubRoles{})
	r.newSTS = func(aws.Config) stscreds.AssumeRoleAPIClient { return stub }
	tgt := Target{AccountID: "111111111111", Region: "us-east-1"}
	stub.onFirst = func() { r.Invalidate(tgt.AccountID) }

	first, err := r.ECS(ctx, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Fatalf("stale build must be redone: want 2 AssumeRole, got %d", got)
	}
	second, err := r.ECS(ctx, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || stub.calls.Load() != 2 {
		t.Fatalf("fresh client should be cached (same=%v, calls=%d)", first == second, stub.calls.Load())
	}
}
