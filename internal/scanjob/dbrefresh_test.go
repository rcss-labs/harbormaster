package scanjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/scan"
)

const archivePath = "vulnerability-db_v6_2026-11-07T01-00-00Z.tar.zst"

// upstream serves Anchore's listing and one archive over TLS. listedChecksum is what the listing claims.
func upstream(t *testing.T, listedChecksum string, archive func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/databases/v6/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(scan.UpstreamDB{Built: time.Now().Add(-time.Hour).UTC(), Path: archivePath, Checksum: listedChecksum})
	})
	mux.HandleFunc("/databases/v6/"+archivePath, archive)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func checksumOf(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// refreshRunner is a runner that can only refresh: it needs no database.
func refreshRunner(srv *httptest.Server, scanner *fake.Scanner) *Runner {
	return &Runner{Bucket: scanner, DB: newHTTPDBSource(srv.Client(), srv.URL+"/databases/v6/latest.json", true), Now: time.Now, Log: quietLog()}
}

func seededScanner(t *testing.T) (*fake.Scanner, []byte) {
	t.Helper()
	scanner := fake.NewScanner(fake.MockupEstate(time.Now))
	old := putMirrorDB(t, scanner, time.Now().Add(-30*time.Hour))
	data, _ := json.Marshal(old)
	return scanner, data
}

func assertOldDBKept(t *testing.T, scanner *fake.Scanner, old []byte) {
	t.Helper()
	if got := scanner.Objects()[scan.DBLatestKey]; !bytes.Equal(got, old) {
		t.Fatalf("latest.json = %s, want the old one kept", got)
	}
	for key := range scanner.Objects() {
		if key != scan.DBLatestKey {
			t.Fatalf("object %s was stored, want no archive", key)
		}
	}
	if len(scanner.AbortedUploads()) != 1 {
		t.Fatalf("aborted uploads = %v, want exactly one", scanner.AbortedUploads())
	}
}

func TestRefreshDB_StoresArchiveThenLatest(t *testing.T) {
	archive := []byte("archive bytes")
	srv := upstream(t, checksumOf(archive), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	scanner := fake.NewScanner(fake.MockupEstate(time.Now))
	if err := refreshRunner(srv, scanner).refreshDB(context.Background()); err != nil {
		t.Fatal(err)
	}
	var latest scan.DBLatest
	if err := json.Unmarshal(scanner.Objects()[scan.DBLatestKey], &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := scan.ParseDBLatest(scanner.Objects()[scan.DBLatestKey], time.Now()); err != nil {
		t.Fatalf("latest.json is not valid for the scanner side: %v", err)
	}
	if !bytes.Equal(scanner.Objects()[latest.ArchiveKey], archive) || latest.Checksum != checksumOf(archive) {
		t.Fatalf("latest = %+v, archive stored = %q, want the verified archive", latest, scanner.Objects()[latest.ArchiveKey])
	}
	writes := scanner.Writes()
	if writes[len(writes)-1] != scan.DBLatestKey {
		t.Fatalf("writes = %v, want latest.json last", writes)
	}
}

func TestRefreshDB_ChecksumMismatchAbortsAndKeepsOldDB(t *testing.T) {
	srv := upstream(t, checksumOf([]byte("what the listing promised")), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("something else"))
	})
	scanner, old := seededScanner(t)
	if err := refreshRunner(srv, scanner).refreshDB(context.Background()); err == nil {
		t.Fatal("refresh succeeded on a checksum mismatch")
	}
	assertOldDBKept(t, scanner, old)
}

func TestRefreshDB_OversizeAborts(t *testing.T) {
	archive := bytes.Repeat([]byte("x"), 1000)
	srv := upstream(t, checksumOf(archive), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	scanner, old := seededScanner(t)
	runner := refreshRunner(srv, scanner)
	runner.archiveCap = 100
	if err := runner.refreshDB(context.Background()); err == nil {
		t.Fatal("refresh succeeded on an archive over the cap")
	}
	assertOldDBKept(t, scanner, old)
}

func TestRefreshDB_TimeoutKeepsOldDB(t *testing.T) {
	srv := upstream(t, checksumOf([]byte("never arrives")), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	scanner, old := seededScanner(t)
	runner := refreshRunner(srv, scanner)
	runner.refreshTimeout = 200 * time.Millisecond
	started := time.Now()
	if err := runner.refreshDB(context.Background()); err == nil {
		t.Fatal("refresh succeeded against a stalled download")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("refresh took %v, want it cut at its timeout", time.Since(started))
	}
	assertOldDBKept(t, scanner, old)
}

func TestHTTPDBSource_OnlyHTTPSAndSameHostRedirects(t *testing.T) {
	ctx := context.Background()
	other := 0
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { other++ }))
	defer elsewhere.Close()
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/latest.json", http.StatusFound)
	}))
	defer redirecting.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer plain.Close()

	if _, err := newHTTPDBSource(redirecting.Client(), redirecting.URL+"/latest.json", true).FetchLatest(ctx); err == nil || other != 0 {
		t.Fatalf("cross-host redirect: err = %v, hits elsewhere = %d, want refused before connecting", err, other)
	}
	if _, err := newHTTPDBSource(plain.Client(), plain.URL+"/latest.json", true).FetchLatest(ctx); err == nil {
		t.Fatal("plain http was accepted")
	}
}
