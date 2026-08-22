package builtin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeCommitHelperSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	switch os.Getenv("BUCKLEY_FAKE_COMMIT_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "fatal: nothing to commit (fake)")
		os.Exit(3)
	case "hang":
		time.Sleep(2 * time.Minute)
	case "spam":
		blob := make([]byte, 8192)
		for i := range blob {
			blob[i] = 'x'
		}
		// Emit roughly 1MiB per stream so tests can prove truncation under
		// both configured limits and the finite default bound.
		for i := 0; i < 128; i++ {
			fmt.Println(string(blob))
			fmt.Fprintln(os.Stderr, string(blob))
		}
	default:
		if record := os.Getenv("BUCKLEY_FAKE_COMMIT_RECORD"); record != "" {
			data, _ := json.Marshal(os.Args)
			_ = os.WriteFile(record, append(data, '\n'), 0o644)
		}
		fmt.Println("fake commit ok")
	}
}
`

// newFakeCommitHelper builds a tiny standalone binary that stands in for the
// Buckley executable. Behavior is selected through BUCKLEY_FAKE_COMMIT_MODE.
func newFakeCommitHelper(t *testing.T) string {
	t.Helper()

	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable, cannot build fake commit helper: %v", err)
	}

	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create helper src dir: %v", err)
	}
	files := map[string]string{
		"go.mod":  "module fakecommit\n\ngo 1.21\n",
		"main.go": fakeCommitHelperSource,
	}
	for name, content := range files {
		path := filepath.Join(srcDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write helper %s: %v", name, err)
		}
	}

	bin := filepath.Join(t.TempDir(), "fake-buckley")
	// -buildvcs=false: the temp module is not part of any repository, so VCS
	// stamping is unnecessary and can fail in restricted environments.
	build := exec.Command(goBinary, "build", "-buildvcs=false", "-o", bin, ".")
	build.Dir = srcDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake commit helper: %v\n%s", err, out)
	}
	return bin
}

func TestCommitChangesTool_Schema(t *testing.T) {
	tool := &CommitChangesTool{}

	if tool.Name() != "commit_changes" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "commit_changes")
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Error("Description() should not be empty")
	}
	// The runtime always passes --exclusive, which refuses to commit when
	// unrelated staged files exist; the description must say so instead of
	// implying other staged files are left staged.
	for _, want := range []string{"--exclusive", "unrelated staged files"} {
		if !strings.Contains(tool.Description(), want) {
			t.Errorf("Description() = %q, want mention of %q", tool.Description(), want)
		}
	}

	params := tool.Parameters()
	if params.Type != "object" {
		t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
	}
	pathsProp, ok := params.Properties["paths"]
	if !ok {
		t.Fatal("Parameters().Properties[\"paths\"] missing")
	}
	if pathsProp.Type != "array" {
		t.Errorf("paths.Type = %q, want %q", pathsProp.Type, "array")
	}
	if pathsProp.Items == nil || pathsProp.Items.Type != "string" {
		t.Errorf("paths.Items = %+v, want string items", pathsProp.Items)
	}
	if len(params.Required) != 1 || params.Required[0] != "paths" {
		t.Errorf("Required = %v, want [paths]", params.Required)
	}
	if params.AdditionalProperties != false {
		t.Errorf("AdditionalProperties = %v, want false (explicit scoped paths only)", params.AdditionalProperties)
	}
}

func TestBuildCommitChangesArgv_ExactSafeArgv(t *testing.T) {
	exe := "/usr/local/bin/buckley"
	p1 := "/workdir/a.go"
	p2 := "/workdir/dir with space/b.txt"

	got := buildCommitChangesArgv(exe, []string{p1, p2})
	want := []string{
		exe,
		"commit",
		"--yes",
		"--push=false",
		"--minimal-output",
		"--exclusive",
		"--paths", p1,
		"--paths", p2,
		"--", p1, p2,
	}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full argv: %q)", i, got[i], want[i], got)
		}
	}

	// The configured default commit backend/model must stay authoritative:
	// no overrides may ever appear in the argv.
	forbidden := []string{"--model", "--backend", "--timeout"}
	for _, arg := range got {
		for _, f := range forbidden {
			if arg == f || strings.HasPrefix(arg, f+"=") {
				t.Errorf("argv contains forbidden override %q: %q", f, got)
			}
		}
	}
}

func TestCommitChangesTool_PathValidation(t *testing.T) {
	workDir := t.TempDir()
	parent := filepath.Dir(workDir)

	tool := &CommitChangesTool{}
	tool.SetWorkDir(workDir)

	cases := []struct {
		name       string
		params     map[string]any
		wantErrSub string
	}{
		{
			name:       "missing paths",
			params:     map[string]any{},
			wantErrSub: "paths parameter required",
		},
		{
			name:       "nil paths",
			params:     map[string]any{"paths": nil},
			wantErrSub: "paths parameter required",
		},
		{
			name:       "empty array",
			params:     map[string]any{"paths": []any{}},
			wantErrSub: "non-empty array",
		},
		{
			name:       "wrong type",
			params:     map[string]any{"paths": "a.go"},
			wantErrSub: "array of strings",
		},
		{
			name:       "non-string element",
			params:     map[string]any{"paths": []any{"a.go", 42}},
			wantErrSub: "array of strings",
		},
		{
			name:       "empty entry",
			params:     map[string]any{"paths": []any{"a.go", "   "}},
			wantErrSub: "cannot be empty",
		},
		{
			name:       "relative escape",
			params:     map[string]any{"paths": []any{filepath.Join("..", filepath.Base(workDir), "..", "outside.txt")}},
			wantErrSub: "escapes workdir",
		},
		{
			name:       "absolute outside workdir",
			params:     map[string]any{"paths": []any{filepath.Join(parent, "sibling.txt")}},
			wantErrSub: "escapes workdir",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(tc.params)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if result.Success {
				t.Fatalf("Execute succeeded, want failure: %+v", result)
			}
			if !strings.Contains(result.Error, tc.wantErrSub) {
				t.Errorf("Error = %q, want substring %q", result.Error, tc.wantErrSub)
			}
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		outside := t.TempDir()
		link := filepath.Join(workDir, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		result, err := tool.Execute(map[string]any{
			"paths": []any{filepath.Join("link", "secret.txt")},
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("symlink escape succeeded: %+v", result)
		}
		if !strings.Contains(result.Error, "escapes workdir") {
			t.Errorf("Error = %q, want symlink escape rejection", result.Error)
		}
	})
}

func TestCommitChangesTool_CanonicalizesAndDeduplicatesPaths(t *testing.T) {
	helper := newFakeCommitHelper(t)

	workDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv.json")

	tool := &CommitChangesTool{Executable: helper}
	tool.SetWorkDir(workDir)
	tool.SetEnv(map[string]string{
		"BUCKLEY_FAKE_COMMIT_RECORD": record,
	})

	result, err := tool.Execute(map[string]any{
		"paths": []any{"b.go", "./a.go", "sub/../a.go", filepath.Join(workDir, "a.go"), "b.go"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("helper did not record argv: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("decode recorded argv: %v", err)
	}

	// ./a.go, sub/../a.go, and the absolute spelling of a.go all resolve to
	// the same absolute identity and collapse into one workdir-relative
	// scope; b.go keeps its original position. Duplicates are removed
	// everywhere (flags AND trailing positional scopes), and every emitted
	// scope is relative because cmd/buckley compares them against
	// repository-relative staged names.
	want := []string{
		helper,
		"commit",
		"--yes",
		"--push=false",
		"--minimal-output",
		"--exclusive",
		"--paths", "b.go",
		"--paths", "a.go",
		"--", "b.go", "a.go",
	}
	if len(argv) != len(want) {
		t.Fatalf("recorded argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("recorded argv[%d] = %q, want %q (full: %q)", i, argv[i], want[i], argv)
		}
	}
}

// TestCommitChangesTool_PassesWorkdirRelativeScopes is the regression test
// for the staged-name matching contract: cmd/buckley's
// stagedFilesMatchingPaths compares --paths entries against
// repository-relative staged names, so canonicalPaths must emit
// workdir-relative scopes whether the caller passed relative or absolute
// inputs. Absolute argv scopes would match nothing and fail every commit.
func TestCommitChangesTool_PassesWorkdirRelativeScopes(t *testing.T) {
	helper := newFakeCommitHelper(t)

	workDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv.json")

	tool := &CommitChangesTool{Executable: helper}
	tool.SetWorkDir(workDir)
	tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_RECORD": record})

	result, err := tool.Execute(map[string]any{
		"paths": []any{
			"pkg/a.go",                          // plain relative input
			"./cmd/b.go",                        // dotted relative input
			filepath.Join(workDir, "docs/c.md"), // absolute input inside workdir
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("helper did not record argv: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("decode recorded argv: %v", err)
	}

	want := []string{
		helper,
		"commit",
		"--yes",
		"--push=false",
		"--minimal-output",
		"--exclusive",
		"--paths", filepath.Join("pkg", "a.go"),
		"--paths", filepath.Join("cmd", "b.go"),
		"--paths", filepath.Join("docs", "c.md"),
		"--",
		filepath.Join("pkg", "a.go"),
		filepath.Join("cmd", "b.go"),
		filepath.Join("docs", "c.md"),
	}
	if len(argv) != len(want) {
		t.Fatalf("recorded argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("recorded argv[%d] = %q, want %q (full: %q)", i, argv[i], want[i], argv)
		}
	}
	for _, arg := range argv[1:] {
		if filepath.IsAbs(arg) {
			t.Errorf("argv contains absolute scope %q; want workdir-relative names stagedFilesMatchingPaths can match (full: %q)", arg, argv)
		}
	}
}

// TestCommitChangesTool_UnsetWorkDirBindsToProcessCwd proves the safety
// boundary exists even when no workdir is configured: paths are validated
// against the process working directory (os.Getwd) instead of being accepted
// as arbitrary absolute paths.
func TestCommitChangesTool_UnsetWorkDirBindsToProcessCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	t.Run("absolute escape rejected", func(t *testing.T) {
		tool := &CommitChangesTool{} // deliberately no SetWorkDir

		result, err := tool.Execute(map[string]any{
			"paths": []any{filepath.Join(filepath.Dir(cwd), "outside-cwd.txt")},
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("absolute escape accepted without a configured workdir: %+v", result)
		}
		if !strings.Contains(result.Error, "escapes workdir") {
			t.Errorf("Error = %q, want escape rejection", result.Error)
		}
	})

	t.Run("relative input resolves against process cwd", func(t *testing.T) {
		helper := newFakeCommitHelper(t)
		record := filepath.Join(t.TempDir(), "argv.json")

		tool := &CommitChangesTool{Executable: helper} // still no SetWorkDir
		tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_RECORD": record})

		result, err := tool.Execute(map[string]any{"paths": []any{"commit_test.go"}})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("Execute failed: %s", result.Error)
		}

		raw, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("helper did not record argv: %v", err)
		}
		var argv []string
		if err := json.Unmarshal(raw, &argv); err != nil {
			t.Fatalf("decode recorded argv: %v", err)
		}
		found := false
		for i, arg := range argv {
			if arg == "--paths" && i+1 < len(argv) && argv[i+1] == "commit_test.go" {
				found = true
			}
		}
		if !found {
			t.Errorf("recorded argv = %q, want cwd-relative scope \"commit_test.go\"", argv)
		}
	})
}

// TestCommitChangesTool_DefaultBoundedOutputWithoutConfiguredLimit proves
// stdout and stderr have a finite default bound: with no registry maximum
// configured, output is still truncated at defaultCommitMaxOutputBytes
// instead of being captured unbounded. A stricter configured maximum is
// covered by the "bounded output" case below.
func TestCommitChangesTool_DefaultBoundedOutputWithoutConfiguredLimit(t *testing.T) {
	helper := newFakeCommitHelper(t)

	tool := &CommitChangesTool{Executable: helper}
	tool.SetWorkDir(t.TempDir())
	tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": "spam"})
	// Deliberately no SetMaxOutputBytes call: the default bound must apply.

	result, err := tool.Execute(map[string]any{"paths": []any{"a.go"}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	stdout, _ := result.Data["stdout"].(string)
	stderr, _ := result.Data["stderr"].(string)
	if len(stdout) == 0 || len(stderr) == 0 {
		t.Fatalf("expected captured output, got stdout=%d stderr=%d bytes", len(stdout), len(stderr))
	}
	if len(stdout) > defaultCommitMaxOutputBytes || len(stderr) > defaultCommitMaxOutputBytes {
		t.Errorf("unbounded default output: stdout=%d bytes stderr=%d bytes, want <=%d each",
			len(stdout), len(stderr), defaultCommitMaxOutputBytes)
	}
	if result.Data["stdout_truncated"] != true || result.Data["stderr_truncated"] != true {
		t.Errorf("truncation flags = %v/%v, want true/true under the default bound",
			result.Data["stdout_truncated"], result.Data["stderr_truncated"])
	}
	if !result.ShouldAbridge {
		t.Error("ShouldAbridge = false, want true when default truncation kicked in")
	}
}

func TestCommitChangesTool_CommandFailure(t *testing.T) {
	helper := newFakeCommitHelper(t)

	tool := &CommitChangesTool{Executable: helper}
	tool.SetWorkDir(t.TempDir())
	tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": "fail"})

	result, err := tool.Execute(map[string]any{
		"paths": []any{"a.go"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("Execute succeeded, want failure for non-zero exit")
	}
	if !strings.Contains(result.Error, "buckley commit failed") {
		t.Errorf("Error = %q, want prefix \"buckley commit failed\"", result.Error)
	}
	if !strings.Contains(result.Error, "nothing to commit") {
		t.Errorf("Error = %q, want captured stderr detail", result.Error)
	}
	if head, _ := result.Data["head"].(string); head != "" {
		t.Errorf("Data[head] = %q on failure, want empty", head)
	}
}

func TestCommitChangesTool_Timeout(t *testing.T) {
	helper := newFakeCommitHelper(t)

	tool := &CommitChangesTool{Executable: helper}
	tool.SetWorkDir(t.TempDir())
	tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": "hang"})
	tool.SetMaxExecTimeSeconds(1)

	done := make(chan struct{})
	var result *Result
	var err error
	go func() {
		defer close(done)
		result, err = tool.Execute(map[string]any{"paths": []any{"a.go"}})
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after timeout budget")
	}

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("Execute succeeded, want timeout failure")
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Errorf("Error = %q, want timeout indication", result.Error)
	}
}

func TestCommitChangesTool_SuccessExposesBoundedOutputAndHead(t *testing.T) {
	helper := newFakeCommitHelper(t)

	repo := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q")
	writeFile(t, filepath.Join(repo, "README.md"), "# test\n")
	gitRun("add", "README.md")
	gitRun("commit", "-q", "-m", "initial")
	wantHead := gitRun("rev-parse", "HEAD")

	t.Run("head and stdout", func(t *testing.T) {
		tool := &CommitChangesTool{Executable: helper}
		tool.SetWorkDir(repo)

		result, err := tool.Execute(map[string]any{"paths": []any{"README.md"}})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("Execute failed: %s", result.Error)
		}
		if got := result.Data["head"]; got != wantHead {
			t.Errorf("Data[head] = %v, want %q", got, wantHead)
		}
		if got := result.Data["stdout"]; got != "fake commit ok\n" {
			t.Errorf("Data[stdout] = %q, want captured helper output", got)
		}
		if _, ok := result.Data["stderr"]; !ok {
			t.Error("Data[stderr] missing")
		}
		if _, ok := result.Data["stdout_truncated"]; ok {
			t.Error("Data[stdout_truncated] set without truncation")
		}
	})

	t.Run("bounded output", func(t *testing.T) {
		tool := &CommitChangesTool{Executable: helper}
		tool.SetWorkDir(repo)
		tool.SetMaxOutputBytes(1024)
		tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": "spam"})

		result, err := tool.Execute(map[string]any{"paths": []any{"README.md"}})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("Execute failed: %s", result.Error)
		}
		stdout, _ := result.Data["stdout"].(string)
		stderr, _ := result.Data["stderr"].(string)
		if len(stdout) > 1024 || len(stderr) > 1024 {
			t.Errorf("unbounded output: stdout=%d bytes stderr=%d bytes", len(stdout), len(stderr))
		}
		if result.Data["stdout_truncated"] != true || result.Data["stderr_truncated"] != true {
			t.Errorf("truncation flags = %v/%v, want true/true", result.Data["stdout_truncated"], result.Data["stderr_truncated"])
		}
		if !result.ShouldAbridge {
			t.Error("ShouldAbridge = false, want true when output was truncated")
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
