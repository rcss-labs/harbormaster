package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

type testUserDTO struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	Admin              bool   `json:"admin"`
	MustChangePassword bool   `json:"must_change_password"`
	CanSee             []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"can_see"`
}

func lastAudit(t *testing.T, st *store.Store) store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(t.Context(), 1, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit: %v %v", entries, err)
	}
	return entries[0]
}

func userID(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	u, err := st.GetUserByUsername(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestUsers_NonAdminIs403(t *testing.T) {
	srv, _, _ := newTestServer(t)
	omer := login(t, srv, "omer", testPassword)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/users", nil},
		{"POST", "/api/users", map[string]any{"username": "x1", "admin": false}},
		{"DELETE", "/api/users/1", nil},
		{"POST", "/api/users/1/reset-password", nil},
		{"POST", "/api/users/1/admin", map[string]any{"admin": true}},
		{"GET", "/api/permissions", nil},
		{"PUT", "/api/permissions/1/1", map[string]any{"actions": 1}},
	} {
		resp := omer.do(tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestListUsers_CanSee(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedMockupTags(t, st)
	pavel := login(t, srv, "pavel", testPassword)
	var out struct {
		Items []testUserDTO `json:"items"`
	}
	decode(t, pavel.get("/api/users"), &out)
	byName := map[string]testUserDTO{}
	for _, u := range out.Items {
		byName[u.Username] = u
	}
	if len(byName["pavel"].CanSee) != 3 {
		t.Fatalf("admin can_see = %+v, want all 3 tags", byName["pavel"].CanSee)
	}
	oncall := byName["oncall"].CanSee
	if len(oncall) != 2 || oncall[0].Name != "Prod" || oncall[1].Name != "Staging" {
		t.Fatalf("oncall can_see = %+v, want Prod, Staging", oncall)
	}
}

func TestCreateUser_ValidationAndConflict(t *testing.T) {
	srv, _, st := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	for _, name := range []string{"a", "Bad", "1abc", "has space", "x" + string(make([]byte, 40))} {
		resp := pavel.post("/api/users", map[string]any{"username": name, "admin": false})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%q: status %d", name, resp.StatusCode)
		}
		if _, msg := errorCode(t, resp); msg != "Use 2–32 lowercase letters, digits, dots, hyphens or underscores." {
			t.Fatalf("%q: message %q", name, msg)
		}
	}
	resp := pavel.post("/api/users", map[string]any{"username": "gina.k", "admin": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var created struct {
		User              testUserDTO `json:"user"`
		TemporaryPassword string      `json:"temporary_password"`
	}
	decode(t, resp, &created)
	if created.User.Username != "gina.k" || !created.User.Admin || !created.User.MustChangePassword || created.TemporaryPassword == "" {
		t.Fatalf("created = %+v", created)
	}
	if a := lastAudit(t, st); a.Action != "user_create" || a.Target != "user:gina.k" || a.Summary != "Created user gina.k as global admin." {
		t.Fatalf("audit = %+v", a)
	}
	resp = pavel.post("/api/users", map[string]any{"username": "gina.k", "admin": false})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "gina.k already exists." {
		t.Fatalf("conflict message %q", msg)
	}
	// Case-insensitive duplicate of an existing user is also a conflict.
	resp = pavel.post("/api/users", map[string]any{"username": "omer", "admin": false})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("existing user status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = pavel.post("/api/users", map[string]any{"username": "plain", "admin": false})
	decode(t, resp, &created)
	if a := lastAudit(t, st); a.Summary != "Created user plain." {
		t.Fatalf("audit summary %q", a.Summary)
	}
}

func TestCreateUser_TemporaryPasswordForcesChange(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	var created struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	decode(t, pavel.post("/api/users", map[string]any{"username": "newbie", "admin": false}), &created)
	newbie := login(t, srv, "newbie", created.TemporaryPassword)
	var me MeDTO
	// /api/me is allowed while a password change is pending.
	decode(t, newbie.get("/api/me"), &me)
	if !me.MustChangePassword {
		t.Fatal("must_change_password should be true")
	}
}

func TestDeleteUser_SelfAndLastAdminRefused(t *testing.T) {
	srv, _, st := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	pavelID := userID(t, st, "pavel")
	resp := pavel.do("DELETE", fmt.Sprintf("/api/users/%d", pavelID), nil, nil)
	if resp.StatusCode != 422 {
		t.Fatalf("self delete %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "You cannot delete yourself." {
		t.Fatalf("msg %q", msg)
	}
	resp = pavel.do("DELETE", "/api/users/999999", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown delete %d", resp.StatusCode)
	}
	resp.Body.Close()

	omerID := userID(t, st, "omer")
	resp = pavel.do("DELETE", fmt.Sprintf("/api/users/%d", omerID), nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete omer %d", resp.StatusCode)
	}
	if a := lastAudit(t, st); a.Action != "user_delete" || a.Summary != "Deleted user omer." {
		t.Fatalf("audit %+v", a)
	}
	if _, err := st.GetUser(t.Context(), omerID); err == nil {
		t.Fatal("omer still exists")
	}
}

func TestSetAdmin_LastAdminRefused(t *testing.T) {
	srv, _, st := newTestServer(t)
	// Deleting the last admin is unreachable through DELETE (the caller is an admin, so it is self);
	// the store test covers that guard. Here: the last admin cannot be demoted.
	omerID := userID(t, st, "omer")
	if err := st.SetAdmin(t.Context(), omerID, true); err != nil {
		t.Fatal(err)
	}
	pavel := login(t, srv, "pavel", testPassword)
	omer := login(t, srv, "omer", testPassword)
	// pavel demotes omer (2 admins -> ok); then omer is no longer admin; pavel is last admin.
	resp := pavel.post(fmt.Sprintf("/api/users/%d/admin", omerID), map[string]any{"admin": false})
	if resp.StatusCode != 204 {
		t.Fatalf("demote omer %d", resp.StatusCode)
	}
	// omer's sessions ended.
	resp = omer.get("/api/me")
	if resp.StatusCode != 401 {
		t.Fatalf("demoted admin session = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	// pavel is the last admin: cannot be demoted.
	pavelID := userID(t, st, "pavel")
	resp = pavel.post(fmt.Sprintf("/api/users/%d/admin", pavelID), map[string]any{"admin": false})
	if resp.StatusCode != 422 {
		t.Fatalf("demote last admin %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Harbormaster needs at least one global admin." {
		t.Fatalf("msg %q", msg)
	}
}

func TestRemoveAdmin_EndsSessionsOfThatUser(t *testing.T) {
	srv, _, st := newTestServer(t)
	omerID := userID(t, st, "omer")
	pavelID := userID(t, st, "pavel")
	if err := st.SetAdmin(t.Context(), omerID, true); err != nil {
		t.Fatal(err)
	}
	pavel := login(t, srv, "pavel", testPassword)
	omer := login(t, srv, "omer", testPassword)
	resp := omer.post(fmt.Sprintf("/api/users/%d/admin", pavelID), map[string]any{"admin": false})
	if resp.StatusCode != 204 {
		t.Fatalf("demote status %d", resp.StatusCode)
	}
	if a := lastAudit(t, st); a.Action != "admin_revoke" || a.Summary != "Removed pavel as global admin." {
		t.Fatalf("audit %+v", a)
	}
	resp = pavel.get("/api/me")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("demoted user's next request = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	// Granting again is audited; a no-op is not.
	resp = omer.post(fmt.Sprintf("/api/users/%d/admin", pavelID), map[string]any{"admin": true})
	resp.Body.Close()
	if a := lastAudit(t, st); a.Action != "admin_grant" || a.Summary != "Made pavel a global admin." {
		t.Fatalf("audit %+v", a)
	}
	before := lastAudit(t, st).ID
	resp = omer.post(fmt.Sprintf("/api/users/%d/admin", pavelID), map[string]any{"admin": true})
	if resp.StatusCode != 204 {
		t.Fatalf("no-op status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if lastAudit(t, st).ID != before {
		t.Fatal("no-op was audited")
	}
}

func TestResetPassword(t *testing.T) {
	srv, _, st := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	omer := login(t, srv, "omer", testPassword)
	var out struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	decode(t, pavel.post(fmt.Sprintf("/api/users/%d/reset-password", userID(t, st, "omer")), nil), &out)
	if out.TemporaryPassword == "" {
		t.Fatal("no temporary password")
	}
	if a := lastAudit(t, st); a.Action != "password_reset" || a.Summary != "Reset the password of omer." {
		t.Fatalf("audit %+v", a)
	}
	resp := omer.get("/api/me")
	if resp.StatusCode != 401 {
		t.Fatalf("old session = %d", resp.StatusCode)
	}
	resp.Body.Close()
	login(t, srv, "omer", out.TemporaryPassword)
}

func TestPermissions_NormalizesAndAudits(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedMockupTags(t, st)
	pavel := login(t, srv, "pavel", testPassword)
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var prodID int64
	for _, tg := range tags {
		if tg.Name == "Prod" {
			prodID = int64(tg.ID)
		}
	}
	omerID := userID(t, st, "omer")
	path := fmt.Sprintf("/api/permissions/%d/%d", omerID, prodID)

	var got struct {
		Actions int `json:"actions"`
	}
	resp := pavel.do("PUT", path, map[string]any{"actions": 2}, nil)
	decode(t, resp, &got)
	if got.Actions != 3 {
		t.Fatalf("actions = %d, want 3", got.Actions)
	}
	a := lastAudit(t, st)
	if a.Action != "permission_set" || a.Summary != "Gave omer View, Logs on Prod." {
		t.Fatalf("audit %+v", a)
	}
	if a.Details["preset"] != "Custom" {
		t.Fatalf("details %+v", a.Details)
	}

	// Unchanged -> 200, no audit.
	before := a.ID
	resp = pavel.do("PUT", path, map[string]any{"actions": 3}, nil)
	decode(t, resp, &got)
	if got.Actions != 3 || lastAudit(t, st).ID != before {
		t.Fatalf("unchanged: %d, audited=%v", got.Actions, lastAudit(t, st).ID != before)
	}

	resp = pavel.do("PUT", path, map[string]any{"actions": 0}, nil)
	decode(t, resp, &got)
	if got.Actions != 0 {
		t.Fatalf("actions = %d", got.Actions)
	}
	if a := lastAudit(t, st); a.Summary != "Removed View, Logs on Prod from omer." {
		t.Fatalf("audit summary %q", a.Summary)
	}

	// Both lines when something is added and something removed.
	pavel.do("PUT", path, map[string]any{"actions": 3}, nil).Body.Close()
	pavel.do("PUT", path, map[string]any{"actions": 33}, nil).Body.Close() // V,L -> V,T
	if a := lastAudit(t, st); a.Summary != "Gave omer Terminal on Prod. Removed Logs on Prod from omer." {
		t.Fatalf("audit summary %q", a.Summary)
	}
}

func TestPermissions_Validation(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedMockupTags(t, st)
	pavel := login(t, srv, "pavel", testPassword)
	omerID, pavelID := userID(t, st, "omer"), userID(t, st, "pavel")
	for _, tc := range []struct {
		path    string
		actions int
		status  int
		msg     string
	}{
		{fmt.Sprintf("/api/permissions/%d/1", pavelID), 1, 422, "Global admins have every permission."},
		{fmt.Sprintf("/api/permissions/%d/1", omerID), 64, 422, ""},
		{fmt.Sprintf("/api/permissions/%d/1", omerID), -1, 422, ""},
		{"/api/permissions/999999/1", 1, 404, ""},
		{fmt.Sprintf("/api/permissions/%d/999999", omerID), 1, 404, ""},
	} {
		resp := pavel.do("PUT", tc.path, map[string]any{"actions": tc.actions}, nil)
		if resp.StatusCode != tc.status {
			t.Errorf("%s %d: status %d, want %d", tc.path, tc.actions, resp.StatusCode, tc.status)
		}
		if tc.msg != "" {
			if _, msg := errorCode(t, resp); msg != tc.msg {
				t.Errorf("message %q", msg)
			}
		} else {
			resp.Body.Close()
		}
	}
}

func TestGetPermissions_Matrix(t *testing.T) {
	srv, _, st := newTestServer(t)
	seedMockupTags(t, st)
	pavel := login(t, srv, "pavel", testPassword)
	var out struct {
		Tags []struct {
			ID               int64  `json:"id"`
			Name             string `json:"name"`
			Color            string `json:"color"`
			ServicesInEffect int    `json:"services_in_effect"`
		} `json:"tags"`
		Users []struct {
			ID       int64          `json:"id"`
			Username string         `json:"username"`
			Admin    bool           `json:"admin"`
			Perms    map[string]int `json:"perms"`
		} `json:"users"`
	}
	decode(t, pavel.get("/api/permissions"), &out)
	if len(out.Tags) != 3 || out.Tags[0].Name != "Dev" || out.Tags[1].Name != "Prod" || out.Tags[2].Name != "Staging" {
		t.Fatalf("tags %+v", out.Tags)
	}
	names := []string{}
	for _, u := range out.Users {
		names = append(names, u.Username)
		if u.Admin && len(u.Perms) != 0 {
			t.Fatalf("admin perms %+v", u.Perms)
		}
		if u.Perms == nil {
			t.Fatalf("%s perms is null", u.Username)
		}
	}
	if fmt.Sprint(names) != "[dana omer oncall pavel]" {
		t.Fatalf("users %v", names)
	}
	if out.Users[0].Perms[fmt.Sprint(out.Tags[0].ID)] != 63 {
		t.Fatalf("dana perms %+v", out.Users[0].Perms)
	}
}
