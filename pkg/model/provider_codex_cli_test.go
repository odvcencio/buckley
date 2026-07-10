package model

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestCodexCLIProviderChatCompletionUsesExecLastMessage(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{
			Command: "codex",
			Models:  []string{"codex/gpt-5.4-mini"},
		},
		config.SandboxConfig{Mode: "workspace"},
		config.ApprovalConfig{Mode: "safe"},
	)

	var got CodexCLICommand
	provider.runner = func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		outputPath := argAfter(cmd.Args, "--output-last-message")
		if outputPath == "" {
			t.Fatalf("missing --output-last-message in args: %v", cmd.Args)
		}
		if err := os.WriteFile(outputPath, []byte("codex answer\n"), 0o644); err != nil {
			t.Fatalf("write codex output: %v", err)
		}
		return CodexCLICommandResult{Stdout: []byte(strings.Join([]string{
			`{"type":"thread.started","thread_id":"thread-1"}`,
			`{"type":"item.completed","item":{"id":"item-1","type":"command_execution","command":"/bin/bash -lc 'go build ./pkg/model'","aggregated_output":"","exit_code":0,"status":"completed"}}`,
			`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"codex answer"}}`,
		}, "\n"))}, nil
	}

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "codex/gpt-5.4-mini",
		Reasoning: &ReasoningConfig{Effort: "xhigh"},
		Messages: []Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if got.Name != "codex" {
		t.Fatalf("command name=%q want codex", got.Name)
	}
	if !containsArgs(got.Args, "exec", "--json", "--color", "never") {
		t.Fatalf("unexpected codex args: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--model", "gpt-5.4-mini"}) {
		t.Fatalf("codex args missing model: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--sandbox", "workspace-write"}) {
		t.Fatalf("codex args missing workspace sandbox: %v", got.Args)
	}
	if containsArgs(got.Args, "--ask-for-approval") {
		t.Fatalf("codex args should not use removed approval flag: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"-c", `approval_policy="never"`}) {
		t.Fatalf("codex args missing approval policy: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"-c", `model_reasoning_effort="xhigh"`}) {
		t.Fatalf("codex args missing reasoning effort: %v", got.Args)
	}
	if got.Args[len(got.Args)-1] != "-" {
		t.Fatalf("codex prompt should be read from stdin, args: %v", got.Args)
	}
	if !strings.Contains(got.Stdin, "System:\nsystem prompt") || !strings.Contains(got.Stdin, "User:\nhello") {
		t.Fatalf("stdin missing transcript: %q", got.Stdin)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "codex answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.ExecutionEvidence) != 1 || resp.ExecutionEvidence[0].Command != "/bin/bash -lc 'go build ./pkg/model'" {
		t.Fatalf("unexpected command execution evidence: %+v", resp.ExecutionEvidence)
	}
	if resp.ExecutionEvidence[0].ExitCode == nil || *resp.ExecutionEvidence[0].ExitCode != 0 || resp.ExecutionEvidence[0].Status != "completed" {
		t.Fatalf("incomplete command execution evidence: %+v", resp.ExecutionEvidence[0])
	}
}

func TestParseCodexCommandExecutionEvidenceRequiresCompletedCommandEvents(t *testing.T) {
	stdout := strings.Join([]string{
		`not-json`,
		`{"type":"item.started","item":{"type":"command_execution","command":"go test ./...","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go build ./pkg/model","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./pkg/model","exit_code":1,"status":"failed"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./pkg/rlm","status":"completed"}}`,
	}, "\n")

	got := parseCodexCommandExecutionEvidence([]byte(stdout))
	if len(got) != 3 {
		t.Fatalf("evidence count=%d want 3: %+v", len(got), got)
	}
	if got[0].Command != "go build ./pkg/model" || got[0].ExitCode == nil || *got[0].ExitCode != 0 || got[0].Status != "completed" {
		t.Fatalf("successful evidence = %+v", got[0])
	}
	if got[1].ExitCode == nil || *got[1].ExitCode != 1 || got[1].Status != "failed" {
		t.Fatalf("failed evidence = %+v", got[1])
	}
	if got[2].ExitCode != nil {
		t.Fatalf("missing exit status became trusted zero: %+v", got[2])
	}
}

func TestCodexCLIProviderDefaultModelOmitsModelArg(t *testing.T) {
	provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{Mode: "readonly"}, config.ApprovalConfig{Mode: "ask"})

	var got CodexCLICommand
	provider.runner = func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		if err := os.WriteFile(argAfter(cmd.Args, "--output-last-message"), []byte("answer"), 0o644); err != nil {
			t.Fatalf("write output: %v", err)
		}
		return CodexCLICommandResult{}, nil
	}

	if _, err := provider.ChatCompletion(context.Background(), ChatRequest{Model: "codex/default", Messages: []Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if argAfter(got.Args, "--model") != "" {
		t.Fatalf("default codex model should not pass --model: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--sandbox", "read-only"}) {
		t.Fatalf("codex args missing read-only sandbox: %v", got.Args)
	}
	if containsArgs(got.Args, "--ask-for-approval") {
		t.Fatalf("codex args should not use removed approval flag: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"-c", `approval_policy="untrusted"`}) {
		t.Fatalf("codex args missing untrusted approval: %v", got.Args)
	}
}

func TestCodexCLIProviderReadOnlyRequestOverridesWritableConfig(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Command: "codex"},
		config.SandboxConfig{Mode: "disabled", AllowUnsafe: true},
		config.ApprovalConfig{Mode: "yolo"},
	)

	var got CodexCLICommand
	isolatedDir := t.TempDir()
	runCodexProviderGit(t, isolatedDir, "init")
	sourceRoot := t.TempDir()
	snapshot, err := NewReviewSnapshot(
		ReviewSnapshotHead,
		sourceRoot,
		sourceRoot,
		"1111111111111111111111111111111111111111",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	provider.reviewWorkspace = func(_ context.Context, got *ReviewSnapshot) (string, func(), error) {
		if got != snapshot {
			t.Fatalf("snapshot pointer changed before provider: got %p want %p", got, snapshot)
		}
		return isolatedDir, func() { cleaned = true }, nil
	}
	provider.reviewVerifier = func(_ context.Context, gotDir string, got *ReviewSnapshot) error {
		if gotDir != isolatedDir || got != snapshot {
			t.Fatalf("verification identity changed: dir=%q snapshot=%p", gotDir, got)
		}
		return nil
	}
	provider.runner = func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		if err := os.WriteFile(argAfter(cmd.Args, "--output-last-message"), []byte("review"), 0o644); err != nil {
			t.Fatalf("write output: %v", err)
		}
		return CodexCLICommandResult{}, nil
	}

	_, err = provider.ChatCompletion(context.Background(), ChatRequest{
		Model:          "codex/gpt-5.6-terra",
		Messages:       []Message{{Role: "user", Content: "## Repository Information\n\n- **Root**: " + sourceRoot + "\n\nreview this change"}},
		Metadata:       map[string]string{RequestMetadataReadOnly: "true"},
		ReviewSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if containsArgs(got.Args, "--sandbox") {
		t.Fatalf("isolated review mixed legacy sandbox flags with its permission profile: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"-c", `default_permissions="buckley-review-snapshot"`}) {
		t.Fatalf("isolated review did not select the snapshot permission profile: %v", got.Args)
	}
	if !containsArgs(got.Args, "--strict-config", "--ignore-user-config", "--ignore-rules") {
		t.Fatalf("isolated review retained ambient Codex configuration: %v", got.Args)
	}
	joinedArgs := strings.Join(got.Args, "\n")
	for _, want := range []string{
		`":workspace_roots" = { "." = "read" }`,
		`":tmpdir" = "write"`,
		`shell_environment_policy={ inherit = "none"`,
		`network={ enabled = false }`,
	} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("isolated review permission policy missing %q:\n%s", want, joinedArgs)
		}
	}
	if tempDir := envValue(got.Env, "TMPDIR"); tempDir == "" || tempDir == os.TempDir() {
		t.Fatalf("isolated review did not receive a private TMPDIR: %q in %v", tempDir, got.Env)
	}
	if got.Dir != isolatedDir || argAfter(got.Args, "--cd") != isolatedDir {
		t.Fatalf("review ran outside disposable workspace: dir=%q args=%v", got.Dir, got.Args)
	}
	if strings.Contains(got.Stdin, "- **Root**: "+sourceRoot) {
		t.Fatalf("review prompt exposed the live checkout root:\n%s", got.Stdin)
	}
	for _, want := range []string{"Authoritative review repository root: " + isolatedDir, "- **Root**: " + isolatedDir + " (isolated immutable snapshot)"} {
		if !strings.Contains(got.Stdin, want) {
			t.Fatalf("review prompt missing %q:\n%s", want, got.Stdin)
		}
	}
	if !cleaned {
		t.Fatal("disposable review workspace was not cleaned")
	}

	writableArgs := provider.buildExecArgs("codex/gpt-5.6-terra", "/tmp/out", "/workspace", nil, "")
	if !containsSubsequence(writableArgs, []string{"--sandbox", "danger-full-access"}) {
		t.Fatalf("ordinary non-review request lost configured write capability: %v", writableArgs)
	}
}

func TestCodexCLIProviderNestedSnapshotExecutesEvidenceFromRepositoryRoot(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Command: "codex"},
		config.SandboxConfig{Mode: "readonly"},
		config.ApprovalConfig{Mode: "safe"},
	)

	isolatedRoot := t.TempDir()
	runCodexProviderGit(t, isolatedRoot, "init")
	isolatedNested := filepath.Join(isolatedRoot, "pkg", "nested")
	if err := os.MkdirAll(isolatedNested, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	sourceNested := filepath.Join(sourceRoot, "pkg", "nested")
	if err := os.MkdirAll(sourceNested, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewReviewSnapshot(
		ReviewSnapshotHead,
		sourceRoot,
		sourceNested,
		"1111111111111111111111111111111111111111",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	provider.reviewWorkspace = func(context.Context, *ReviewSnapshot) (string, func(), error) {
		return isolatedNested, func() {}, nil
	}
	provider.reviewVerifier = func(_ context.Context, workDir string, _ *ReviewSnapshot) error {
		if workDir != isolatedRoot {
			t.Fatalf("post-run verification dir = %q, want repository root %q", workDir, isolatedRoot)
		}
		return nil
	}
	var got CodexCLICommand
	provider.runner = func(_ context.Context, command CodexCLICommand) (CodexCLICommandResult, error) {
		got = command
		if err := os.WriteFile(argAfter(command.Args, "--output-last-message"), []byte("review"), 0o644); err != nil {
			t.Fatal(err)
		}
		return CodexCLICommandResult{Stdout: []byte(
			`{"type":"item.completed","item":{"id":"build","type":"command_execution","command":"go build ./...","exit_code":0,"status":"completed"}}`,
		)}, nil
	}

	response, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:          "codex/gpt-5.6-terra",
		Messages:       []Message{{Role: "user", Content: "- **Root**: " + sourceRoot}},
		ReviewSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got.Dir != isolatedRoot || argAfter(got.Args, "--cd") != isolatedRoot {
		t.Fatalf("native review command was not rooted: dir=%q args=%v", got.Dir, got.Args)
	}
	if !strings.Contains(got.Stdin, "Authoritative review working directory: "+isolatedNested) {
		t.Fatalf("nested caller context missing from prompt:\n%s", got.Stdin)
	}
	if len(response.ExecutionEvidence) != 1 ||
		response.ExecutionEvidence[0].WorkingDirectory != isolatedRoot ||
		response.ExecutionEvidence[0].RepositoryRoot != isolatedRoot {
		t.Fatalf("nested review evidence was not root-bound: %+v", response.ExecutionEvidence)
	}
}

func TestCodexCLIProviderReadOnlyRequestWithoutSnapshotUsesNativeReadOnlySandbox(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Command: "codex"},
		config.SandboxConfig{Mode: "workspace"},
		config.ApprovalConfig{Mode: "safe"},
	)
	provider.reviewWorkspace = func(context.Context, *ReviewSnapshot) (string, func(), error) {
		t.Fatal("snapshot materializer called without a descriptor")
		return "", nil, nil
	}

	var got CodexCLICommand
	provider.runner = func(_ context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		if err := os.WriteFile(argAfter(cmd.Args, "--output-last-message"), []byte("review"), 0o644); err != nil {
			return CodexCLICommandResult{}, err
		}
		return CodexCLICommandResult{}, nil
	}

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "codex/gpt-5.6-terra",
		Messages: []Message{{Role: "user", Content: "review this change"}},
		Metadata: map[string]string{RequestMetadataReadOnly: "true"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if !containsSubsequence(got.Args, []string{"--sandbox", "read-only"}) {
		t.Fatalf("read-only request did not use native read-only sandbox: %v", got.Args)
	}
}

func TestCodexCLIProviderSnapshotReproductionFailureFailsClosed(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Command: "codex"},
		config.SandboxConfig{Mode: "workspace"},
		config.ApprovalConfig{Mode: "safe"},
	)
	root := t.TempDir()
	snapshot, err := NewReviewSnapshot(
		ReviewSnapshotIndex,
		root,
		root,
		"1111111111111111111111111111111111111111",
		[]byte("captured patch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider.reviewWorkspace = func(context.Context, *ReviewSnapshot) (string, func(), error) {
		return "", nil, os.ErrPermission
	}
	called := false
	provider.runner = func(_ context.Context, _ CodexCLICommand) (CodexCLICommandResult, error) {
		called = true
		return CodexCLICommandResult{}, nil
	}

	_, err = provider.ChatCompletion(context.Background(), ChatRequest{
		Model:          "codex/gpt-5.6-terra",
		Messages:       []Message{{Role: "user", Content: "review this change"}},
		ReviewSnapshot: snapshot,
	})
	if err == nil || !strings.Contains(err.Error(), "reproduce codex review snapshot") {
		t.Fatalf("ChatCompletion error = %v, want fail-closed reproduction error", err)
	}
	if called {
		t.Fatal("Codex ran against live checkout after snapshot reproduction failed")
	}
}

func TestPrepareCodexReviewWorkspaceSnapshotsTrackedChanges(t *testing.T) {
	repo := t.TempDir()
	runCodexProviderGit(t, repo, "init")
	runCodexProviderGit(t, repo, "config", "user.email", "test@example.com")
	runCodexProviderGit(t, repo, "config", "user.name", "Test User")

	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "tracked.txt")
	runCodexProviderGit(t, repo, "commit", "-m", "initial")
	unreferenced := filepath.Join(repo, "source-only.txt")
	if err := os.WriteFile(unreferenced, []byte("must stay in source object store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreferencedObject := runCodexProviderGit(t, repo, "hash-object", "-w", "source-only.txt")
	if err := os.Remove(unreferenced); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tracked, []byte("working tree change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(repo, "staged.txt")
	if err := os.WriteFile(staged, []byte("staged addition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "staged.txt")

	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotTrackedWorktree})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	isolated, cleanup, err := prepareCodexReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("prepareCodexReviewWorkspace: %v", err)
	}
	tempRoot := filepath.Dir(isolated)
	if cleanup == nil {
		t.Fatal("prepareCodexReviewWorkspace returned nil cleanup")
	}
	t.Cleanup(cleanup)

	assertCodexProviderFileContent(t, filepath.Join(isolated, "tracked.txt"), "working tree change\n")
	assertCodexProviderFileContent(t, filepath.Join(isolated, "staged.txt"), "staged addition\n")
	if err := verifyCodexReviewWorkspace(context.Background(), isolated, snapshot); err != nil {
		t.Fatalf("materialized snapshot verification: %v", err)
	}
	if _, err := os.Stat(filepath.Join(isolated, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("materialized snapshot retained an object-store alternate: %v", err)
	}
	catFile := exec.Command("git", "-C", isolated, "cat-file", "-e", unreferencedObject)
	if err := catFile.Run(); err == nil {
		t.Fatal("materialized snapshot exposed an unrelated source-only Git object")
	}
	if err := os.WriteFile(filepath.Join(isolated, "tracked.txt"), []byte("review mutation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyCodexReviewWorkspace(context.Background(), isolated, snapshot); err == nil {
		t.Fatal("tracked review mutation was not detected")
	}
	assertCodexProviderFileContent(t, tracked, "working tree change\n")

	cleanup()
	if _, err := os.Stat(tempRoot); !os.IsNotExist(err) {
		t.Fatalf("temporary review workspace still exists: err=%v", err)
	}
}

func TestPrepareReviewWorkspaceIgnoresAmbientGitHooksAndFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-hook fixture is Unix-specific")
	}
	repo := t.TempDir()
	runCodexProviderGit(t, repo, "init")
	runCodexProviderGit(t, repo, "config", "user.email", "test@example.com")
	runCodexProviderGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("captured\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=poison\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "tracked.txt", ".gitattributes")
	runCodexProviderGit(t, repo, "commit", "-m", "initial")

	marker := filepath.Join(t.TempDir(), "ambient-git-executed")
	uploadHook := filepath.Join(t.TempDir(), "upload-pack-hook")
	if err := os.WriteFile(uploadHook, []byte("#!/bin/sh\nprintf upload-pack > '"+marker+"'\nexec git pack-objects \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "config", "uploadpack.packObjectsHook", uploadHook)

	evilHome := t.TempDir()
	templates := filepath.Join(evilHome, "templates")
	if err := os.MkdirAll(filepath.Join(templates, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	postCheckout := filepath.Join(templates, "hooks", "post-checkout")
	if err := os.WriteFile(postCheckout, []byte("#!/bin/sh\nprintf post-checkout > '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	smudge := filepath.Join(evilHome, "smudge")
	if err := os.WriteFile(smudge, []byte("#!/bin/sh\nprintf smudge > '"+marker+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := "[init]\n\ttemplateDir = " + templates + "\n[filter \"poison\"]\n\tsmudge = " + smudge + "\n\trequired = true\n"
	if err := os.WriteFile(filepath.Join(evilHome, ".gitconfig"), []byte(globalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", evilHome)
	evilExecPath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(evilExecPath, "git-upload-pack"),
		[]byte("#!/bin/sh\nprintf git-exec-path > '"+marker+"'\nexit 99\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	// Set executable-routing and trace variables only after capture so this
	// fixture targets the materializer's environment boundary.
	t.Setenv("GIT_EXEC_PATH", evilExecPath)
	t.Setenv("GIT_TRACE", marker)
	workDir, cleanup, err := PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	assertCodexProviderFileContent(t, filepath.Join(workDir, "tracked.txt"), "captured\n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("ambient Git hook executed during snapshot materialization: %v", err)
	}
}

func runCodexProviderGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertCodexProviderFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestCodexCLIProviderCatalogIncludesConfiguredModels(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Models: []string{"gpt-5.4-mini"}},
		config.SandboxConfig{},
		config.ApprovalConfig{},
	)

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}

	if len(catalog.Data) != 2 {
		t.Fatalf("catalog size=%d want 2: %+v", len(catalog.Data), catalog.Data)
	}
	if catalog.Data[0].ID != "codex/default" || catalog.Data[1].ID != "codex/gpt-5.4-mini" {
		t.Fatalf("unexpected catalog: %+v", catalog.Data)
	}
	if provider.SupportsToolsForTest("codex/gpt-5.4-mini") {
		t.Fatal("codex provider catalog should not advertise OpenAI-style tool calling")
	}
	if !provider.SupportsReasoningForTest("codex/gpt-5.4-mini") {
		t.Fatal("codex provider catalog should advertise reasoning support")
	}
}

func (p *CodexCLIProvider) SupportsToolsForTest(modelID string) bool {
	info, err := p.GetModelInfo(modelID)
	if err != nil {
		return false
	}
	for _, param := range info.SupportedParameters {
		if param == "tools" || param == "functions" {
			return true
		}
	}
	return false
}

func (p *CodexCLIProvider) SupportsReasoningForTest(modelID string) bool {
	info, err := p.GetModelInfo(modelID)
	if err != nil {
		return false
	}
	for _, param := range info.SupportedParameters {
		if param == "reasoning" {
			return true
		}
	}
	return false
}

func argAfter(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func containsArgs(args []string, values ...string) bool {
	for _, value := range values {
		found := false
		for _, arg := range args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsSubsequence(values, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i <= len(values)-len(want); i++ {
		match := true
		for j := range want {
			if values[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
