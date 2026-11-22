package authz

import "sort"

// VisibleTags returns, in ascending order, the tags on which s has View.
// Admins see everything; callers must check Subject.Admin first.
func VisibleTags(s Subject) []TagID {
	var ids []TagID
	for id, set := range s.Perms {
		if set.Has(View) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
