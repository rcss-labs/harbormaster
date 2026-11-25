package agentresult

import (
	"os/exec"
	"strings"
	"testing"
)

// Both the agent service and Harbormaster import this package, so it may link nothing beyond the standard library
// and the scrubber.
func TestAgentResultImportsOnlyStdlibAndSecretshape(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{.Standard}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	sawSelf := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, std, _ := strings.Cut(line, " ")
		if std == "true" {
			continue
		}
		switch pkg {
		case module + "internal/agentresult":
			sawSelf = true
		case module + "internal/secretshape":
		default:
			t.Errorf("agentresult depends on %s, which is neither standard library nor secretshape", pkg)
		}
	}
	if !sawSelf {
		t.Fatal("the package itself was not part of the dependency list")
	}
}
