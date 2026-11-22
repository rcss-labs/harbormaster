package authz

// Subject is the authenticated user with their per-tag permissions.
type Subject struct {
	UserID   int64
	Username string
	Admin    bool
	Perms    map[TagID]ActionSet
	// ReadOnly is set for every request authenticated by an access token. Such a subject may only View and read
	// Logs, whatever its permissions, admins included. It is the second, independent read-only layer behind the
	// token route allowlist.
	ReadOnly bool
}

// Can reports whether s may perform a on resources carrying tag.
// A read-only subject may do nothing but View and Logs; this is checked before the admin shortcut.
// Admins may do anything else; others need View on the tag plus the action itself.
func Can(s Subject, a Action, tag TagID) bool {
	if s.ReadOnly && a != View && a != Logs {
		return false
	}
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
