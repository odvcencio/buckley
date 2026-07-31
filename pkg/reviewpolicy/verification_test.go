package reviewpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVerificationConstraints_GotreesitterHostRules(t *testing.T) {
	constraints := ParseVerificationConstraints(`
- Do not run repo-wide ` + "`go test ./...`" + ` or ` + "`go test ./... -race`" + ` on the host.
- For heavy correctness, parity, or race coverage, use Docker isolation only.
- Focused package/unit tests inside Docker, scoped with ` + "`-run`" + ` whenever possible.
`)
	if !constraints.TestsRequireContainer {
		t.Fatal("Docker test requirement was not detected")
	}
	if !constraints.ForbidHostRepoWideGo {
		t.Fatal("repo-wide host Go prohibition was not detected")
	}
	if rejection := constraints.HostRejection("test", "go", "."); !strings.Contains(rejection, "Docker") {
		t.Fatalf("host test rejection = %q", rejection)
	}
}

func TestLoadApplicableVerificationConstraints_IncludesNestedChain(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pkg", "parser")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "AGENTS.md"),
		[]byte("Do not run `go test ./...` on the host.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(nested, "AGENTS.md"),
		[]byte("Run focused tests inside Docker.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	constraints, err := LoadApplicableVerificationConstraints(root, "pkg/parser")
	if err != nil {
		t.Fatal(err)
	}
	if !constraints.TestsRequireContainer || !constraints.ForbidHostRepoWideGo {
		t.Fatalf("nested constraints = %#v", constraints)
	}
}

func TestVerificationConstraints_DoNotInferDockerRequirementFromDescription(t *testing.T) {
	constraints := ParseVerificationConstraints("The repository includes Docker test helpers for CI.")
	if constraints.TestsRequireContainer || constraints.ForbidHostRepoWideGo {
		t.Fatalf("descriptive text created restrictions: %#v", constraints)
	}
}
