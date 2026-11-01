package storage

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func visibleFixture() ([]model.FileSystem, map[string][]MountedBy) {
	fs := []model.FileSystem{
		{FileSystemID: "fs-3", Name: "uploads-archive"},
		{FileSystemID: "fs-2", Name: "shared-certs"},
		{FileSystemID: "fs-1", Name: "reports-data"},
	}
	idx := map[string][]MountedBy{"fs-1": {{ServiceName: "report-worker"}}, "fs-2": {{ServiceName: "edge-nginx"}}}
	return fs, idx
}

func names(fs []model.FileSystem) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestVisible_NonAdminSeesOnlyMounted(t *testing.T) {
	fs, idx := visibleFixture()
	got := names(Visible(authz.Subject{Username: "omer"}, fs, idx))
	if len(got) != 2 || got[0] != "reports-data" || got[1] != "shared-certs" {
		t.Fatalf("got %v; uploads-archive must be hidden", got)
	}
}

func TestVisible_AdminSeesNotMounted(t *testing.T) {
	fs, idx := visibleFixture()
	if got := names(Visible(authz.Subject{Admin: true}, fs, idx)); len(got) != 3 {
		t.Fatalf("got %v, want all 3", got)
	}
}

func TestVisible_SortedByName(t *testing.T) {
	fs, idx := visibleFixture()
	got := names(Visible(authz.Subject{Admin: true}, fs, idx))
	if got[0] != "reports-data" || got[1] != "shared-certs" || got[2] != "uploads-archive" {
		t.Fatalf("got %v", got)
	}
}
