package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/transparency"
)

func writeDeepFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnumerateDeepUnits(t *testing.T) {
	root := t.TempDir()
	writeDeepFixtureFile(t, root, "go.mod", "module fixture\n\ngo 1.22\n")
	writeDeepFixtureFile(t, root, "a/a.go", "package a\n\nfunc A() int { return 1 }\n")
	writeDeepFixtureFile(t, root, "b/b.go", "package b\n\nfunc B() int { return 2 }\n")
	writeDeepFixtureFile(t, root, "b/b_test.go", "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { _ = B() }\n")
	writeDeepFixtureFile(t, root, "app/page.gsx", "package app\n")
	writeDeepFixtureFile(t, root, "scripts/run.sh", "#!/bin/sh\necho hi\n")
	writeDeepFixtureFile(t, root, "shader.sel", "material X {}\n")
	writeDeepFixtureFile(t, root, "node_modules/dep/index.js", "module.exports = 1\n")
	writeDeepFixtureFile(t, root, "b/testdata/fixture.js", "ignored\n")

	units, err := EnumerateDeepUnits(root)
	if err != nil {
		t.Fatalf("EnumerateDeepUnits: %v", err)
	}

	byName := map[string]DeepUnit{}
	var names []string
	for _, u := range units {
		byName[u.Name] = u
		names = append(names, u.Name)
	}

	for _, want := range []string{"pkg:a", "pkg:b", "pkg:app", "files:scripts", "files:root"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing unit %q; got %v", want, names)
		}
	}
	if got := byName["pkg:b"].Files; len(got) != 2 {
		t.Fatalf("pkg b files = %v, want source + test file", got)
	}
	if got := byName["pkg:app"].Files; len(got) != 1 || got[0] != "app/page.gsx" {
		t.Fatalf("gsx should review with its directory package: %v", got)
	}
	if got := byName["files:root"].Files; len(got) != 1 || got[0] != "shader.sel" {
		t.Fatalf("root group = %v, want [shader.sel]", got)
	}
	for _, u := range units {
		for _, f := range u.Files {
			if strings.Contains(f, "node_modules") || strings.Contains(f, "testdata") {
				t.Fatalf("skipped dir leaked into unit %s: %s", u.Name, f)
			}
		}
	}
	for i := 1; i < len(units); i++ {
		if units[i-1].Name >= units[i].Name {
			t.Fatalf("units not sorted: %s >= %s", units[i-1].Name, units[i].Name)
		}
	}
}

func TestAssembleDeepUnitContextBudget(t *testing.T) {
	root := t.TempDir()
	writeDeepFixtureFile(t, root, "p/big.go", "package p\n\n// "+strings.Repeat("x", 4_000)+"\n")
	writeDeepFixtureFile(t, root, "p/small.go", "package p\n\nfunc S() {}\n")

	unit := DeepUnit{Name: "pkg:fixture/p", Dir: "p", Files: []string{"p/big.go", "p/small.go"}}
	audit := transparency.NewContextAudit()
	uc := AssembleDeepUnitContext(root, unit, DeepReviewOptions{UnitTokenBudget: 200, MaxFileBytes: 64_000}, audit)

	if len(uc.Included) != 1 || uc.Included[0] != "p/big.go" {
		t.Fatalf("included = %v, want the first file only (whole-file admission)", uc.Included)
	}
	if len(uc.Omitted) != 1 || uc.Omitted[0] != "p/small.go" {
		t.Fatalf("omitted = %v, want [p/small.go]", uc.Omitted)
	}
	if !strings.Contains(uc.Body, "### p/big.go") || !strings.Contains(uc.Body, "```go") {
		t.Fatalf("body missing fenced file section:\n%s", uc.Body[:120])
	}
	if audit.TotalTokens() == 0 {
		t.Fatal("audit should record unit tokens")
	}

	prompt := BuildDeepUnitPrompt("## Repository\n\n", uc)
	if !strings.Contains(prompt, "Files omitted (unit budget)") || !strings.Contains(prompt, "p/small.go") {
		t.Fatal("prompt must disclose omitted files")
	}
}

func TestBuildDeepSynthesisPromptMarksFailures(t *testing.T) {
	reports := []DeepUnitReport{
		{Unit: DeepUnit{Name: "pkg:a"}, Review: "## Unit Verdict\nfine\n"},
		{Unit: DeepUnit{Name: "pkg:b"}, Err: errors.New("timeout")},
	}
	prompt := BuildDeepSynthesisPrompt(reports, 10_000)
	if !strings.Contains(prompt, "## Unit: pkg:a") {
		t.Fatal("successful unit report missing")
	}
	if !strings.Contains(prompt, "review FAILED: timeout") {
		t.Fatal("failed unit must be marked as a coverage gap")
	}
}

func TestBuildDeepSynthesisPromptTruncatesToBudget(t *testing.T) {
	long := strings.Repeat("finding text ", 5_000)
	reports := []DeepUnitReport{
		{Unit: DeepUnit{Name: "pkg:a"}, Review: long},
		{Unit: DeepUnit{Name: "pkg:b"}, Review: long},
	}
	prompt := BuildDeepSynthesisPrompt(reports, 1_000)
	if !strings.Contains(prompt, "truncated for synthesis budget") {
		t.Fatal("oversized unit reports must be truncated with a marker")
	}
	if got, cap := reviewEstimateTokens(prompt), 2_000; got > cap {
		t.Fatalf("synthesis prompt = %d tokens, want <= %d", got, cap)
	}
}

func TestBuildDeepReportKeepsAppendix(t *testing.T) {
	reports := []DeepUnitReport{
		{Unit: DeepUnit{Name: "pkg:a"}, Review: "### FINDING-1: [MINOR] thing"},
		{Unit: DeepUnit{Name: "pkg:b"}, Err: errors.New("boom")},
	}
	out := BuildDeepReport("## Grade: [B]\n\n## Summary\nok", reports)
	if !strings.HasPrefix(out, "## Grade: [B]") {
		t.Fatal("synthesis must lead the report (ParseReview reads the top)")
	}
	if !strings.Contains(out, "Per-Unit Reports (appendix)") || !strings.Contains(out, "FINDING-1") {
		t.Fatal("appendix must preserve unit findings verbatim")
	}
	if !strings.Contains(out, "_Review failed: boom_") {
		t.Fatal("failed units must appear in the appendix")
	}
}
