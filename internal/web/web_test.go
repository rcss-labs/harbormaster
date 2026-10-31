package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

var testFS = fstest.MapFS{
	"index.html":    {Data: []byte("<html>spa</html>")},
	"assets/app.js": {Data: []byte("console.log(1)")},
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSPAFallback_ServesIndexForDeepLink(t *testing.T) {
	rec := get(newHandler(testFS), "/clusters/core-services")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || string(body) != "<html>spa</html>" {
		t.Fatalf("deep link = %d %q, want index.html", rec.Code, body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("index Cache-Control = %q, want no-store", got)
	}
}

func TestSPAFallback_NoFallbackForAPI(t *testing.T) {
	rec := get(newHandler(testFS), "/api/unknown")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestAssets_AreImmutable(t *testing.T) {
	rec := get(newHandler(testFS), "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestMissingFileWithExtension_Is404(t *testing.T) {
	for _, p := range []string{"/assets/missing.js", "/favicon.ico"} {
		if rec := get(newHandler(testFS), p); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
}

func TestHandler_ServesEmbeddedPlaceholder(t *testing.T) {
	if rec := get(Handler(), "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ = %d, want 200", rec.Code)
	}
}
