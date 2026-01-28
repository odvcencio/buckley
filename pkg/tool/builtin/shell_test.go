package builtin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShellCommandTool(t *testing.T) {
	tool := &ShellCommandTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "run_shell" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "run_shell")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("echo command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "echo hello",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		if stdout, ok := result.Data["stdout"].(string); ok {
			if !strings.Contains(stdout, "hello") {
				t.Errorf("expected stdout to contain 'hello', got %q", stdout)
			}
		}
	})

	t.Run("failing command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "exit 1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for exit 1")
		}
		if exitCode, ok := result.Data["exit_code"].(int); ok {
			if exitCode != 1 {
				t.Errorf("expected exit_code=1, got %d", exitCode)
			}
		}
	})

	t.Run("command with stderr", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "echo error >&2",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Command succeeds but has stderr output
		if stderr, ok := result.Data["stderr"].(string); ok {
			if !strings.Contains(stderr, "error") {
				t.Errorf("expected stderr to contain 'error', got %q", stderr)
			}
		}
	})

	t.Run("truncates stdout when output limit is set", func(t *testing.T) {
		tool := &ShellCommandTool{}
		tool.SetMaxOutputBytes(5)

		result, err := tool.Execute(map[string]any{
			"command": "printf '1234567890'",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		stdout, _ := result.Data["stdout"].(string)
		if len(stdout) > 5 {
			t.Fatalf("expected stdout <= 5 bytes, got %d", len(stdout))
		}
		if truncated, ok := result.Data["stdout_truncated"].(bool); !ok || !truncated {
			t.Fatalf("expected stdout_truncated=true, got %v", result.Data["stdout_truncated"])
		}
		if !result.ShouldAbridge || result.DisplayData == nil {
			t.Fatal("expected abridged display data when stdout is truncated")
		}
	})

	t.Run("command with timeout", func(t *testing.T) {
		// Test with a very short timeout
		result, err := tool.Execute(map[string]any{
			"command":         "sleep 10",
			"timeout_seconds": 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure due to timeout")
		}
		// The error should indicate timeout
		if !strings.Contains(result.Error, "timeout") && !strings.Contains(result.Error, "killed") {
			t.Logf("timeout test result: success=%v, error=%s", result.Success, result.Error)
		}
	})

	t.Run("missing command parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing command")
		}
	})

	t.Run("empty command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for empty command")
		}
	})

	t.Run("whitespace only command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "   ",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for whitespace command")
		}
	})

	t.Run("complex pipeline", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "echo -e 'a\\nb\\nc' | wc -l",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
	})

	t.Run("command with quoted args", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": `echo "hello world"`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		if stdout, ok := result.Data["stdout"].(string); ok {
			if !strings.Contains(stdout, "hello world") {
				t.Errorf("expected 'hello world', got %q", stdout)
			}
		}
	})

	t.Run("execution time is recorded", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "sleep 0.1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		// Check that duration is recorded
		if _, ok := result.Data["duration_ms"]; !ok {
			t.Logf("duration_ms not present in result data")
		}
	})

	t.Run("nonexistent command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "nonexistent_command_12345",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent command")
		}
	})

	t.Run("timeout clamped to max", func(t *testing.T) {
		// Timeout > 600 should be clamped
		result, err := tool.Execute(map[string]any{
			"command":         "echo fast",
			"timeout_seconds": 1000,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
	})

	t.Run("negative timeout uses default", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command":         "echo fast",
			"timeout_seconds": -5,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
	})
}

func TestShellCommandToolStreaming(t *testing.T) {
	tool := &ShellCommandTool{}

	t.Run("large output", func(t *testing.T) {
		// Generate larger output to test buffering
		result, err := tool.Execute(map[string]any{
			"command": "seq 1 1000",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		if stdout, ok := result.Data["stdout"].(string); ok {
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			if len(lines) != 1000 {
				t.Errorf("expected 1000 lines, got %d", len(lines))
			}
		}
	})
}

func TestShellCommandToolConcurrency(t *testing.T) {
	tool := &ShellCommandTool{}

	t.Run("concurrent executions", func(t *testing.T) {
		done := make(chan bool, 3)

		for i := 0; i < 3; i++ {
			go func(idx int) {
				result, err := tool.Execute(map[string]any{
					"command": "echo test",
				})
				if err != nil || !result.Success {
					t.Errorf("concurrent execution %d failed", idx)
				}
				done <- true
			}(i)
		}

		// Wait with timeout
		timeout := time.After(5 * time.Second)
		for i := 0; i < 3; i++ {
			select {
			case <-done:
			case <-timeout:
				t.Fatal("timeout waiting for concurrent executions")
			}
		}
	})
}

func TestShellCommandToolContainerMode(t *testing.T) {
	tool := &ShellCommandTool{}

	t.Run("configure container mode", func(t *testing.T) {
		// Configure container mode
		tool.ConfigureContainerMode("/path/to/compose.yaml", "myservice", "/workdir")

		// Check that it was configured (we can't easily verify internal state,
		// but we can verify it doesn't panic)
		if tool.containerEnabled != true {
			t.Error("expected containerEnabled=true after configuration")
		}
	})
}

func TestShellEscapeSingleQuotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple string", input: "hello", want: "'hello'"},
		{name: "empty string", input: "", want: "''"},
		{name: "string with single quote", input: "it's fine", want: "'it'\\''s fine'"},
		{name: "multiple quotes", input: "a'b'c", want: "'a'\\''b'\\''c'"},
		{name: "path with spaces", input: "/path/to/my file.txt", want: "'/path/to/my file.txt'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscapeSingleQuotes(tc.input)
			if got != tc.want {
				t.Errorf("shellEscapeSingleQuotes(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple string", input: "hello", want: "hello"},
		{name: "double quote", input: `say "hello"`, want: `say \"hello\"`},
		{name: "backslash", input: `path\to\file`, want: `path\\to\\file`},
		{name: "both", input: `"test\path"`, want: `\"test\\path\"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeAppleScript(tc.input)
			if got != tc.want {
				t.Errorf("escapeAppleScript(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseBoolParam(t *testing.T) {
	tests := []struct {
		name       string
		value      any
		defaultVal bool
		want       bool
	}{
		{name: "bool true", value: true, defaultVal: false, want: true},
		{name: "bool false", value: false, defaultVal: true, want: false},
		{name: "string true", value: "true", defaultVal: false, want: true},
		{name: "string True", value: "True", defaultVal: false, want: true},
		{name: "string TRUE", value: "TRUE", defaultVal: false, want: true},
		{name: "string 1", value: "1", defaultVal: false, want: true},
		{name: "string yes", value: "yes", defaultVal: false, want: true},
		{name: "string on", value: "on", defaultVal: false, want: true},
		{name: "string false", value: "false", defaultVal: true, want: false},
		{name: "string 0", value: "0", defaultVal: true, want: false},
		{name: "string no", value: "no", defaultVal: true, want: false},
		{name: "string off", value: "off", defaultVal: true, want: false},
		{name: "string with whitespace", value: "  true  ", defaultVal: false, want: true},
		{name: "invalid string", value: "maybe", defaultVal: true, want: true},
		{name: "nil", value: nil, defaultVal: true, want: true},
		{name: "int value", value: 1, defaultVal: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBoolParam(tc.value, tc.defaultVal)
			if got != tc.want {
				t.Errorf("parseBoolParam(%v, %v) = %v, want %v", tc.value, tc.defaultVal, got, tc.want)
			}
		})
	}
}

func TestTimeoutContext(t *testing.T) {
	t.Run("zero timeout", func(t *testing.T) {
		ctx, cancel := timeoutContext(context.Background(), 0)
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Error("zero timeout should not set deadline")
		}
	})

	t.Run("negative timeout", func(t *testing.T) {
		ctx, cancel := timeoutContext(context.Background(), -1)
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Error("negative timeout should not set deadline")
		}
	})

	t.Run("positive timeout", func(t *testing.T) {
		ctx, cancel := timeoutContext(context.Background(), 60)
		defer cancel()
		if _, ok := ctx.Deadline(); !ok {
			t.Error("positive timeout should set deadline")
		}
	})
}

func TestInTmux(t *testing.T) {
	// Just test that it doesn't panic
	result := inTmux()
	_ = result // result depends on environment
}

func TestHasGUIEnvironment(t *testing.T) {
	// Just test that it doesn't panic and returns a bool
	result := hasGUIEnvironment()
	_ = result // result depends on OS and environment
}
