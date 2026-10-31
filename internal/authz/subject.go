package authz

// Subject is the authenticated user with their per-tag permissions.
type Subject struct {
	UserID   int64
	Username string
	Admin    bool
	Perms    map[TagID]ActionSet
}

// Can reports whether s may perform a on resources carrying tag.
// Admins may do anything; others need View on the tag plus the action itself.
func Can(s Subject, a Action, tag TagID) bool {
	if s.Admin {
		return true
	}
	set := s.Perms[tag]
	return set.Has(View) && set.Has(a)
}

// PermissionsOf returns the API `can` block for a tag.
func PermissionsOf(s Subject, tag TagID) map[string]bool {
	return map[string]bool{
		"view":     Can(s, View, tag),
		"logs":     Can(s, Logs, tag),
		"operate":  Can(s, Operate, tag),
		"deploy":   Can(s, Deploy, tag),
		"edit":     Can(s, Edit, tag),
		"terminal": Can(s, Terminal, tag),
	}
}
