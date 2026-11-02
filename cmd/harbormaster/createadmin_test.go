package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// TestCreateAdmin_CreatesBreakGlassAdmin pins the owner's ruling that create-admin always makes a
// break-glass admin, so the account keeps a password sign-in when SSO is enabled.
func TestCreateAdmin_CreatesBreakGlassAdmin(t *testing.T) {
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	ctx := t.Context()
	schemaURL := schemaScopedURL(t, dbURL)
	st, err := store.Open(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()

	const username = "breakglass-cli"
	getenv := func(k string) string {
		if k == "HM_DATABASE_URL" {
			return schemaURL
		}
		return ""
	}
	var out bytes.Buffer
	if err := runCreateAdmin(ctx, []string{"--username", username}, getenv, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runCreateAdmin: %v", err)
	}

	st, err = store.Open(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Admin || !u.BreakGlass || !u.MustChangePassword {
		t.Errorf("admin=%v break_glass=%v must_change=%v; want all true", u.Admin, u.BreakGlass, u.MustChangePassword)
	}
	want := "created break-glass admin " + username + " (must change password at first sign-in)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output %q does not contain %q", out.String(), want)
	}
}
