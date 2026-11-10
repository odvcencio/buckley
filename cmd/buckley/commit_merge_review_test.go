package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

const cleanMergeBody = `{"action":"merge","scope":"x","subject":"x","body":["Bring in the upstream changes"]}`

// Review of #469, finding 1: Buckley writes the branch names into the committed
// header, so the leak checks must read the header, not a placeholder.
func TestMergeCommitDefinition_ChecksTheHeaderThatIsCommitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"f.txt": "a\nb\n"})
	var prompts []string
	framework := oneshot.NewFramework(scriptedInvoker{answers: []string{cleanMergeBody}, prompts: &prompts}, nil)

	def := mergeCommitDefinition{source: "feature/person@example.com", target: "main"}
	if _, err := framework.Run(context.Background(), def, oneshot.RunOpts{}); err == nil {
		t.Fatal("a branch name with an email address passed the leak checks and would be committed in the header")
	}
	prompts = nil
	if _, err := framework.Run(context.Background(), mergeCommitDefinition{source: "feature", target: "main"}, oneshot.RunOpts{}); err != nil {
		t.Fatalf("a clean branch name failed: %v", err)
	}
}

// Finding 3: a tool call with the arguments "null" must be a validation error.
func TestMergeCommitDefinition_NullArgumentsAreRejectedNotAPanic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"f.txt": "a\nb\n"})
	var prompts []string
	framework := oneshot.NewFramework(scriptedInvoker{answers: []string{"null", cleanMergeBody}, prompts: &prompts}, nil)
	if _, err := framework.Run(context.Background(), mergeCommitDefinition{source: "feature", target: "main"}, oneshot.RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("model calls = %d, want the null answer retried once", len(prompts))
	}
	def := mergeCommitDefinition{source: "feature", target: "main"}
	if err := def.Validate([]byte("null")); err == nil {
		t.Error("Validate accepted null arguments")
	}
}

// Finding 5: git can use another comment character; a missing record is not a
// clean merge.
func TestMergeConflictFilesKnown_CommentCharAndMissingRecord(t *testing.T) {
	dir := t.TempDir()
	if files, known := mergeConflictFilesKnown(dir); known || files != nil {
		t.Fatalf("a missing MERGE_MSG gave files=%v known=%v, want unknown", files, known)
	}
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "MERGE_MSG"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Merge branch 'x'\n\n; Conflicts:\n;\tpath/one\n;\tpath/two\n")
	if files, known := mergeConflictFilesKnown(dir); !known || strings.Join(files, ",") != "path/one,path/two" {
		t.Fatalf("with commentChar=; got files=%v known=%v", files, known)
	}
	write("Merge branch 'x'\n\n# Conflicts:\n#\tpath/one\n")
	if files, _ := mergeConflictFilesKnown(dir); len(files) != 1 || files[0] != "path/one" {
		t.Fatalf("with # got %v", files)
	}
	write("Merge branch 'x'\n")
	if files, known := mergeConflictFilesKnown(dir); !known || len(files) != 0 {
		t.Fatalf("a clean MERGE_MSG gave files=%v known=%v", files, known)
	}

	ctx := &oneshot.Context{Sources: map[string]string{}}
	unknown := mergeCommitDefinition{source: "a", target: "b"}.BuildPrompt(ctx)
	if strings.Contains(unknown, "There were no conflicts") {
		t.Errorf("unknown conflict information was stated as a clean merge:\n%s", unknown)
	}
}
