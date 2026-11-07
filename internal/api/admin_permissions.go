package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type permissionTagDTO struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Color            string `json:"color"`
	ServicesInEffect int    `json:"services_in_effect"`
}

type permissionUserDTO struct {
	ID       int64          `json:"id"`
	Username string         `json:"username"`
	Admin    bool           `json:"admin"`
	Perms    map[string]int `json:"perms"`
}

// getPermissions returns the matrix: tags (Dev first, then by name) and users by username.
// Admins carry empty perms because they hold every action on every tag.
func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.Store.TagCounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	perms, err := s.Store.AllPermissions(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Builtin != tags[j].Builtin {
			return tags[i].Builtin
		}
		return tags[i].Name < tags[j].Name
	})
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })

	out := PermissionsResponse{Tags: []permissionTagDTO{}, Users: []permissionUserDTO{}}
	for _, t := range tags {
		out.Tags = append(out.Tags, permissionTagDTO{ID: int64(t.ID), Name: t.Name, Color: t.Color, ServicesInEffect: counts[t.ID].ServicesEffective})
	}
	for _, u := range users {
		dto := permissionUserDTO{ID: u.ID, Username: u.Username, Admin: u.Admin, Perms: map[string]int{}}
		if !u.Admin {
			for tag, set := range perms[u.ID] {
				dto.Perms[strconv.FormatInt(int64(tag), 10)] = int(set)
			}
		}
		out.Users = append(out.Users, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// actionNames lists the display names of the actions in set, in ActionNames order.
func actionNames(set authz.ActionSet) []string {
	names := []string{}
	for _, a := range authz.ActionNames() {
		if set.Has(a.Action) {
			names = append(names, a.Name)
		}
	}
	return names
}

func (s *Server) setPermission(w http.ResponseWriter, r *http.Request) {
	userID, ok1 := pathID(r, "userId")
	tagID, ok2 := pathID(r, "tagId")
	if !ok1 || !ok2 {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body struct {
		Actions int `json:"actions"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	user, err := s.Store.GetUser(ctx, userID)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	var tag *store.Tag
	for i := range tags {
		if int64(tags[i].ID) == tagID {
			tag = &tags[i]
		}
	}
	if tag == nil {
		s.fail(w, store.ErrNotFound)
		return
	}
	if user.Admin {
		s.fail(w, invalid("Global admins have every permission."))
		return
	}
	if body.Actions < 0 || body.Actions > allActions {
		s.fail(w, invalid("Actions must be between 0 and "+strconv.Itoa(allActions)+"."))
		return
	}
	updated := authz.Normalize(authz.ActionSet(body.Actions))
	current, err := s.Store.PermissionsOf(ctx, userID)
	if err != nil {
		s.fail(w, err)
		return
	}
	previous := authz.Normalize(current[tag.ID])
	if previous != updated {
		if err := s.Store.SetPermission(ctx, userID, tag.ID, updated); err != nil {
			s.fail(w, err)
			return
		}
		added, removed := updated&^previous, previous&^updated
		var lines []string
		if added != 0 {
			lines = append(lines, "Gave "+user.Username+" "+strings.Join(actionNames(added), ", ")+" on "+tag.Name+".")
		}
		if removed != 0 {
			lines = append(lines, "Removed "+strings.Join(actionNames(removed), ", ")+" on "+tag.Name+" from "+user.Username+".")
		}
		s.auditAdmin(r, "permission_set", "user:"+user.Username, strings.Join(lines, " "), map[string]any{
			"tag": tag.Name, "added": actionNames(added), "removed": actionNames(removed), "preset": authz.PresetName(updated),
		})
	}
	writeJSON(w, http.StatusOK, map[string]int{"actions": int(updated)})
}
