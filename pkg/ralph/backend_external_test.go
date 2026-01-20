// pkg/ralph/backend_external_test.go
package ralph

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestExternalBackend_Name(t *testing.T) {
	backend := NewExternalBackend("claude", "claude", []string{"-p", "{prompt}"}, nil)

	if got := backend.Name(); got != "claude" {
		t.Errorf("Name() = %q, want %q", got, "claude")
	}
}

func TestExternalBackend_Name_CustomName(t *testing.T) {
	backend := NewExternalBackend("my-claude", "claude", []string{"-p", "{prompt}"}, nil)

	if got := backend.Name(); got != "my-claude" {
		t.Errorf("Name() = %q, want %q", got, "my-claude")
	}
}

func TestExternalBackend_Available_Default(t *testing.T) {
	backend := NewExternalBackend("test", "echo", nil, nil)

	// Should be available by default
	if !backend.Available() {
		t.Error("Available() = false, want true by default")
	}
}

func TestExternalBackend_SetAvailable(t *testing.T) {
	backend := NewExternalBackend("test", "echo", nil, nil)

	// Set unavailable
	backend.SetAvailable(false)
	if backend.Available() {
		t.Error("Available() = true after SetAvailable(false)")
	}

	// Set available again
	backend.SetAvailable(true)
	if !backend.Available() {
		t.Error("Available() = false after SetAvailable(true)")
	}
}

func TestExternalBackend_Execute_Success(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"hello", "world"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "Test prompt",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Backend != "echo-test" {
		t.Errorf("result.Backend = %q, want %q", result.Backend, "echo-test")
	}

	if result.Duration <= 0 {
		t.Error("result.Duration should be positive")
	}

	if result.Error != nil {
		t.Errorf("result.Error = %v, want nil", result.Error)
	}

	// echo outputs "hello world\n"
	expected := "hello world\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_TemplateExpansion_Prompt(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"{prompt}"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "my test prompt",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "my test prompt\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_TemplateExpansion_Iteration(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"iteration:{iteration}"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   42,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "iteration:42\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_TemplateExpansion_SessionID(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"session:{session_id}"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "abc-123-xyz",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "session:abc-123-xyz\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_TemplateExpansion_Sandbox(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"{sandbox}"}, nil)

	sandboxPath := t.TempDir()
	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: sandboxPath,
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := sandboxPath + "\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_TemplateExpansion_Multiple(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"{prompt}", "{iteration}", "{session_id}"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "hello",
		SandboxPath: t.TempDir(),
		Iteration:   5,
		SessionID:   "sess-001",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "hello 5 sess-001\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_Options(t *testing.T) {
	options := map[string]string{
		"model": "opus",
		"temp":  "0.5",
	}
	backend := NewExternalBackend("echo-test", "echo", []string{"base"}, options)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Options are appended as --key value flags
	// Order is deterministic (sorted by key)
	expected := "base --model opus --temp 0.5\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_OptionsEmpty(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"base"}, map[string]string{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "base\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_Error(t *testing.T) {
	backend := NewExternalBackend("false-test", "false", nil, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)

	// Execute should not return error directly; error is captured in result
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (error should be in result)", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Error == nil {
		t.Error("result.Error = nil, want error for non-zero exit code")
	}
}

func TestExternalBackend_Execute_CommandNotFound(t *testing.T) {
	backend := NewExternalBackend("nonexistent", "this-command-does-not-exist-12345", nil, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)

	// Execute should not return error directly; error is captured in result
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (error should be in result)", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Error == nil {
		t.Error("result.Error = nil, want error for command not found")
	}
}

func TestExternalBackend_Execute_ContextCanceled(t *testing.T) {
	// Use sleep to have a command that takes time
	var cmd string
	var args []string
	if runtime.GOOS == "windows" {
		cmd = "ping"
		args = []string{"-n", "10", "127.0.0.1"}
	} else {
		cmd = "sleep"
		args = []string{"10"}
	}

	backend := NewExternalBackend("sleep-test", cmd, args, nil)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	start := time.Now()
	result, err := backend.Execute(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	// Should complete quickly due to cancellation, not wait 10 seconds
	if elapsed > 2*time.Second {
		t.Errorf("Execute took %v, expected < 2s due to cancellation", elapsed)
	}

	// Should capture context cancellation error
	if result.Error == nil {
		t.Error("result.Error = nil, want error due to cancellation")
	}
}

func TestExternalBackend_Execute_TracksDuration(t *testing.T) {
	// Use a command that takes a measurable amount of time
	var cmd string
	var args []string
	if runtime.GOOS == "windows" {
		cmd = "ping"
		args = []string{"-n", "1", "127.0.0.1"}
	} else {
		cmd = "sleep"
		args = []string{"0.05"}
	}

	backend := NewExternalBackend("sleep-test", cmd, args, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	minExpected := 40 * time.Millisecond
	if result.Duration < minExpected {
		t.Errorf("result.Duration = %v, want >= %v", result.Duration, minExpected)
	}
}

func TestExternalBackend_Execute_WorkingDirectory(t *testing.T) {
	// pwd outputs the current working directory
	backend := NewExternalBackend("pwd-test", "pwd", nil, nil)

	sandboxPath := t.TempDir()
	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: sandboxPath,
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := sandboxPath + "\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_CapturesStderr(t *testing.T) {
	// Use sh -c to write to stderr
	backend := NewExternalBackend("stderr-test", "sh", []string{"-c", "echo error >&2"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "error\n"
	if result.Output != expected {
		t.Errorf("result.Output = %q, want %q", result.Output, expected)
	}
}

func TestExternalBackend_Execute_CombinesStdoutStderr(t *testing.T) {
	// Use sh -c to write to both stdout and stderr
	backend := NewExternalBackend("combined-test", "sh", []string{"-c", "echo stdout; echo stderr >&2"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Both stdout and stderr should be captured
	if result.Output != "stdout\nstderr\n" && result.Output != "stderr\nstdout\n" {
		t.Errorf("result.Output = %q, want combined stdout and stderr", result.Output)
	}
}

func TestExternalBackend_Execute_FilesChangedEmpty(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"hello"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// FilesChanged should be empty when no git repo or no changes
	if len(result.FilesChanged) != 0 {
		t.Errorf("result.FilesChanged = %v, want empty slice", result.FilesChanged)
	}
}

func TestExternalBackend_Execute_FilesChangedDetectsNewFiles(t *testing.T) {
	// Create a git repo in temp dir
	sandbox := t.TempDir()

	// Initialize git repo
	initCmd := NewExternalBackend("git-init", "git", []string{"init"}, nil)
	ctx := context.Background()
	_, err := initCmd.Execute(ctx, BackendRequest{SandboxPath: sandbox})
	if err != nil {
		t.Fatalf("git init error = %v", err)
	}

	// Create a command that will create a new file
	backend := NewExternalBackend("touch-test", "sh", []string{"-c", "echo 'new content' > newfile.txt"}, nil)

	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: sandbox,
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// FilesChanged should contain the newly created file
	if len(result.FilesChanged) != 1 {
		t.Errorf("result.FilesChanged len = %d, want 1, got %v", len(result.FilesChanged), result.FilesChanged)
	}

	if len(result.FilesChanged) > 0 && result.FilesChanged[0] != "newfile.txt" {
		t.Errorf("result.FilesChanged[0] = %q, want %q", result.FilesChanged[0], "newfile.txt")
	}
}

func TestExternalBackend_Execute_FilesChangedIgnoresPreexisting(t *testing.T) {
	// Create a git repo in temp dir
	sandbox := t.TempDir()

	// Initialize git repo
	initCmd := NewExternalBackend("git-init", "git", []string{"init"}, nil)
	ctx := context.Background()
	_, err := initCmd.Execute(ctx, BackendRequest{SandboxPath: sandbox})
	if err != nil {
		t.Fatalf("git init error = %v", err)
	}

	// Pre-create a file that will already be "dirty" before execution
	touchCmd := NewExternalBackend("touch", "sh", []string{"-c", "echo 'existing' > existing.txt"}, nil)
	_, err = touchCmd.Execute(ctx, BackendRequest{SandboxPath: sandbox})
	if err != nil {
		t.Fatalf("touch existing error = %v", err)
	}

	// Now execute a command that creates a NEW file
	backend := NewExternalBackend("touch-test", "sh", []string{"-c", "echo 'brand new' > brandnew.txt"}, nil)

	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: sandbox,
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// FilesChanged should only contain the newly created file, not the pre-existing one
	if len(result.FilesChanged) != 1 {
		t.Errorf("result.FilesChanged len = %d, want 1, got %v", len(result.FilesChanged), result.FilesChanged)
	}

	if len(result.FilesChanged) > 0 && result.FilesChanged[0] != "brandnew.txt" {
		t.Errorf("result.FilesChanged[0] = %q, want %q", result.FilesChanged[0], "brandnew.txt")
	}
}

func TestExternalBackend_Execute_DefaultTokensAndCost(t *testing.T) {
	backend := NewExternalBackend("echo-test", "echo", []string{"hello"}, nil)

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "test",
		SandboxPath: t.TempDir(),
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// For external backends, tokens and cost are 0 (not available)
	if result.TokensIn != 0 {
		t.Errorf("result.TokensIn = %d, want 0", result.TokensIn)
	}
	if result.TokensOut != 0 {
		t.Errorf("result.TokensOut = %d, want 0", result.TokensOut)
	}
	if result.Cost != 0 {
		t.Errorf("result.Cost = %f, want 0", result.Cost)
	}
}

func TestExternalBackend_ConcurrentAccess(t *testing.T) {
	backend := NewExternalBackend("test", "echo", nil, nil)

	// Test concurrent SetAvailable and Available calls
	done := make(chan bool)

	go func() {
		for i := 0; i < 100; i++ {
			backend.SetAvailable(i%2 == 0)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			_ = backend.Available()
		}
		done <- true
	}()

	<-done
	<-done

	// No race condition should occur
}

func TestExternalBackend_Command(t *testing.T) {
	backend := NewExternalBackend("test", "claude", []string{"-p"}, nil)

	if got := backend.Command(); got != "claude" {
		t.Errorf("Command() = %q, want %q", got, "claude")
	}
}

func TestExternalBackend_Args(t *testing.T) {
	args := []string{"-p", "{prompt}", "--workdir", "{sandbox}"}
	backend := NewExternalBackend("test", "claude", args, nil)

	gotArgs := backend.Args()

	if len(gotArgs) != len(args) {
		t.Fatalf("Args() len = %d, want %d", len(gotArgs), len(args))
	}

	for i, arg := range args {
		if gotArgs[i] != arg {
			t.Errorf("Args()[%d] = %q, want %q", i, gotArgs[i], arg)
		}
	}
}

func TestExternalBackend_Options(t *testing.T) {
	options := map[string]string{
		"model": "opus",
		"temp":  "0.5",
	}
	backend := NewExternalBackend("test", "claude", nil, options)

	gotOpts := backend.Options()

	if len(gotOpts) != len(options) {
		t.Fatalf("Options() len = %d, want %d", len(gotOpts), len(options))
	}

	for k, v := range options {
		if gotOpts[k] != v {
			t.Errorf("Options()[%q] = %q, want %q", k, gotOpts[k], v)
		}
	}
}

func TestNewExternalBackend(t *testing.T) {
	tests := []struct {
		name    string
		beName  string
		command string
		args    []string
		options map[string]string
	}{
		{
			name:    "with all fields",
			beName:  "full-backend",
			command: "claude",
			args:    []string{"-p", "{prompt}"},
			options: map[string]string{"model": "opus"},
		},
		{
			name:    "with nil args",
			beName:  "nil-args",
			command: "echo",
			args:    nil,
			options: nil,
		},
		{
			name:    "with empty args",
			beName:  "empty-args",
			command: "echo",
			args:    []string{},
			options: map[string]string{},
		},
		{
			name:    "with empty name",
			beName:  "",
			command: "echo",
			args:    nil,
			options: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewExternalBackend(tt.beName, tt.command, tt.args, tt.options)

			if backend == nil {
				t.Fatal("NewExternalBackend() returned nil")
			}

			if got := backend.Name(); got != tt.beName {
				t.Errorf("Name() = %q, want %q", got, tt.beName)
			}

			if got := backend.Command(); got != tt.command {
				t.Errorf("Command() = %q, want %q", got, tt.command)
			}
		})
	}
}

func TestExternalBackend_ImplementsBackend(t *testing.T) {
	// Compile-time check that ExternalBackend implements Backend
	var _ Backend = (*ExternalBackend)(nil)
}

func TestExpandTemplateVars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		req      BackendRequest
		expected string
	}{
		{
			name:  "no variables",
			input: "hello world",
			req: BackendRequest{
				Prompt:      "test",
				SandboxPath: "/tmp",
				Iteration:   1,
				SessionID:   "sess",
			},
			expected: "hello world",
		},
		{
			name:  "prompt variable",
			input: "run {prompt}",
			req: BackendRequest{
				Prompt:      "my task",
				SandboxPath: "/tmp",
				Iteration:   1,
				SessionID:   "sess",
			},
			expected: "run my task",
		},
		{
			name:  "sandbox variable",
			input: "--workdir {sandbox}",
			req: BackendRequest{
				Prompt:      "test",
				SandboxPath: "/home/user/project",
				Iteration:   1,
				SessionID:   "sess",
			},
			expected: "--workdir /home/user/project",
		},
		{
			name:  "iteration variable",
			input: "iter-{iteration}",
			req: BackendRequest{
				Prompt:      "test",
				SandboxPath: "/tmp",
				Iteration:   42,
				SessionID:   "sess",
			},
			expected: "iter-42",
		},
		{
			name:  "session_id variable",
			input: "session={session_id}",
			req: BackendRequest{
				Prompt:      "test",
				SandboxPath: "/tmp",
				Iteration:   1,
				SessionID:   "abc-123",
			},
			expected: "session=abc-123",
		},
		{
			name:  "model variable",
			input: "--model={model}",
			req: BackendRequest{
				Model: "sonnet",
			},
			expected: "--model=sonnet",
		},
		{
			name:  "multiple variables",
			input: "{prompt} in {sandbox} iter {iteration} session {session_id}",
			req: BackendRequest{
				Prompt:      "task",
				SandboxPath: "/work",
				Iteration:   5,
				SessionID:   "sess-001",
			},
			expected: "task in /work iter 5 session sess-001",
		},
		{
			name:  "repeated variable",
			input: "{prompt}-{prompt}-{prompt}",
			req: BackendRequest{
				Prompt:      "x",
				SandboxPath: "/tmp",
				Iteration:   1,
				SessionID:   "sess",
			},
			expected: "x-x-x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTemplateVars(tt.input, tt.req)
			if got != tt.expected {
				t.Errorf("expandTemplateVars(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
