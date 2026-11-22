package awsx

import (
	"context"
	"errors"
	"testing"
)

type fixedRoles struct {
	arn, external string
	err           error
}

func (f fixedRoles) Role(context.Context, string) (string, string, error) {
	return f.arn, f.external, f.err
}

func TestSuffixRoles_AppendsToTheRoleName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"arn:aws:iam::123456789012:role/harbormaster-access", "arn:aws:iam::123456789012:role/harbormaster-access-poller"},
		{"arn:aws:iam::123456789012:role/platform/tools/harbormaster-access", "arn:aws:iam::123456789012:role/platform/tools/harbormaster-access-poller"},
		{"arn:aws-us-gov:iam::123456789012:role/hm", "arn:aws-us-gov:iam::123456789012:role/hm-poller"},
		{"arn:aws-cn:iam::123456789012:role/a/b", "arn:aws-cn:iam::123456789012:role/a/b-poller"},
	} {
		src := SuffixRoles(fixedRoles{arn: tc.in, external: "ext-1"}, "-poller")
		got, ext, err := src.Role(context.Background(), "123456789012")
		if err != nil || got != tc.want || ext != "ext-1" {
			t.Errorf("%s: got %q, %q, %v; want %q with the external id passed through", tc.in, got, ext, err, tc.want)
		}
	}
}

func TestSuffixRoles_EmptySuffixIsIdentity(t *testing.T) {
	inner := fixedRoles{arn: "arn:aws:iam::123456789012:role/x"}
	got := SuffixRoles(inner, "")
	if got != RoleSource(inner) {
		t.Fatalf("an empty suffix wrapped the source: %T", got)
	}
}

func TestSuffixRoles_RejectsNonRoleARN(t *testing.T) {
	for _, in := range []string{
		"", "not-an-arn", "arn:aws:iam::123456789012:user/sam", "arn:aws:s3:::bucket/role/x",
		"arn:aws:iam::123456789012:role/", "arn:aws:iam::123456789012:role/path/",
		"arn:aws:iam:::role/harbormaster-access", // no account
	} {
		src := SuffixRoles(fixedRoles{arn: in}, "-poller")
		got, _, err := src.Role(context.Background(), "123456789012")
		if !errors.Is(err, ErrBadRoleARN) || got != "" {
			t.Errorf("%q: got %q, %v; want ErrBadRoleARN and no guessed value", in, got, err)
		}
	}
}

func TestSuffixRoles_PassesInnerErrorsThrough(t *testing.T) {
	want := ErrAccountUnknown
	src := SuffixRoles(fixedRoles{err: want}, "-coordinator")
	if _, _, err := src.Role(context.Background(), "1"); !errors.Is(err, ErrAccountUnknown) {
		t.Fatalf("err = %v, want ErrAccountUnknown preserved", err)
	}
}
