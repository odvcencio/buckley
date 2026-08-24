package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

const testMITLicense = `MIT License

Copyright (c) 2026 Test Holder

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

func TestFormTrackedOSSPrompt_UsesExactCommittedBlob(t *testing.T) {
	root, head := newTrackedPromptRepository(t)
	rule, prompt, err := formTrackedOSSPrompt(context.Background(), root, head, ".buckley/tasks/task.md")
	if err != nil {
		t.Fatalf("formTrackedOSSPrompt() error = %v", err)
	}
	if got, want := string(prompt), "return a unified diff\n"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	binding, err := rule.ClaimForDispatch(context.Background(), prompt)
	if err != nil {
		t.Fatalf("ClaimForDispatch() error = %v", err)
	}
	if binding == ([32]byte{}) {
		t.Fatal("ClaimForDispatch() returned an empty binding")
	}
}

func TestFormTrackedOSSPrompt_RejectsExternalAndUntrackedPaths(t *testing.T) {
	root, head := newTrackedPromptRepository(t)
	untracked := filepath.Join(root, ".buckley", "tasks", "untracked.md")
	if err := os.WriteFile(untracked, []byte("untracked\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(untracked) error = %v", err)
	}

	for _, promptPath := range []string{untracked, ".buckley/tasks/untracked.md"} {
		if _, _, err := formTrackedOSSPrompt(context.Background(), root, head, promptPath); err == nil {
			t.Fatalf("formTrackedOSSPrompt(%q) succeeded, want rejection", promptPath)
		}
	}
}

func newTrackedPromptRepository(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "config", "user.email", "buckley-tests@example.invalid")
	runGitTest(t, root, "config", "user.name", "Buckley Tests")
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(testMITLicense), 0o600); err != nil {
		t.Fatalf("WriteFile(LICENSE) error = %v", err)
	}
	taskDir := filepath.Join(root, ".buckley", "tasks")
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(taskDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("return a unified diff\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(task) error = %v", err)
	}
	runGitTest(t, root, "add", "LICENSE", ".buckley/tasks/task.md")
	runGitTest(t, root, "commit", "-q", "-m", "test fixture")
	head := strings.TrimSpace(runGitTest(t, root, "rev-parse", "HEAD"))
	return root, head
}

func runGitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestWriteExclusiveOutput_CreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.patch")
	if err := writeExclusiveOutput(path, []byte("patch text")); err != nil {
		t.Fatalf("writeExclusiveOutput() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "patch text" {
		t.Fatalf("content = %q, want %q", content, "patch text")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestWriteExclusiveOutput_ExistingFileIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "response.patch")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := writeExclusiveOutput(path, []byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("writeExclusiveOutput() error = %v, want existing-output error", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "existing" {
		t.Fatalf("content = %q, want existing content", content)
	}
}

func TestWriteExclusiveOutput_ExistingSymlinkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "response.patch")
	if err := os.WriteFile(target, []byte("target content"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Symlink() is unavailable: %v", err)
	}

	err := writeExclusiveOutput(path, []byte("replacement"))
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("writeExclusiveOutput() error = %v, want existing-output error", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "target content" {
		t.Fatalf("target content = %q, want original content", content)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("output path is no longer a symlink")
	}
}

func TestOxAlphaPatchText_NilResponse(t *testing.T) {
	content, err := oxAlphaPatchText(nil)
	if err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("oxAlphaPatchText(nil) = %q, %v; want nil-response error", content, err)
	}
}

func TestOxAlphaPatchText_RecoversReasoningDetails(t *testing.T) {
	response := &model.ChatResponse{Choices: []model.Choice{{
		Message: model.Message{ReasoningDetails: []model.ReasoningDetail{
			{Text: "first"},
			{Summary: "second"},
		}},
	}}}

	content, err := oxAlphaPatchText(response)
	if err != nil {
		t.Fatalf("oxAlphaPatchText() error = %v", err)
	}
	if content != "first\nsecond\n" {
		t.Fatalf("content = %q, want recovered reasoning details", content)
	}
}

func TestValidatePatchResponse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "applicable diff", content: applicableDiff},
		{name: "fenced prose prefix", content: "```diff\n" + applicableDiff + "```\n", wantErr: true},
		{name: "missing final newline", content: strings.TrimSuffix(applicableDiff, "\n"), wantErr: true},
		{
			name:    "truncated hunk",
			content: strings.Replace(applicableDiff, "+return an applicable unified diff\n", "", 1),
			wantErr: true,
		},
		{
			name:    "wrong hunk counts",
			content: strings.Replace(applicableDiff, "@@ -1 +1 @@", "@@ -1,2 +1,2 @@", 1),
			wantErr: true,
		},
		{
			name:    "nonexistent context",
			content: strings.Replace(applicableDiff, "-return a unified diff\n", "-absent line\n", 1),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := newTrackedPromptRepository(t)
			err := validatePatchResponse(context.Background(), root, []byte(tc.content))
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePatchResponse() error = %v, wantErr %v", err, tc.wantErr)
			}
			if status := runGitTest(t, root, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
				t.Fatalf("validator changed worktree: %q", status)
			}
		})
	}
}

const applicableDiff = `diff --git a/.buckley/tasks/task.md b/.buckley/tasks/task.md
--- a/.buckley/tasks/task.md
+++ b/.buckley/tasks/task.md
@@ -1 +1 @@
-return a unified diff
+return an applicable unified diff
`
