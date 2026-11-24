package agent

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The agent holds provider keys and takes only run tokens, so it may link none of Harbormaster's stores, the SAML code or any
// AWS service client beyond Bedrock's runtime and its credential chain.
func TestAgentImportsNothingElseFromTheModule(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	allowedModule := []string{
		module + "internal/agent", // and everything below it
		module + "internal/hmtools",
		module + "internal/apitoken",
		module + "internal/secretshape",
		module + "cmd/harbormaster-agent",
	}
	// "internal" holds the helper packages the allowed service clients share.
	allowedAWSServices := []string{"bedrockruntime", "sts", "sso", "ssooidc", "signin", "internal"}

	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./...", "../../cmd/harbormaster-agent").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	packages := strings.Fields(string(out))
	if !strings.Contains(string(out), module+"cmd/harbormaster-agent") {
		t.Fatal("the agent command was not part of the dependency check")
	}
	for _, pkg := range packages {
		if strings.HasPrefix(pkg, module) {
			ok := false
			for _, prefix := range allowedModule {
				if pkg == prefix || strings.HasPrefix(pkg, prefix+"/") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("the agent depends on %s, which is outside its allowed packages", pkg)
			}
		}
		for _, banned := range []string{"jackc/pgx", "valkey-io", "crewjam/saml"} {
			if strings.Contains(pkg, banned) {
				t.Errorf("the agent links %s (contains %q)", pkg, banned)
			}
		}
		if _, service, found := strings.Cut(pkg, "aws-sdk-go-v2/service/"); found {
			service, _, _ = strings.Cut(service, "/")
			allowed := false
			for _, a := range allowedAWSServices {
				allowed = allowed || service == a
			}
			if !allowed {
				t.Errorf("the agent links the AWS service client %s", pkg)
			}
		}
	}
}

// The only HTTP clients are the upstream's, one per provider package, and the command's loopback healthcheck.
// Those files may each build one client; no file may use the package-level helpers or the default client.
func TestAgentHasNoStrayHTTPClient(t *testing.T) {
	forbiddenAlways := []string{"http.Get(", "http.Post(", "http.PostForm(", "http.Head(", "http.DefaultClient"}
	exemptFile := func(path string) bool {
		slash := filepath.ToSlash(path)
		if slash == "../hmtools/upstream.go" || slash == "../../cmd/harbormaster-agent/main.go" {
			return true
		}
		// provider/<name>/client.go and nothing deeper or shallower
		parts := strings.Split(slash, "/")
		return len(parts) == 3 && parts[0] == "provider" && parts[2] == "client.go"
	}
	checked := 0
	for _, root := range []string{".", "../hmtools", "../apitoken", "../secretshape", "../../cmd/harbormaster-agent"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checked++
			clients := 0
			for _, line := range strings.Split(string(source), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, bad := range forbiddenAlways {
					if strings.Contains(line, bad) {
						t.Errorf("%s uses %s", path, bad)
					}
				}
				clients += strings.Count(line, "http.Client{")
			}
			if limit := map[bool]int{true: 1, false: 0}[exemptFile(path)]; clients > limit {
				t.Errorf("%s builds %d http.Client values, want at most %d", path, clients, limit)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 20 {
		t.Errorf("only %d source files checked; the walk is wrong", checked)
	}
}
