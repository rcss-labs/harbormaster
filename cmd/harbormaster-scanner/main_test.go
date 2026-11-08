package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOptionsFromEnv_AppliesDefaultsAndRejectsBadRunID(t *testing.T) {
	env := map[string]string{"HM_SCAN_RUN_ID": "12", "HM_SCAN_BUCKET": "bucket", "HM_SCAN_MODE": "scan"}
	opts, err := optionsFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if opts.RunID != 12 || opts.WorkDir != "/work" || opts.SyftPath != "/usr/local/bin/syft" || opts.GrypePath != "/usr/local/bin/grype" {
		t.Errorf("options = %+v, want run 12 with default paths", opts)
	}
	env["HM_SCAN_RUN_ID"] = "abc"
	if _, err := optionsFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Error("a non-numeric run id must be rejected")
	}
}

func TestHealthcheck_NeedsExecutableTools(t *testing.T) {
	dir := t.TempDir()
	syft, grype := filepath.Join(dir, "syft"), filepath.Join(dir, "grype")
	for _, path := range []string{syft, grype} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"HM_SCAN_SYFT": syft, "HM_SCAN_GRYPE": grype}
	get := func(k string) string { return env[k] }
	if code := healthcheck(&bytes.Buffer{}, get); code != 0 {
		t.Fatalf("healthcheck = %d, want 0", code)
	}
	if err := os.Chmod(grype, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := healthcheck(&bytes.Buffer{}, get); code != 1 {
		t.Errorf("healthcheck = %d, want 1 for a non-executable grype", code)
	}
}
