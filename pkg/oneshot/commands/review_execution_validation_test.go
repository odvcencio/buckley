package commands

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestValidateReviewEvidenceCoverageRequiresSameToolchainAndChangedPaths(t *testing.T) {
	changed := []string{"pkg/a/a.go", "pkg/b/b.go"}
	target := func(path string, recursive bool) []reviewCoverageTarget {
		return []reviewCoverageTarget{{Path: path, Recursive: recursive}}
	}

	t.Run("disjoint package evidence", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg/a", false)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg/b", false)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err == nil {
			t.Fatal("disjoint build/test targets satisfied changed-file coverage")
		}
	})

	t.Run("mixed toolchains", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target(".", true)},
			{Kind: reviewEvidenceTest, Language: "python", Targets: target(".", true)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err == nil {
			t.Fatal("mixed build/test toolchains satisfied approval")
		}
	})

	t.Run("recursive Go scope", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg", true)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg", true)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err != nil {
			t.Fatalf("recursive applicable evidence rejected: %v", err)
		}
	})

	t.Run("repo verification preset", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "*", Targets: target(".", true)},
			{Kind: reviewEvidenceTest, Language: "*", Targets: target(".", true)},
		}
		if err := validateReviewEvidenceCoverage(append(changed, "web/app.ts"), evidence); err != nil {
			t.Fatalf("paired repo-wide build/test evidence rejected: %v", err)
		}
	})

	t.Run("unrecognized configuration still requires repository coverage", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg/a", false)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg/a", false)},
		}
		if err := validateReviewEvidenceCoverage([]string{"release.yaml"}, evidence); err == nil {
			t.Fatal("scoped unrelated evidence approved an unrecognized configuration change")
		}
	})

	t.Run("Cargo default members do not imply workspace coverage", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "rust", Targets: target(".", false)},
			{Kind: reviewEvidenceTest, Language: "rust", Targets: target(".", false)},
		}
		if err := validateReviewEvidenceCoverage([]string{"crates/changed/src/lib.rs"}, evidence); err == nil {
			t.Fatal("plain Cargo default-member evidence covered an arbitrary changed crate")
		}
	})
}

func TestValidateReviewEvidenceCoverageMixedGoPythonPullRequest(t *testing.T) {
	changed := []string{
		"cgo_harness/parity_gate_ratchet_test.go",
		"cgo_harness/parity_javascript_regression_test.go",
		"grammars/external_lex_states_election_test.go",
		"grammars/javascript_external_lex_states.go",
		"grammars/javascript_external_lex_states_default_test.go",
		"grammars/javascript_external_lex_states_regression_test.go",
		"grammars/javascript_scanner.go",
		"parser_recover_c.go",
		"parser_result_test/parser_result_javascript_block_assignment_regression_test.go",
		"cgo_harness/tier_scan/gen_external_lex_elections.py",
	}
	commands := []struct {
		command string
		output  string
	}{
		{command: `go test -run '^$' . ./grammars ./parser_result_test`},
		{command: "go test . ./grammars ./parser_result_test", output: "ok  root\nok  grammars\nok  parser_result_test"},
		{command: `go -C cgo_harness test -run '^$' .`},
		{command: "go -C cgo_harness test .", output: "ok  cgo_harness"},
		{command: "python3 -m py_compile cgo_harness/tier_scan/gen_external_lex_elections.py"},
		{command: "python3 cgo_harness/tier_scan/gen_external_lex_elections.py --check", output: "external lex election ledger is current"},
	}

	evidence := classifyCompletedReviewCommands(t, commands)
	if err := validateReviewEvidenceCoverage(changed, evidence); err != nil {
		t.Fatalf("valid mixed Go/Python evidence rejected: %v", err)
	}

	withoutPythonBuild := append([]reviewCommandEvidenceDetails(nil), evidence[:4]...)
	withoutPythonBuild = append(withoutPythonBuild, evidence[5])
	err := validateReviewEvidenceCoverage(changed, withoutPythonBuild)
	if err == nil || !strings.Contains(err.Error(), "python(build:cgo_harness/tier_scan/gen_external_lex_elections.py)") {
		t.Fatalf("missing Python build evidence error = %v", err)
	}

	wrongCheck := classifyCompletedReviewCommands(t, []struct {
		command string
		output  string
	}{{command: "python3 scripts/other.py --check", output: "current"}})
	err = validateReviewEvidenceCoverage(changed, append(evidence[:5:5], wrongCheck...))
	if err == nil || !strings.Contains(err.Error(), "python(test:cgo_harness/tier_scan/gen_external_lex_elections.py)") {
		t.Fatalf("unrelated exact-file check error = %v", err)
	}
}

func classifyCompletedReviewCommands(t *testing.T, commands []struct {
	command string
	output  string
}) []reviewCommandEvidenceDetails {
	t.Helper()
	zero := 0
	evidence := make([]reviewCommandEvidenceDetails, 0, len(commands))
	for _, command := range commands {
		details, ok := classifyReviewCommandEvidenceDetails(model.CommandExecutionEvidence{
			Command:          command.command,
			AggregatedOutput: command.output,
			ExitCode:         &zero,
			Status:           "completed",
			WorkingDirectory: "/snapshot",
			RepositoryRoot:   "/snapshot",
		})
		if !ok {
			t.Fatalf("command did not classify: %s", command.command)
		}
		evidence = append(evidence, details)
	}
	return evidence
}

func TestReviewChangedFilesDocumentationOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		paths []string
		want  bool
	}{
		"markdown":              {paths: []string{"README.md", "docs/release.mdx"}, want: true},
		"other doc formats":     {paths: []string{"guide.rst", "docs/design.adoc"}, want: true},
		"extensionless license": {paths: []string{"LICENSE"}, want: true},
		"empty":                 {paths: nil, want: false},
		"mixed source":          {paths: []string{"README.md", "main.go"}, want: false},
		"mixed configuration":   {paths: []string{"README.md", "release.yaml"}, want: false},
		"unsafe path":           {paths: []string{"../README.md"}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reviewChangedFilesDocumentationOnly(tc.paths); got != tc.want {
				t.Fatalf("reviewChangedFilesDocumentationOnly(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}
