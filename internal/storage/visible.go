package storage

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Visible returns the file systems the subject may see, sorted by name. Admins
// see all; others only those mounted by at least one (visible) service.
func Visible(subj authz.Subject, fs []model.FileSystem, index map[string][]MountedBy) []model.FileSystem {
	out := []model.FileSystem{}
	for _, f := range fs {
		if subj.Admin || len(index[f.FileSystemID]) > 0 {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
