package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func authHash() (string, error) { return auth.HashPassword(testPassword) }

type meBody struct {
	ID                 int64          `json:"id"`
	Username           string         `json:"username"`
	Admin              bool           `json:"admin"`
	MustChangePassword bool           `json:"must_change_password"`
	CSRFToken          string         `json:"csrf_token"`
	Perms              map[string]int `json:"perms"`
	Tags               []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Color   string `json:"color"`
		Builtin bool   `json:"builtin"`
	} `json:"tags"`
	Accounts []struct {
		ID        int64    `json:"id"`
		Name      string   `json:"name"`
		AccountID string   `json:"account_id"`
		Regions   []string `json:"regions"`
	} `json:"accounts"`
	Regions []string `json:"regions"`
}

func TestLogin_SetsCookieAndCSRF(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := newAPIClient(t, srv)
	resp := c.post("/api/auth/login", map[string]string{"username": "pavel", "password": testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Value == "" {
		t.Fatalf("session cookie = %+v, want HttpOnly with a value", cookie)
	}
	var out struct {
		User meBody `json:"user"`
	}
	decode(t, resp, &out)
	if out.User.Username != "pavel" || !out.User.Admin || out.User.CSRFToken == "" {
		t.Fatalf("user = %+v, want pavel admin with csrf token", out.User)
	}
}

func TestLogin_WrongPasswordIsGeneric(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, tc := range []struct{ user, pw string }{{"pavel", "wrong-password-xx"}, {"ghost", "wrong-password-xx"}} {
		resp := newAPIClient(t, srv).post("/api/auth/login", map[string]string{"username": tc.user, "password": tc.pw})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.user, resp.StatusCode)
		}
		code, msg := errorCode(t, resp)
		if code != "unauthenticated" || msg != "Wrong username or password." {
			t.Fatalf("%s: %q %q, want unauthenticated / generic message", tc.user, code, msg)
		}
	}
	empty := newAPIClient(t, srv).post("/api/auth/login", map[string]string{"username": "", "password": ""})
	if empty.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty fields: status = %d, want 422", empty.StatusCode)
	}
	empty.Body.Close()
}

func TestMe_ShapeForAdminAndUser(t *testing.T) {
	srv, _, st := newTestServer(t)
	ctx := t.Context()
	for _, a := range []store.AWSAccount{
		{Name: "homelab", AccountID: "111111111111", RoleARN: "arn:aws:iam::111111111111:role/r", ExternalID: "x", Regions: []string{"us-east-1", "eu-west-1"}, Enabled: true},
		{Name: "staging", AccountID: "222222222222", RoleARN: "arn:aws:iam::222222222222:role/r", ExternalID: "x", Regions: []string{"us-east-1"}, Enabled: true},
	} {
		if _, err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	admin := login(t, srv, "pavel", testPassword)
	var a meBody
	decode(t, admin.get("/api/me"), &a)
	if !a.Admin || a.Username != "pavel" || a.CSRFToken != admin.csrf {
		t.Fatalf("admin me = %+v", a)
	}
	if len(a.Accounts) != 2 {
		t.Fatalf("admin accounts = %d, want 2 (all enabled)", len(a.Accounts))
	}

	omer := login(t, srv, "omer", testPassword)
	var o meBody
	decode(t, omer.get("/api/me"), &o)
	if o.Admin || o.Username != "omer" {
		t.Fatalf("omer me = %+v", o)
	}
	if o.Accounts == nil || o.Regions == nil || o.Tags == nil || o.Perms == nil {
		t.Fatalf("omer slices/maps must be empty, never null: %+v", o)
	}
	if len(o.Accounts) != 0 || len(o.Regions) != 0 {
		t.Fatalf("omer has no permissions yet but sees accounts=%v regions=%v", o.Accounts, o.Regions)
	}
	if len(o.Tags) == 0 || !o.Tags[0].Builtin {
		t.Fatalf("tags = %+v, want at least the built-in Dev tag", o.Tags)
	}

	// View on Dev plus one cluster with a service in homelab/eu-west-1 only: omer sees exactly that account and region.
	omerUser, err := st.GetUserByUsername(ctx, "omer")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPermission(ctx, omerUser.ID, dev.ID, authz.ActionSet(authz.View)); err != nil {
		t.Fatal(err)
	}
	ck := model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "apps"}
	if _, err := st.UpsertClusters(ctx, []model.Cluster{{Key: ck, PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertServices(ctx, []model.Service{{Key: model.ServiceKey{ClusterKey: ck, Service: "web"}, PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	var o2 meBody
	decode(t, omer.get("/api/me"), &o2)
	if len(o2.Accounts) != 1 || o2.Accounts[0].AccountID != "111111111111" ||
		len(o2.Accounts[0].Regions) != 1 || o2.Accounts[0].Regions[0] != "eu-west-1" {
		t.Fatalf("omer accounts = %+v, want homelab with eu-west-1 only", o2.Accounts)
	}
	if len(o2.Regions) != 1 || o2.Regions[0] != "eu-west-1" || o2.Perms[strconv.FormatInt(int64(dev.ID), 10)] != int(authz.View) {
		t.Fatalf("omer regions=%v perms=%v", o2.Regions, o2.Perms)
	}
}

func TestLogout_ClearsSession(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	out := c.post("/api/auth/logout", nil)
	out.Body.Close()
	if out.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", out.StatusCode)
	}
	me := c.get("/api/me")
	me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", me.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "omer", testPassword)

	wrong := c.post("/api/auth/password", map[string]string{"current": "nope-nope-nope-1", "new": "another-good-password"})
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current = %d, want 401", wrong.StatusCode)
	}
	if _, msg := errorCode(t, wrong); msg != "Current password is wrong." {
		t.Fatalf("message = %q", msg)
	}
	short := c.post("/api/auth/password", map[string]string{"current": testPassword, "new": "short"})
	if short.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("short new = %d, want 422", short.StatusCode)
	}
	if code, msg := errorCode(t, short); code != "invalid" || msg != "Use at least 12 characters." {
		t.Fatalf("short: %q %q", code, msg)
	}
	ok := c.post("/api/auth/password", map[string]string{"current": testPassword, "new": "another-good-password"})
	ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("change = %d, want 204", ok.StatusCode)
	}
	// The current session survives the change.
	me := c.get("/api/me")
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me after change = %d, want 200", me.StatusCode)
	}
}

func TestSetup_FlowWhenNoUsers(t *testing.T) {
	srv, _, _, logs := newServerWithLog(t, false)
	c := newAPIClient(t, srv)

	var needed struct {
		Needed bool `json:"needed"`
	}
	decode(t, c.get("/api/auth/setup"), &needed)
	if !needed.Needed {
		t.Fatal("needed = false, want true")
	}

	bad := c.post("/api/auth/setup", map[string]string{"token": "wrong", "username": "root", "password": testPassword})
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("bad token = %d, want 403", bad.StatusCode)
	}
	if code, _ := errorCode(t, bad); code != "bad_setup_token" {
		t.Fatalf("code = %q, want bad_setup_token", code)
	}

	token := setupTokenFrom(t, logs)
	resp := c.post("/api/auth/setup", map[string]string{"token": token, "username": "root", "password": testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup = %d, want 200", resp.StatusCode)
	}
	var me meBody
	decode(t, resp, &me)
	if me.Username != "root" || !me.Admin || me.CSRFToken == "" {
		t.Fatalf("me = %+v", me)
	}
	// Signed in by the same response: the cookie works.
	check := c.get("/api/me")
	check.Body.Close()
	if check.StatusCode != http.StatusOK {
		t.Fatalf("me after setup = %d, want 200", check.StatusCode)
	}

	again := c.post("/api/auth/setup", map[string]string{"token": token, "username": "x2", "password": testPassword})
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", again.StatusCode)
	}
	again.Body.Close()
}
