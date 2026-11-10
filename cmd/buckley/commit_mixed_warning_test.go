package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestIsTestData(t *testing.T) {
	for path, want := range map[string]bool{
		"internal/ccore/testdata/golden/json-object.json": true,
		"web/__snapshots__/app.test.js.snap":              true,
		"cmd/tool/usage.golden":                           true,
		"dist/bundle.min.js":                              false,
		"testdata.go":                                     false,
		"pkg/testdatax/a.json":                            false,
	} {
		if got := isTestData(path); got != want {
			t.Errorf("isTestData(%q) = %v, want %v", path, got, want)
		}
	}
}

// A commit of 19 source files and three golden JSON files was told to
// "commit them separately". Golden files hold one long line, so the diff
// reader calls them minified, but they belong with the change they test.
func TestGeneratedCommitRuntime_MixedWarning(t *testing.T) {
	longLine := `{"trace":"` + strings.Repeat("x", 2500) + `"}` + "\n"
	cases := []struct {
		name     string
		files    map[string]string
		opts     commitCommandOptions
		wantWarn string
	}{
		{
			name:  "golden test data beside source does not warn",
			files: map[string]string{"internal/ccore/lexer.go": "package ccore\n", "internal/ccore/testdata/golden/json-object.json": longLine},
		},
		{
			name:     "build output beside source warns and names the files",
			files:    map[string]string{"main.go": "package main\n", "dist/bundle.min.js": "var a=1;\n", "internal/ccore/testdata/golden/a.json": longLine},
			wantWarn: "commit mixes 1 generated file(s) (dist/bundle.min.js) with 1 source file(s)",
		},
		{
			name:  "--allow-mixed acknowledges it",
			files: map[string]string{"main.go": "package main\n", "dist/bundle.min.js": "var a=1;\n"},
			opts:  commitCommandOptions{allowMixed: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stageRepo(t, tc.files)
			// Every case is a mixed commit as the diff reader sees it, so a
			// missing warning comes from the rule under test.
			if stats, err := oneshot.StagedDiffStats(nil); err != nil || !stats.Mixed() {
				t.Fatalf("StagedDiffStats = %+v, %v; want generated and source files", stats, err)
			}
			out := captureTermOut(t, func() {
				if rt := generatedCommitRuntime(tc.opts, repoOpState{Kind: opNone}); rt != nil {
					t.Error("a commit with source files must use the model")
				}
			})
			if tc.wantWarn == "" {
				if strings.Contains(out, "commit mixes") {
					t.Errorf("unexpected warning: %s", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantWarn) || !strings.Contains(out, "--allow-mixed") {
				t.Errorf("warning = %q, want it to contain %q and name --allow-mixed", out, tc.wantWarn)
			}
		})
	}
}

func TestPreviewPaths(t *testing.T) {
	if got := previewPaths([]string{"a", "b"}, 3); got != "a, b" {
		t.Errorf("previewPaths = %q", got)
	}
	if got := previewPaths([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c and 2 more" {
		t.Errorf("previewPaths = %q", got)
	}
}

// Review of #469, finding 6: a commit of golden files alone needs a model
// written message; the fixed "regenerate ... no hand-written changes" text is
// for build output.
func TestGeneratedCommit_GoldenFilesAloneAreNotGenerated(t *testing.T) {
	stats := oneshot.DiffStats{Files: 2, GeneratedPaths: []string{"pkg/x/testdata/a.golden.json", "pkg/x/b.golden"}}
	if got := commands.GeneratedCommit(withoutTestDataGenerated(stats)); got != nil {
		t.Fatalf("golden files alone got the fixed message: %+v", got)
	}
	mixed := oneshot.DiffStats{Files: 2, GeneratedPaths: []string{"dist/app.min.js", "pkg/x/testdata/a.golden.json"}}
	moved := withoutTestDataGenerated(mixed)
	if len(moved.GeneratedPaths) != 1 || len(moved.SourcePaths) != 1 {
		t.Fatalf("moved = %+v", moved)
	}
	build := oneshot.DiffStats{Files: 1, GeneratedPaths: []string{"dist/app.min.js"}}
	if commands.GeneratedCommit(withoutTestDataGenerated(build)) == nil {
		t.Fatal("build output alone must keep the fixed message")
	}
}
