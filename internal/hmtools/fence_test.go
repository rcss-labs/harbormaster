package hmtools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tool table is shared with the agent service, so it may import only itself and internal/apitoken from this
// module, and nothing that links the store, the cache, AWS, SAML or the MCP SDK.
func TestToolPackageImportsOnlyStdlibAndAPIToken(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	allowed := map[string]bool{module + "internal/hmtools": true, module + "internal/apitoken": true}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, module) && !allowed[pkg] {
			t.Errorf("tool table depends on %s; only internal/hmtools and internal/apitoken are allowed", pkg)
		}
		for _, banned := range []string{"jackc/pgx", "valkey-io", "aws-sdk-go-v2", "modelcontextprotocol", "crewjam/saml"} {
			if strings.Contains(pkg, banned) {
				t.Errorf("tool table links %s (contains %q)", pkg, banned)
			}
		}
	}
}

// Only upstream.go may create an HTTP client, and it creates exactly one. The scan covers every non-test file of
// the package, so a new tool file cannot make a call of its own.
func TestToolPackageHasOneHTTPClient(t *testing.T) {
	forbidden := []string{"http.Get(", "http.Post(", "http.PostForm(", "http.Head(", "http.DefaultClient", "http.Client{"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "upstream.go" {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, line := range strings.Split(string(source), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(line, bad) {
					t.Errorf("%s uses %s", file, bad)
				}
			}
		}
	}
	if checked < 6 {
		t.Errorf("only %d source files checked; the glob is wrong", checked)
	}
	source, err := os.ReadFile("upstream.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(source), "http.Client{"); n != 1 {
		t.Errorf("upstream.go declares %d http.Client values, want exactly 1", n)
	}
}
