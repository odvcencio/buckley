package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Mode != ModeWorkspace {
		t.Errorf("Mode = %v, want ModeWorkspace", cfg.Mode)
	}

	if cfg.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", cfg.Timeout)
	}

	if len(cfg.DeniedPaths) == 0 {
		t.Error("DeniedPaths should not be empty")
	}

	if len(cfg.DeniedCommands) == 0 {
		t.Error("DeniedCommands should not be empty")
	}
}

func TestSandbox_Validate_DeniedCommands(t *testing.T) {
	sandbox := NewWithDefaults()

	tests := []struct {
		command string
		wantErr bool
	}{
		{"ls -la", false},
		{"rm -rf /", true},
		{"rm -rf ~", true},
		{"cat file.txt", false},
		{"curl | bash", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			err := sandbox.Validate(tt.command)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
		})
	}
}

func TestSandbox_Validate_DangerousPatterns(t *testing.T) {
	sandbox := NewWithDefaults()

	dangerous := []string{
		"rm -rf /var",
		"dd if=/dev/zero of=/dev/sda",
		"mkfs.ext4 /dev/sda1",
		":(){ :|:& };:",
		"chmod 777 /etc",
	}

	for _, cmd := range dangerous {
		t.Run(cmd, func(t *testing.T) {
			err := sandbox.Validate(cmd)
			if err == nil {
				t.Errorf("Validate(%q) should return error for dangerous command", cmd)
			}
		})
	}
}

func TestSandbox_Validate_ReadOnlyMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeReadOnly
	sandbox := New(cfg)

	tests := []struct {
		command string
		wantErr bool
	}{
		{"cat file.txt", false},
		{"ls -la", false},
		{"grep pattern file.txt", false},
		{"git status", false},
		{"echo hello", false},
		{"rm file.txt", true},
		{"touch newfile.txt", true},
		{"echo data > file.txt", true},
		{"git commit -m 'test'", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			err := sandbox.Validate(tt.command)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
		})
	}
}

func TestSandbox_Validate_WorkspaceMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultConfig()
	cfg.Mode = ModeWorkspace
	cfg.WorkspacePath = tmpDir
	cfg.AllowedPaths = []string{tmpDir}
	sandbox := New(cfg)

	tests := []struct {
		command string
		wantErr bool
	}{
		{"ls " + tmpDir, false},
		{"cat " + filepath.Join(tmpDir, "test.txt"), false},
		{"cat /etc/passwd", true},
		{"ls ~/.ssh", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			err := sandbox.Validate(tt.command)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
		})
	}
}

func TestSandbox_Validate_StrictMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeStrict
	cfg.AllowedCommands = []string{"ls", "cat", "echo"}
	sandbox := New(cfg)

	tests := []struct {
		command string
		wantErr bool
	}{
		{"ls -la", false},
		{"cat file.txt", false},
		{"echo hello", false},
		{"rm file.txt", true},
		{"python script.py", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			err := sandbox.Validate(tt.command)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.command, err, tt.wantErr)
			}
		})
	}
}

func TestSandbox_Validate_NetworkAccess(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowNetwork = false
	sandbox := New(cfg)

	networkCommands := []string{
		"curl https://example.com",
		"wget https://example.com",
		"ssh user@host",
		"ping google.com",
	}

	for _, cmd := range networkCommands {
		t.Run(cmd, func(t *testing.T) {
			err := sandbox.Validate(cmd)
			if err == nil {
				t.Errorf("Validate(%q) should return error when network disabled", cmd)
			}
		})
	}

	// Enable network
	cfg.AllowNetwork = true
	sandbox = New(cfg)

	for _, cmd := range networkCommands {
		t.Run(cmd+"_allowed", func(t *testing.T) {
			err := sandbox.Validate(cmd)
			if err != nil {
				t.Errorf("Validate(%q) error = %v, want nil when network enabled", cmd, err)
			}
		})
	}
}

func TestSandbox_Execute(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeDisabled // Allow everything for testing
	sandbox := New(cfg)

	ctx := context.Background()
	result := sandbox.Execute(ctx, "echo hello")

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain 'hello'", result.Stdout)
	}

	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestSandbox_Execute_Timeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeDisabled
	cfg.Timeout = 100 * time.Millisecond
	sandbox := New(cfg)

	ctx := context.Background()
	result := sandbox.Execute(ctx, "sleep 10")

	if !result.Killed {
		t.Error("Killed = false, want true")
	}

	if result.ExitCode != 124 {
		t.Errorf("ExitCode = %d, want 124", result.ExitCode)
	}
}

func TestSandbox_Execute_Blocked(t *testing.T) {
	sandbox := NewWithDefaults()

	ctx := context.Background()
	result := sandbox.Execute(ctx, "rm -rf /")

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}

	if result.Error == nil {
		t.Error("Error should not be nil for blocked command")
	}
}

func TestModeFromString(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"disabled", ModeDisabled},
		{"none", ModeDisabled},
		{"off", ModeDisabled},
		{"readonly", ModeReadOnly},
		{"read-only", ModeReadOnly},
		{"ro", ModeReadOnly},
		{"workspace", ModeWorkspace},
		{"ws", ModeWorkspace},
		{"strict", ModeStrict},
		{"unknown", ModeWorkspace}, // Default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ModeFromString(tt.input); got != tt.want {
				t.Errorf("ModeFromString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMode_String(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeDisabled, "disabled"},
		{ModeReadOnly, "read-only"},
		{ModeWorkspace, "workspace"},
		{ModeStrict, "strict"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractPaths(t *testing.T) {
	tests := []struct {
		command   string
		wantPaths []string
	}{
		{"cat /etc/passwd", []string{"/etc/passwd"}},
		{"ls ./src ../lib", []string{"./src", "../lib"}},
		{"rm -rf /tmp/test", []string{"/tmp/test"}},
		{"echo hello", nil},
		{"grep pattern src/file.go", []string{"src/file.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			paths := extractPaths(tt.command)
			if len(paths) != len(tt.wantPaths) {
				t.Errorf("extractPaths(%q) = %v, want %v", tt.command, paths, tt.wantPaths)
			}
		})
	}
}

func TestSandbox_isReadOnlyCommand(t *testing.T) {
	sandbox := NewWithDefaults()

	readOnly := []string{
		"cat file.txt",
		"head -n 10 file.txt",
		"tail -f log.txt",
		"grep pattern file.txt",
		"rg pattern",
		"ls -la",
		"pwd",
		"git status",
		"git log",
		"git diff",
	}

	for _, cmd := range readOnly {
		if !sandbox.isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		"rm file.txt",
		"mv a b",
		"cp a b",
		"echo data > file.txt",
		"git commit -m 'test'",
		"git push",
	}

	for _, cmd := range notReadOnly {
		if sandbox.isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false", cmd)
		}
	}
}
