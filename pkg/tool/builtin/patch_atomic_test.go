package builtin_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func requireGNUPatch(t *testing.T) {
	t.Helper()

	patchPath, err := exec.LookPath("patch")
	if err != nil {
		t.Skipf("skipping PatchFileTool integration test: GNU patch executable is unavailable: %v", err)
	}

	version, err := exec.Command(patchPath, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "GNU patch") {
		t.Skipf("skipping PatchFileTool integration test: GNU patch is unavailable (patch=%q, version=%q, err=%v)", patchPath, strings.TrimSpace(string(version)), err)
	}
}

func TestPatchFileTool_RejectsPatchWithoutPartialEdits(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	target := filepath.Join(workDir, "victim.txt")
	original := []byte("alpha\nbeta\ngamma\ndelta\n")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	patch := "--- victim.txt\n" +
		"+++ victim.txt\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-alpha\n" +
		"+ALPHA\n" +
		"@@ -4,1 +4,1 @@\n" +
		"-not-present\n" +
		"+DELTA\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch})
	if err != nil {
		t.Fatalf("PatchFileTool returned unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected patch failure for a rejected later hunk, got %+v", result)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after rejected patch: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("rejected patch changed target: got %q, want original %q", got, original)
	}

	if artifacts := patchArtifacts(t, workDir); len(artifacts) != 0 {
		t.Fatalf("rejected patch left artifacts: %v", artifacts)
	}
}

func TestPatchFileTool_AppliesValidMultiFilePatch(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	files := map[string][]byte{
		"one.txt":     []byte("first\n"),
		"dir/two.txt": []byte("second\n"),
	}
	for name, content := range files {
		path := filepath.Join(workDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	patch := "--- a/one.txt\n" +
		"+++ b/one.txt\n" +
		"@@ -1 +1 @@\n" +
		"-first\n" +
		"+first updated\n" +
		"--- a/dir/two.txt\n" +
		"+++ b/dir/two.txt\n" +
		"@@ -1 +1 @@\n" +
		"-second\n" +
		"+second updated\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch, "strip": 1})
	if err != nil {
		t.Fatalf("PatchFileTool returned unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected valid multi-file patch to succeed: %s", result.Error)
	}
	if got := result.Data["strip"]; got != 1 {
		t.Fatalf("result strip = %#v, want 1", got)
	}

	want := map[string][]byte{
		"one.txt":     []byte("first updated\n"),
		"dir/two.txt": []byte("second updated\n"),
	}
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(workDir, name))
		if err != nil {
			t.Fatalf("read patched %s: %v", name, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("%s = %q, want %q", name, got, expected)
		}
	}

	if artifacts := patchArtifacts(t, workDir); len(artifacts) != 0 {
		t.Fatalf("successful patch left artifacts: %v", artifacts)
	}
}

func TestPatchFileTool_AutoStripsGitStylePatchWhenStripOmitted(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	target := filepath.Join(workDir, "proration.go")
	original := []byte("package billing\n\nfunc Daily() int { return 1 }\n")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	patch := "diff --git a/proration.go b/proration.go\n" +
		"--- a/proration.go\n" +
		"+++ b/proration.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package billing\n" +
		" \n" +
		"-func Daily() int { return 1 }\n" +
		"+func Daily() int { return 2 }\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch})
	if err != nil {
		t.Fatalf("PatchFileTool returned unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected git-style patch without explicit strip to succeed: %s", result.Error)
	}
	if got := result.Data["strip"]; got != 1 {
		t.Fatalf("result strip = %#v, want 1", got)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	if want := []byte("package billing\n\nfunc Daily() int { return 2 }\n"); !bytes.Equal(got, want) {
		t.Fatalf("patched file = %q, want %q", got, want)
	}

	if artifacts := patchArtifacts(t, workDir); len(artifacts) != 0 {
		t.Fatalf("successful auto-strip patch left artifacts: %v", artifacts)
	}
}

func TestPatchFileTool_ExplicitStripZeroDisablesAutoStrip(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	target := filepath.Join(workDir, "proration.go")
	original := []byte("package billing\n\nfunc Daily() int { return 1 }\n")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	patch := "diff --git a/proration.go b/proration.go\n" +
		"--- a/proration.go\n" +
		"+++ b/proration.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package billing\n" +
		" \n" +
		"-func Daily() int { return 1 }\n" +
		"+func Daily() int { return 2 }\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch, "strip": 0})
	if err != nil {
		t.Fatalf("PatchFileTool returned unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected explicit strip=0 to preserve caller choice and fail against a/ path")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file after rejected patch: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("explicit strip=0 failure changed file: got %q, want %q", got, original)
	}

	if artifacts := patchArtifacts(t, workDir); len(artifacts) != 0 {
		t.Fatalf("rejected explicit-strip patch left artifacts: %v", artifacts)
	}
}

func TestPatchFileTool_AutoStripHandlesCreateDeleteQuotedAndTimestampPaths(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	for name, content := range map[string]string{
		"obsolete.txt": "obsolete\n",
		"timed.txt":    "before\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	patch := "diff --git a/obsolete.txt b/obsolete.txt\n" +
		"deleted file mode 100644\n" +
		"--- a/obsolete.txt\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-obsolete\n" +
		"diff --git \"a/name with spaces.txt\" \"b/name with spaces.txt\"\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ \"b/name with spaces.txt\"\n" +
		"@@ -0,0 +1 @@\n" +
		"+created\n" +
		"diff --git a/timed.txt b/timed.txt\n" +
		"--- a/timed.txt\t2026-09-03 12:00:00 +0000\n" +
		"+++ b/timed.txt\t2026-09-03 12:01:00 +0000\n" +
		"@@ -1 +1 @@\n" +
		"-before\n" +
		"+after\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch})
	if err != nil || !result.Success {
		t.Fatalf("mixed auto-strip patch = %+v, %v", result, err)
	}
	if got := result.Data["strip"]; got != 1 {
		t.Fatalf("result strip = %#v, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists or stat failed unexpectedly: %v", err)
	}
	for name, want := range map[string]string{
		"name with spaces.txt": "created\n",
		"timed.txt":            "after\n",
	} {
		got, err := os.ReadFile(filepath.Join(workDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if artifacts := patchArtifacts(t, workDir); len(artifacts) != 0 {
		t.Fatalf("mixed patch left artifacts: %v", artifacts)
	}
}

func TestPatchFileTool_HeaderLikeHunkContentDoesNotAffectInference(t *testing.T) {
	requireGNUPatch(t)

	workDir := t.TempDir()
	target := filepath.Join(workDir, "markers.txt")
	if err := os.WriteFile(target, []byte("alpha\n-- old marker\n++ old marker\nomega\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/markers.txt\n" +
		"+++ b/markers.txt\n" +
		"@@ -1,4 +1,4 @@\n" +
		" alpha\n" +
		"--- old marker\n" +
		"+++ new marker\n" +
		" ++ old marker\n" +
		" omega\n"
	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch})
	if err != nil || !result.Success {
		t.Fatalf("header-like content patch = %+v, %v", result, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "alpha\n++ new marker\n++ old marker\nomega\n" {
		t.Fatalf("patched content = %q, %v", got, err)
	}
}

func TestPatchFileTool_RejectsGitRenameWithoutPartialEdit(t *testing.T) {
	workDir := t.TempDir()
	oldPath := filepath.Join(workDir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/old.txt b/new.txt\n" +
		"similarity index 50%\n" +
		"rename from old.txt\n" +
		"rename to new.txt\n" +
		"--- a/old.txt\n" +
		"+++ b/new.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	result, err := tool.Execute(map[string]any{"patch": patch})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "rename") {
		t.Fatalf("rename result = %+v, want explicit unsupported rejection", result)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil || string(got) != "old\n" {
		t.Fatalf("old path = %q, %v; want unchanged", got, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new path unexpectedly exists or stat failed: %v", err)
	}
}

func TestPatchFileTool_RejectsTargetsOutsideWorkdir(t *testing.T) {
	container := t.TempDir()
	workDir := filepath.Join(container, "work")
	outsideDir := filepath.Join(container, "outside")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(container, "victim.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0644); err != nil {
		t.Fatal(err)
	}
	linkedTarget := filepath.Join(outsideDir, "linked.txt")
	if err := os.WriteFile(linkedTarget, []byte("linked\n"), 0644); err != nil {
		t.Fatal(err)
	}
	symlinkErr := os.Symlink(outsideDir, filepath.Join(workDir, "link"))
	spacedSymlinkErr := os.Symlink(outsideDir, filepath.Join(workDir, " link"))

	tool := &builtin.PatchFileTool{}
	tool.SetWorkDir(workDir)
	for _, tc := range []struct {
		name  string
		patch string
		strip int
	}{
		{
			name:  "parent traversal",
			patch: "--- a/../victim.txt\n+++ b/../victim.txt\n@@ -1 +1 @@\n-outside\n+changed\n",
			strip: 1,
		},
		{
			name:  "absolute path",
			patch: "--- " + outside + "\n+++ " + outside + "\n@@ -1 +1 @@\n-outside\n+changed\n",
			strip: 0,
		},
		{
			name:  "windows separator traversal",
			patch: "--- a/..\\victim.txt\n+++ b/..\\victim.txt\n@@ -1 +1 @@\n-outside\n+changed\n",
			strip: 1,
		},
		{
			name:  "symlink directory",
			patch: "--- a/link/linked.txt\n+++ b/link/linked.txt\n@@ -1 +1 @@\n-linked\n+changed\n",
			strip: 1,
		},
		{
			name:  "quoted leading-space symlink directory",
			patch: "--- \"a/ link/linked.txt\"\n+++ \"b/ link/linked.txt\"\n@@ -1 +1 @@\n-linked\n+changed\n",
			strip: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink directory" && symlinkErr != nil {
				t.Skipf("symlink unavailable: %v", symlinkErr)
			}
			if tc.name == "quoted leading-space symlink directory" && spacedSymlinkErr != nil {
				t.Skipf("symlink unavailable: %v", spacedSymlinkErr)
			}
			result, err := tool.Execute(map[string]any{"patch": tc.patch, "strip": tc.strip})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Success || (!strings.Contains(result.Error, "unsafe") && !strings.Contains(result.Error, "outside")) {
				t.Fatalf("outside patch result = %+v, want bounded rejection", result)
			}
		})
	}
	for path, want := range map[string]string{outside: "outside\n", linkedTarget: "linked\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("outside target %s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func patchArtifacts(t *testing.T, root string) []string {
	t.Helper()

	var artifacts []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".rej") || strings.HasSuffix(entry.Name(), ".orig") {
			artifacts = append(artifacts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk patch workspace: %v", err)
	}
	sort.Strings(artifacts)
	return artifacts
}
