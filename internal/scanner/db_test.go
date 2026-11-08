package scanner

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

func TestRun_StaleDBWritesDbStaleAndNoResults(t *testing.T) {
	h := newHarness(t, image(digestIndex, "edge"), image(digestSingle, "stable"))
	h.job.DB.BuiltAt = h.now.Add(-200 * time.Hour)
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if m := h.manifest(); m.Status != scan.ManifestDBStale {
		t.Errorf("manifest status = %q, want db_stale", m.Status)
	}
	if len(h.s3.puts) != 1 || h.s3.puts[0] != scan.ManifestKey(testRunID) {
		t.Errorf("puts = %v, want only the manifest", h.s3.puts)
	}
	if n := len(h.cmd.scanCalls("syft")); n != 0 {
		t.Errorf("syft ran %d times against a stale DB", n)
	}
}

func TestRun_ScanVerifiesDBChecksumFromJob(t *testing.T) {
	t.Run("mismatching archive fails the run before any image", func(t *testing.T) {
		h := newHarness(t, image(digestSingle, "stable"))
		h.s3.objects["grype-db/v6/db.tar.zst"] = []byte("tampered archive")
		if code := h.run(); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		m := h.manifest()
		if m.Status != scan.ManifestFailed || m.Error != "checksum mismatch" {
			t.Errorf("manifest = status %q error %q, want failed / checksum mismatch", m.Status, m.Error)
		}
		if len(h.cmd.scanCalls("syft")) != 0 || h.ecr.tokenCalls != 0 {
			t.Error("no image may be processed after a checksum mismatch")
		}
		for _, c := range h.cmd.calls {
			if len(c.args) > 1 && c.args[0] == "db" {
				t.Error("a mismatching archive must not be imported")
			}
		}
	})

	t.Run("a tampered latest.json is never read", func(t *testing.T) {
		h := newHarness(t, image(digestSingle, "stable"))
		h.s3.objects[scan.DBLatestKey] = []byte(`{"built_at":"2020-01-01T00:00:00Z","checksum":"sha256:bad","archive_key":"grype-db/v6/evil.tar.zst"}`)
		if code := h.run(); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if m := h.manifest(); m.Status != scan.ManifestSucceeded {
			t.Errorf("manifest status = %q, want succeeded", m.Status)
		}
		for _, key := range h.s3.gets {
			if key == scan.DBLatestKey {
				t.Error("the scanner read latest.json")
			}
		}
	})
}

func TestRun_ImportsDBWithGrypeCacheEnv(t *testing.T) {
	h := newHarness(t, image(digestSingle, "stable"))
	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var imported, scanned *call
	for i := range h.cmd.calls {
		c := &h.cmd.calls[i]
		if len(c.args) > 1 && c.args[0] == "db" && c.args[1] == "import" {
			imported = c
		}
		if len(c.args) > 0 && len(c.args[0]) > 5 && c.args[0][:5] == "sbom:" {
			scanned = c
		}
	}
	if imported == nil || scanned == nil {
		t.Fatalf("db import / grype scan calls missing: %+v", h.cmd.calls)
	}
	want := []string{"GRYPE_DB_AUTO_UPDATE=false", "GRYPE_DB_REQUIRE_UPDATE_CHECK=false", "GRYPE_DB_VALIDATE_AGE=true", "GRYPE_DB_MAX_ALLOWED_BUILT_AGE=120h"}
	have := map[string]bool{}
	for _, kv := range scanned.env {
		have[kv] = true
	}
	for _, kv := range want {
		if !have[kv] {
			t.Errorf("grype scan env lacks %s", kv)
		}
	}
}
