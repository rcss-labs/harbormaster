package main

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
)

type storedRole struct{}

func (storedRole) Role(context.Context, string) (string, string, error) {
	return "arn:aws:iam::123456789012:role/harbormaster-access", "ext", nil
}

func TestSuffixRoles_AllAndWebUseTheStoredARN(t *testing.T) {
	for _, tc := range []struct {
		role config.Role
		want string
	}{
		{config.RoleAll, "arn:aws:iam::123456789012:role/harbormaster-access"},
		{config.RoleWeb, "arn:aws:iam::123456789012:role/harbormaster-access"},
		{config.RolePoller, "arn:aws:iam::123456789012:role/harbormaster-access-poller"},
		{config.RoleCoordinator, "arn:aws:iam::123456789012:role/harbormaster-access-coordinator"},
	} {
		var src awsx.RoleSource = accessRoles(tc.role, storedRole{})
		got, ext, err := src.Role(context.Background(), "123456789012")
		if err != nil || got != tc.want || ext != "ext" {
			t.Errorf("%s: got %q, %q, %v; want %q", tc.role, got, ext, err, tc.want)
		}
	}
}
