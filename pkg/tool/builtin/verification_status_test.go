package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerificationCommandRejection_WrappersAndReasons(t *testing.T) {
	for _, command := range []string{
		"nice -n 10 go test ./...",
		"GOWORK=off nice -n 10 go vet ./...",
		"nice -n 10 env GOWORK=off go test ./...",
		"env GOWORK=off go test ./...",
		"nice -n 0 make check",
		"nice -n 19 cargo test",
		"env GOWORK=off nice -n 10 go test ./...",
		"GOWORK=off env CGO_ENABLED=0 go test ./...",
	} {
		if reason := VerificationCommandRejection(command); reason != "" {
			t.Errorf("%q rejected: %s", command, reason)
		}
	}

	for _, tc := range []struct {
		command string
		want    string
	}{
		{`test "$(cat hello.txt)" = hi`, "test is not an accepted check"},
		{"git diff --check HEAD^ HEAD", "git is not an accepted check"},
		{"python -m unittest", "pytest"},
		{"python3 -c 'print(1)'", "pytest"},
		{"pytest /tmp/test_hello_artifact.py", "is absolute or leaves the workspace"},
		{"go test /tmp/hello_verify_test.go", "is absolute or leaves the workspace"},
		{"go test ./... && echo ok", `"&"`},
		{"go test ./... | tee out.txt", `"|"`},
		{"make", "make needs one of the targets"},
		{"go run ./cmd/tool", "go needs one of build, vet, or test"},
		{"nice -n 20 go test ./...", "nice is not an accepted check"},
		{"nice -n -5 go test ./...", "nice is not an accepted check"},
		{"nice go test ./...", "nice is not an accepted check"},
		{"nice -n 10 GOWORK=off go test ./...", "GOWORK=... is not accepted here"},
		{"env go test ./...", "env is not an accepted check"},
		{"true", "proves nothing"},
		{"   ", "empty"},
		{"go test -exec true ./...", "can run another program"},
		{"go test -n ./...", "dry run"},
		{"pytest --collect-only", "can run another program, read outside the workspace, or run no check"},
	} {
		got := VerificationCommandRejection(tc.command)
		if got == "" {
			t.Errorf("%q accepted, want a rejection containing %q", tc.command, tc.want)
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q rejection = %q, want it to contain %q", tc.command, got, tc.want)
		}
		if IsVerificationCommand(tc.command) {
			t.Errorf("IsVerificationCommand(%q) = true, want false", tc.command)
		}
	}
}

func TestVerificationCommandRejection_NeverEchoesEnvValues(t *testing.T) {
	for _, command := range []string{
		"FAKE_API_KEY=sk-live-notreal123 go test ./...",
		"env PYTHONDONTWRITEBYTECODE=1 pytest -p no:cacheprovider",
		"TOKEN=abc123 pytest",
	} {
		got := VerificationCommandRejection(command)
		if got == "" {
			t.Fatalf("%q accepted", command)
		}
		for _, secret := range []string{"sk-live-notreal123", "abc123"} {
			if strings.Contains(got, secret) {
				t.Errorf("%q rejection echoes a value: %q", command, got)
			}
		}
	}
}

func TestVerificationCommandHelp_NamesEveryPrefixAndWrapper(t *testing.T) {
	help := VerificationCommandHelp()
	for _, prefix := range verificationShellCommandPrefixes {
		if !strings.Contains(help, prefix) {
			t.Errorf("help omits accepted prefix %q: %s", prefix, help)
		}
	}
	for _, want := range []string{"GOWORK=off", "nice -n", "absolute paths"} {
		if !strings.Contains(help, want) {
			t.Errorf("help omits %q: %s", want, help)
		}
	}
}

func TestWorkspaceVerificationTool_SchemaAndDescriptionStateTheContract(t *testing.T) {
	tool := NewWorkspaceVerificationTool(&ShellCommandTool{})
	description := tool.Description()
	for _, want := range []string{"current workspace", "go test", "make check", "pytest", "nice -n", "run_shell", "unverified"} {
		if !strings.Contains(description, want) {
			t.Errorf("description omits %q: %s", want, description)
		}
	}
	schema := tool.Parameters()
	if _, ok := schema.Properties["interactive"]; ok {
		t.Error("run_verification must not offer an interactive mode")
	}
	command, ok := schema.Properties["command"]
	if !ok {
		t.Fatal("run_verification has no command parameter")
	}
	if command.Description == (&ShellCommandTool{}).Parameters().Properties["command"].Description {
		t.Errorf("command description is the generic run_shell text: %q", command.Description)
	}
	for _, want := range []string{"go test ./...", "make check", "pytest"} {
		if !strings.Contains(command.Description, want) {
			t.Errorf("command description omits %q: %q", want, command.Description)
		}
	}
	// The shared shell schema must stay untouched.
	if (&ShellCommandTool{}).Parameters().Properties["command"].Description != "Shell command to execute" {
		t.Error("run_shell command description changed")
	}
}

func TestWorkspaceVerificationTool_RejectionIsNotRunAndExplainsItself(t *testing.T) {
	tool := NewWorkspaceVerificationTool(&ShellCommandTool{})
	result, err := tool.ExecuteWithContext(context.Background(), map[string]any{"command": `test "$(cat hello.txt)" = hi`})
	if err != nil || result == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Success {
		t.Fatal("a rejected call reported success")
	}
	if status, _ := result.Data[VerificationStatusKey].(string); status != VerificationStatusRejected {
		t.Fatalf("status = %q, want %q; data=%v", status, VerificationStatusRejected, result.Data)
	}
	for _, want := range []string{"nothing was executed", "not a test failure", "test is not an accepted check", "go test", "nice -n"} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("error omits %q: %s", want, result.Error)
		}
	}
	if reason := VerificationUnavailableReason(result); reason == "" {
		t.Error("a rejected call must read as unavailable, not as a failed check")
	}
}

// The workspace tool runs the live tree, not a snapshot: an edit made after the
// first call is visible to the second.
func TestWorkspaceVerificationTool_SeesTheLiveWorkspace(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make unavailable")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"target.txt": "before\n",
		"Makefile":   "check:\n\t@test \"$$(cat target.txt)\" = after\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shell := &ShellCommandTool{}
	shell.SetWorkDir(root)
	tool := NewWorkspaceVerificationTool(shell)

	before, err := tool.ExecuteWithContext(context.Background(), map[string]any{"command": "make check"})
	if err != nil || before.Success {
		t.Fatalf("check passed before the edit: %+v err=%v", before, err)
	}
	if reason := VerificationUnavailableReason(before); reason != "" {
		t.Fatalf("a real failing check read as unavailable (%q): %+v", reason, before)
	}

	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := tool.ExecuteWithContext(context.Background(), map[string]any{"command": "make check"})
	if err != nil || !after.Success {
		t.Fatalf("check did not see the edit: %+v err=%v", after, err)
	}
}

func TestVerificationUnavailableReason(t *testing.T) {
	shellResult := func(command string, exit int, stdout, stderr string) *Result {
		return &Result{
			Success: exit == 0,
			Error:   fmt.Sprintf("command exited with code %d", exit),
			Data:    map[string]any{"command": command, "exit_code": exit, "stdout": stdout, "stderr": stderr},
		}
	}
	for _, tc := range []struct {
		name   string
		result *Result
		want   bool
	}{
		{"pass", &Result{Success: true, Data: map[string]any{"exit_code": 0}}, false},
		{"nil", nil, false},
		{"typed rejected", &Result{Data: verificationNotRun(VerificationStatusRejected, "x")}, true},
		{"typed unavailable", &Result{Data: verificationNotRun(VerificationStatusUnavailable, "x")}, true},
		{"command not found", shellResult("go test ./...", 127, "", "bash: go: command not found"), true},
		{"command not found under nice", shellResult("nice -n 10 go test ./...", 127, "", "bash: line 1: nice: command not found"), true},
		{"command not executable", shellResult("make check", 126, "", "bash: /usr/bin/make: Permission denied"), true},
		{"no go.mod", shellResult("go test ./...", 1, "", "go: go.mod file not found in current directory or any parent directory; see 'go help modules'"), true},
		{"go under nice and env", shellResult("nice -n 10 env GOWORK=off go vet ./...", 1, "", "go: go.mod file not found in current directory or any parent directory; see 'go help modules'"), true},
		{"golangci-lint without go.mod", shellResult("golangci-lint run", 3, "", "go: go.mod file not found in current directory or any parent directory; see 'go help modules'"), true},
		{"run_tests go result", &Result{Error: "test command exited with code 1", Data: map[string]any{"framework": "go", "exit_code": 1, "output": "go: go.mod file not found in current directory or any parent directory; see 'go help modules'"}}, true},
		{"go test wildcard with no module", shellResult("go test ./...", 1, "FAIL\t./... [setup failed]\nFAIL", "# ./...\npattern ./...: directory prefix . does not contain main module or its selected dependencies"), true},
		{"go vet wildcard with no module", shellResult("go vet ./...", 1, "", "pattern ./...: directory prefix . does not contain main module or its selected dependencies"), true},
		{"golangci-lint with no module", shellResult("golangci-lint run", 3, "", "level=error msg=\"[linters_context] typechecking error: pattern ./...: directory prefix . does not contain main module or its selected dependencies\""), true},
		{"go toolchain mismatch", shellResult("go test ./...", 1, "", "go: go.mod requires go >= 1.99 (running go 1.24.0; GOTOOLCHAIN=local)"), true},
		{"no cargo manifest", shellResult("cargo test", 101, "", "error: could not find `Cargo.toml` in `/x` or any parent directory"), true},
		{"npm missing script", shellResult("npm test", 1, "", "npm error Missing script: \"test\""), true},
		{"npm init default test script", shellResult("npm test", 1, "> echo \"Error: no test specified\" && exit 1\n\nError: no test specified", ""), true},
		{"npm no package.json", shellResult("npm run build", 254, "", "npm error enoent Could not read package.json"), true},
		{"make no makefile", shellResult("make check", 2, "", "make: *** No targets specified and no makefile found.  Stop."), true},
		{"make no target", shellResult("make test", 2, "", "make: *** No rule to make target 'test'.  Stop."), true},
		{"pytest no tests", shellResult("pytest", 5, "no tests ran in 0.01s", ""), true},
		{"pytest no tests with the banner", shellResult("python3 -m pytest -q", 5, "============================ no tests ran in 0.01s ============================", ""), true},
		{"pytest collected nothing", shellResult("pytest", 5, "collected 0 items\n\n============================ no tests ran in 0.01s ============================", ""), true},
		{"pytest is not installed", shellResult("python -m pytest", 1, "", "/usr/bin/python: No module named pytest"), true},
		{"jest found no tests", shellResult("npm test", 1, "No tests found, exiting with code 1\n", ""), true},
		{"npm enoent uppercase format", shellResult("npm test", 254, "", "npm ERR! enoent ENOENT: no such file or directory, open '/x/package.json'"), true},
		{"npm missing script old format", shellResult("npm run lint", 1, "", "npm ERR! missing script: lint"), true},
		{"sandbox", &Result{Error: "sandbox blocked command: rm"}, true},
		// Real failures stay failures.
		{"go test fails", shellResult("go test ./...", 1, "--- FAIL: TestX\nFAIL", ""), false},
		{"go test fails on a missing binary the test ran", shellResult("go test ./...", 1, "--- FAIL: TestRun\nexec: \"tool\": executable file not found in $PATH\nFAIL", ""), false},
		{"go test prints missing script", shellResult("go test ./...", 1, "--- FAIL: TestNpm\nmissing script\nFAIL", ""), false},
		{"go test prints go.mod not found inside a failing test", shellResult("go test ./...", 1, "=== RUN   TestTool\n--- FAIL: TestTool (0.00s)\n    tool_test.go:9: go: go.mod file not found in current directory or any parent directory\nFAIL\nFAIL\texample.com/tool\t0.003s", ""), false},
		{"go test prints go.mod not found and another package passed", shellResult("go test ./...", 1, "ok  \texample.com/a\t0.002s\n", "go: go.mod file not found in current directory"), false},
		{"npm script runs, fails, and prints missing script", shellResult("npm test", 1, "> pkg@1.0.0 test\n> node check.js\n\ncheck failed: missing script in config\n", ""), false},
		{"npm script runs an inner npm that lacks a script", shellResult("npm test", 1, "> pkg@1.0.0 test\n> node check.js\n", "npm error Missing script: \"lint\"\nnpm error code ELIFECYCLE\nnpm error Lifecycle script `test` failed with error:"), false},
		{"npm script prints no test specified", shellResult("npm test", 1, "> pkg@1.0.0 test\n> node check.js\n\nno test specified in config\n", ""), false},
		{"jest words inside a failing script", shellResult("npm test", 1, "> pkg@1.0.0 test\n> node check.js\n\nthe log says: No tests found, exiting with code 1\n", ""), false},
		{"go test build failure keeps its FAIL line", shellResult("go test ./...", 1, "FAIL\texample.com/x [build failed]\nFAIL", "pattern ./...: directory prefix . does not contain main module or its selected dependencies"), false},
		{"indented go message", shellResult("go vet ./...", 1, "", "    tool_test.go:9: go: go.mod file not found in current directory or any parent directory"), false},
		{"recursive make lacks a target", shellResult("make test", 2, "", "make[1]: *** No rule to make target 'test'.  Stop.\nmake: *** [Makefile:2: test] Error 2"), false},
		{"make lacks a source file named test", shellResult("make check", 2, "", "make: *** No rule to make target 'test', needed by 'check'.  Stop."), false},
		{"pytest prints no tests ran inside a test", shellResult("pytest", 5, "captured: the helper said no tests ran\n", ""), false},
		{"npm test runs jest and prints missing script", shellResult("npm test", 1, "Tests:       1 failed, 1 total\nmissing script", ""), false},
		{"go test prints a make rule", shellResult("go test ./...", 1, "make: *** No rule to make target 'test'.  Stop.", ""), false},
		{"npm script ran and its tool is missing", shellResult("npm test", 127, "> pkg@1.0.0 test\n> jest\n\nsh: 1: jest: not found", "npm error Lifecycle script `test` failed with error:\nnpm error code 127"), false},
		{"npm script ran, exit 127, old npm format", shellResult("npm test", 127, "> pkg@1.0.0 test\n> jest\n\nsh: 1: jest: not found", "npm ERR! code ELIFECYCLE\nnpm ERR! errno 127"), false},
		{"go test ran and exited 127", shellResult("go test ./...", 127, "=== RUN   TestX\n--- FAIL: TestX (0.00s)\nFAIL\tx\t0.004s", "exec: \"tool\": executable file not found in $PATH"), false},
		{"pytest ran and exited 127", shellResult("pytest", 127, "collected 2 items\n\n================ 1 failed, 1 passed in 0.03s ================", "sh: 1: helper: not found"), false},
		{"exit 127 with no start message", shellResult("go test ./...", 127, "", "something unexpected"), false},
		{"make prints a go message", shellResult("make test", 2, "", "go: go.mod file not found in current directory or any parent directory"), false},
		{"cargo prints a go message", shellResult("cargo test", 101, "", "error: go.mod file not found in current directory"), false},
		{"npm prints a go message", shellResult("npm test", 1, "", "go: go.mod file not found in current directory"), false},
		{"go prints a cargo message", shellResult("go test ./...", 1, "", "could not find `Cargo.toml` in `/x`"), false},
		{"unknown command", shellResult("sh script.sh", 1, "", "go: go.mod file not found in current directory"), false},
		{"pytest fails", shellResult("pytest", 1, "1 failed in 0.01s", ""), false},
		{"pytest fails and a test prints go.mod not found", shellResult("pytest", 1, "collected 2 items\n\nFAILED test_tool.py::test_run - go: go.mod file not found in current directory\n================ 1 failed, 1 passed in 0.03s ================", ""), false},
		{"pytest fails with a summary only", shellResult("pytest -q", 1, "go: go.mod file not found in current directory\n1 failed in 0.02s", ""), false},
		{"pytest exit 5 without the marker", shellResult("pytest", 5, "usage: pytest", ""), false},
		{"npm test fails", shellResult("npm test", 1, "1 failing", ""), false},
		{"make fails", shellResult("make check", 2, "", "compile error"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := VerificationUnavailableReason(tc.result)
			if (got != "") != tc.want {
				t.Fatalf("VerificationUnavailableReason = %q, want unavailable=%v", got, tc.want)
			}
		})
	}

	jest := &Result{Error: "test command exited with code 1", Data: map[string]any{"framework": "jest", "exit_code": 1, "output": "npm ERR! missing script: test"}}
	if VerificationUnavailableReason(jest) == "" {
		t.Error("a jest run with no npm test script must read as unavailable")
	}
}

func TestRunTestsTool_NoFrameworkIsUnavailableNotAFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	result, err := tool.ExecuteWithContext(context.Background(), map[string]any{})
	if err != nil || result == nil || result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if status, _ := result.Data[VerificationStatusKey].(string); status != VerificationStatusUnavailable {
		t.Fatalf("status = %q, want unavailable; result=%+v", status, result)
	}
	if !strings.Contains(result.Error, "not a test failure") {
		t.Errorf("error does not say this is not a test failure: %s", result.Error)
	}
	if VerificationUnavailableReason(result) == "" {
		t.Error("no-framework result must read as unavailable")
	}
}

func TestHasVerificationSurface(t *testing.T) {
	write := func(root, name string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"empty", nil, false},
		{"only a note and a cache", []string{"hello.txt", "brief.txt", ".pytest_cache/README.md", "lane.log"}, false},
		{"go module", []string{"go.mod"}, true},
		{"nested go module", []string{"tools/gen/go.mod"}, true},
		{"package json", []string{"package.json"}, true},
		{"cargo", []string{"Cargo.toml"}, true},
		{"makefile", []string{"Makefile"}, true},
		{"python test", []string{"tests/test_hello.py"}, true},
		{"go test file", []string{"pkg/a/a_test.go"}, true},
		{"js spec", []string{"src/a.spec.ts"}, true},
		{"only node_modules", []string{"node_modules/x/package.json", "vendor/y/go.mod"}, false},
		{"only hidden dirs", []string{".git/config", ".github/workflows/ci.yml"}, false},
		{"below the depth bound, a module", []string{"a/b/c/d/e/go.mod"}, true},
		{"below the depth bound, a test", []string{"a/b/c/d/e/f/test_deep.py"}, true},
		{"below the depth bound, only a note", []string{"a/b/c/d/e/note.txt"}, true},
		{"at the depth bound, a module", []string{"a/b/c/d/go.mod"}, true},
		{"deepest searched level, only a note", []string{"a/b/c/d/note.txt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range tc.files {
				write(root, name)
			}
			got, reason := HasVerificationSurface(root)
			if got != tc.want {
				t.Fatalf("HasVerificationSurface = %v (%q), want %v", got, reason, tc.want)
			}
			if !got && !strings.Contains(reason, "go.mod") {
				t.Errorf("reason does not name what was searched for: %q", reason)
			}
		})
	}
}

func TestHasVerificationSurface_ParentModuleCounts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "note.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, reason := HasVerificationSurface(child); !got {
		t.Fatalf("a parent go.mod applies to go test in a subdirectory: %q", reason)
	}
}

func TestHasVerificationSurface_ParentSearchStopsAtRepositoryRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "go.mod"), []byte("module outer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, reason := HasVerificationSurface(repo); got {
		t.Fatalf("a module above the repository root must not count: %q", reason)
	}
}

func TestHasVerificationSurface_LargeTreeCountsAsAvailable(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < surfaceMaxEntries+50; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%05d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, reason := HasVerificationSurface(root); !got {
		t.Fatalf("a tree that outruns the search budget must count as available: %q", reason)
	}
}

func TestRunTestsTool_EmptyRunIsUnavailableButAMissedPatternIsNot(t *testing.T) {
	t.Cleanup(func() { execCommandContext = exec.CommandContext })
	execCommandContext = func(ctx context.Context, name string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1"`, "test", "test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"empty\"\nversion=\"0.1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	empty, err := (&RunTestsTool{}).Execute(map[string]any{"path": dir})
	if err != nil || empty.Success {
		t.Fatalf("result=%+v err=%v", empty, err)
	}
	if status, _ := empty.Data[VerificationStatusKey].(string); status != VerificationStatusUnavailable || VerificationUnavailableReason(empty) == "" {
		t.Fatalf("a clean run that found no tests must read as unavailable: %+v", empty)
	}

	missed, err := (&RunTestsTool{}).Execute(map[string]any{"path": dir, "pattern": "no_such_test"})
	if err != nil || missed.Success {
		t.Fatalf("result=%+v err=%v", missed, err)
	}
	if reason := VerificationUnavailableReason(missed); reason != "" {
		t.Fatalf("a pattern that matches nothing is the caller's mistake, not an unavailable check (%q)", reason)
	}
}

func writeGoModule(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	files["go.mod"] = "module example.com/probe\n\ngo 1.22\n"
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const (
	goPassingSource = "package a\n\nfunc One() int { return 1 }\n"
	goPassingTest   = "package a\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"
)

// A module whose packages all sit in subdirectories has nothing to test at its
// root. run_tests with no path used to fail there in 0 s with "no Go files".
func TestRunTestsTool_DefaultPathTestsEveryPackageWhenTheRootHasNone(t *testing.T) {
	root := writeGoModule(t, map[string]string{"pkg/a/a.go": goPassingSource, "pkg/a/a_test.go": goPassingTest})
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)

	result, err := tool.ExecuteWithContext(context.Background(), map[string]any{"timeout_seconds": float64(120)})
	if err != nil || !result.Success || result.Data["path"] != "./..." || result.Data["passed"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	// A path the caller names is honored exactly.
	explicit, err := tool.ExecuteWithContext(context.Background(), map[string]any{"path": ".", "timeout_seconds": float64(120)})
	if err != nil || explicit.Success || explicit.Data["path"] != "." {
		t.Fatalf("an explicit path must be honored: %+v err=%v", explicit, err)
	}
}

func TestRunTestsTool_DefaultPathKeepsTheRootPackageWhenThereIsOne(t *testing.T) {
	root := writeGoModule(t, map[string]string{"a.go": goPassingSource, "a_test.go": goPassingTest})
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	result, err := tool.ExecuteWithContext(context.Background(), map[string]any{"timeout_seconds": float64(120)})
	if err != nil || !result.Success || result.Data["path"] != "." || result.Data["passed"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDirHasGoFiles(t *testing.T) {
	root := t.TempDir()
	if dirHasGoFiles(root) {
		t.Fatal("an empty directory has no Go files")
	}
	if err := os.MkdirAll(filepath.Join(root, "sub.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if dirHasGoFiles(root) {
		t.Fatal("a directory named like a Go file is not a Go file")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !dirHasGoFiles(root) {
		t.Fatal("a.go is a Go file")
	}
	if !dirHasGoFiles(filepath.Join(root, "missing")) {
		t.Fatal("an unreadable directory must keep the caller's path")
	}
}

// The marker table is only as good as the messages real tools print. This runs
// each tool in a workspace that has nothing for it to run, and again where a
// real failure prints the same words, and checks both readings.
func TestVerificationUnavailableReason_RealToolOutput(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	for _, tc := range []struct {
		name    string
		files   map[string]string
		command string
		// unavailable is the expected reading.
		unavailable bool
	}{
		{"go test wildcard, no module", nil, "go test ./...", true},
		{"go test package, no module", nil, "go test .", true},
		{"go vet wildcard, no module", nil, "go vet ./...", true},
		{"go build, no module", nil, "go build", true},
		{"go test fails and prints the go.mod message", map[string]string{
			"go.mod":    "module example.com/x\n\ngo 1.22\n",
			"x_test.go": "package x\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) {\n\tfmt.Println(\"go: go.mod file not found in current directory or any parent directory\")\n\tt.Fatal(\"no\")\n}\n",
		}, "go test ./...", false},
		{"cargo test, no manifest", nil, "cargo test", true},
		{"npm test, no package.json", nil, "npm test", true},
		{"npm run build, no package.json", nil, "npm run build", true},
		{"npm test, no test script", map[string]string{"package.json": `{"name":"x","version":"1.0.0","scripts":{}}`}, "npm test", true},
		{"npm test, default init script", map[string]string{"package.json": `{"name":"x","version":"1.0.0","scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, "npm test", true},
		{"npm test, script fails and prints missing script", map[string]string{"package.json": `{"name":"x","version":"1.0.0","scripts":{"test":"echo missing script && exit 1"}}`}, "npm test", false},
		{"make test, no makefile", nil, "make test", true},
		{"make test, no such target", map[string]string{"Makefile": "all:\n\t@true\n"}, "make test", true},
		{"make test, recipe fails and prints missing script", map[string]string{"Makefile": "test:\n\t@echo missing script; exit 1\n"}, "make test", false},
		{"pytest, no tests", nil, "pytest", true},
		{"python3 -m pytest, no tests", nil, "python3 -m pytest", true},
		{"pytest, test fails and prints the go.mod message", map[string]string{"test_x.py": "def test_x():\n    print('go: go.mod file not found in current directory or any parent directory')\n    assert False\n"}, "pytest", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := strings.Fields(tc.command)
			versionArgs := []string{"--version"}
			if words[0] == "go" {
				versionArgs = []string{"version"}
			}
			if err := exec.Command(words[0], versionArgs...).Run(); err != nil {
				t.Skipf("%s is not installed or not usable: %v", words[0], err)
			}
			if !tc.unavailable && (words[0] == "python3" || words[0] == "pytest") {
				if err := exec.Command("python3", "-m", "pytest", "--version").Run(); err != nil {
					t.Skip("pytest is not installed")
				}
			}
			dir := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			shell := &ShellCommandTool{}
			shell.SetWorkDir(dir)
			result, err := NewWorkspaceVerificationTool(shell).ExecuteWithContext(context.Background(), map[string]any{"command": tc.command})
			if err != nil || result == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Success {
				t.Skipf("the tool passed here; nothing to classify: %v", result.Data)
			}
			reason := VerificationUnavailableReason(result)
			if (reason != "") != tc.unavailable {
				t.Fatalf("reason = %q, want unavailable=%v\nexit=%v stdout=%q stderr=%q", reason, tc.unavailable, result.Data["exit_code"], result.Data["stdout"], result.Data["stderr"])
			}
		})
	}
}

func TestHasVerificationSurface_UnsearchedSubtreesCountAsUnknown(t *testing.T) {
	t.Run("symlinked directory", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got, reason := HasVerificationSurface(root); !got {
			t.Fatalf("a symlinked directory was not searched, so the workspace has an unknown surface: %q", reason)
		}
	})
	t.Run("symlinked file", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "note-link.txt")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got, reason := HasVerificationSurface(root); got {
			t.Fatalf("a symlink to a plain file hides no surface: %q", reason)
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		root := t.TempDir()
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "go.mod"), []byte("module x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if got, reason := HasVerificationSurface(root); !got {
			t.Fatalf("an unreadable directory may hold a surface: %q", reason)
		}
	})
}
