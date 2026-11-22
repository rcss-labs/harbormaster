package mcpgw

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The gateway may import only itself and internal/apitoken from this module, and must not link the store,
// the cache, AWS or SAML code (design D5).
func TestGatewayImportsNothingElseFromTheModule(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	allowed := map[string]bool{
		module + "internal/mcpgw": true, module + "internal/apitoken": true, module + "cmd/harbormaster-mcp": true,
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".", "../../cmd/harbormaster-mcp").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	packages := strings.Fields(string(out))
	for _, pkg := range packages {
		if strings.HasPrefix(pkg, module) && !allowed[pkg] {
			t.Errorf("gateway depends on %s; only internal/mcpgw and internal/apitoken are allowed", pkg)
		}
		for _, banned := range []string{"jackc/pgx", "valkey-io", "aws-sdk-go-v2", "crewjam/saml"} {
			if strings.Contains(pkg, banned) {
				t.Errorf("gateway links %s (contains %q)", pkg, banned)
			}
		}
	}
	if !strings.Contains(string(out), module+"cmd/harbormaster-mcp") {
		t.Error("the gateway command was not part of the dependency check")
	}
}

// One Upstream is the only HTTP client the gateway has: nothing else may be able to call another host.
func TestGatewayHasOneHTTPClient(t *testing.T) {
	forbidden := []string{"http.Get(", "http.Post(", "http.PostForm(", "http.Head(", "http.DefaultClient", "http.Client{"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	command, err := filepath.Glob("../../cmd/harbormaster-mcp/*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range append(files, command...) {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		base := filepath.Base(file)
		// upstream.go owns the client; the healthcheck's loopback client lives in the command's main.go.
		if base == "upstream.go" || (base == "main.go" && strings.Contains(file, "cmd")) {
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
	if checked < 5 {
		t.Errorf("only %d source files checked; the glob is wrong", checked)
	}
	// And upstream.go itself builds exactly one client value.
	source, err := os.ReadFile("upstream.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(source), "http.Client{"); n > 1 {
		t.Errorf("upstream.go declares %d http.Client values, want at most 1", n)
	}
	// The command's one allowed client is the healthcheck's.
	main, err := os.ReadFile("../../cmd/harbormaster-mcp/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(main), "http.Client{"); n != 1 {
		t.Errorf("cmd main.go declares %d http.Client values, want exactly the healthcheck's 1", n)
	}
}
