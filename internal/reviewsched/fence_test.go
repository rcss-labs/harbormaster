package reviewsched

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

func TestReviewschedImportsOnlyTheStandardLibrary(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range sourceFiles(t) {
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				t.Errorf("%s imports %s, which is not the standard library", f, path)
			}
		}
	}
}

func TestNoUseOfLocalOrNow(t *testing.T) {
	for _, f := range sourceFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"time.Local", "time.Now("} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s uses %s", f, banned)
			}
		}
	}
}

func TestTzdataIsEmbedded(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range sourceFiles(t) {
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if imp.Path.Value == `"time/tzdata"` && imp.Name != nil && imp.Name.Name == "_" {
				return
			}
		}
	}
	t.Fatal(`no file imports _ "time/tzdata"`)
}
