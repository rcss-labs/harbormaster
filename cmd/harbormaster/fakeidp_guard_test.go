package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	fakeIdPImportPath = "github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	serveImportPath   = "github.com/rcss-labs/harbormaster/cmd/harbormaster"
)

// TestFakeIdPOnlyImportedByServe guards against the test IdP reaching production code. Every package
// in the module, transitively, must avoid internal/saml/fakeidp except cmd/harbormaster (which mounts
// it under --fake-aws) and the fakeidp package itself. Test files are not part of `go list` deps.
func TestFakeIdPOnlyImportedByServe(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Deps \" \"}}", "./...")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	if leaks := fakeIdPLeaks(string(out)); len(leaks) > 0 {
		t.Errorf("packages depend on the fake IdP, which must stay dev-only: %v", leaks)
	}
}

// fakeIdPLeaks returns the packages in `go list -f "{{.ImportPath}} {{join .Deps \" \"}}"` output
// that depend on the fake IdP without being allowed to.
func fakeIdPLeaks(listing string) []string {
	var leaks []string
	for line := range strings.SplitSeq(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg := fields[0]
		if pkg == serveImportPath || pkg == fakeIdPImportPath {
			continue
		}
		if slices.Contains(fields[1:], fakeIdPImportPath) {
			leaks = append(leaks, pkg)
		}
	}
	return leaks
}

// TestFakeIdPLeaks_ReportsNonServeDependents checks the helper itself, because the module-wide guard
// passing on a clean tree says nothing about whether it would catch a leak.
func TestFakeIdPLeaks_ReportsNonServeDependents(t *testing.T) {
	const module = "github.com/rcss-labs/harbormaster/"
	listing := module + "internal/saml/fakeidp " + module + "internal/saml\n" +
		module + "cmd/harbormaster " + module + "internal/saml/fakeidp\n" +
		module + "internal/api " + module + "internal/store " + module + "internal/saml/fakeidp\n" +
		module + "internal/store " + module + "internal/config\n"
	got := fakeIdPLeaks(listing)
	want := []string{module + "internal/api"}
	if !slices.Equal(got, want) {
		t.Errorf("fakeIdPLeaks = %v; want %v", got, want)
	}
}
