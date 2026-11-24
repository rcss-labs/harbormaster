package agent

import (
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
func TestAgentHasNoStrayHTTPClient(t *testing.T) {
	forbidden := []string{"http.Get(", "http.Post(", "http.PostForm(", "http.Head(", "http.DefaultClient", "http.Client{"}
	var files []string
	for _, pattern := range []string{"*.go", "provider/*.go", "provider/*/*.go", "../hmtools/*.go", "../../cmd/harbormaster-agent/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	checked := 0
	for _, file := range files {
		slash := filepath.ToSlash(file)
		if strings.HasSuffix(slash, "_test.go") {
			continue
		}
		if slash == "../hmtools/upstream.go" || slash == "../../cmd/harbormaster-agent/main.go" ||
			(strings.HasPrefix(slash, "provider/") && filepath.Base(slash) == "client.go") {
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
	if checked < 15 {
		t.Errorf("only %d source files checked; the globs are wrong", checked)
	}
	main, err := os.ReadFile("../../cmd/harbormaster-agent/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(main), "http.Client{"); n > 1 {
		t.Errorf("the command's main.go declares %d http.Client values, want at most the healthcheck's 1", n)
	}
}
