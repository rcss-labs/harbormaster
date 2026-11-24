package hmtools

import (
	"os/exec"
	"strings"
	"testing"
)

// The tool table is shared with the agent service, so it may import only itself and internal/apitoken from this
// module, and nothing that links the store, the cache, AWS or the MCP SDK.
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
		for _, banned := range []string{"jackc/pgx", "valkey-io", "aws-sdk-go-v2", "modelcontextprotocol"} {
			if strings.Contains(pkg, banned) {
				t.Errorf("tool table links %s (contains %q)", pkg, banned)
			}
		}
	}
}
