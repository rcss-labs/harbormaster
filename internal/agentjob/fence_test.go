package agentjob

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The driver sits under the jobs and knows neither of them nor the web layer.
func TestAgentjobImportsNothingFromTheJobsOrTheWebLayer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files: %v", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"internal/investigate", "internal/review", "internal/api"} {
				if strings.HasSuffix(path, banned) || strings.Contains(path, banned+"/") {
					t.Errorf("%s imports %s", name, path)
				}
			}
			if path == "net/http" || strings.HasPrefix(path, "net/http/") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
	if _, err := os.Stat("driver.go"); err != nil {
		t.Error(err)
	}
}
