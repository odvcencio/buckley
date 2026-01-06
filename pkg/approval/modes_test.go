package approval

import (
	"testing"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		input   string
		want    Mode
		wantErr bool
	}{
		{"ask", ModeAsk, false},
		{"Ask", ModeAsk, false},
		{"ASK", ModeAsk, false},
		{"explicit", ModeAsk, false},
		{"safe", ModeSafe, false},
		{"readonly", ModeSafe, false},
		{"auto", ModeAuto, false},
		{"automatic", ModeAuto, false},
		{"yolo", ModeYolo, false},
		{"full", ModeYolo, false},
		{"dangerous", ModeYolo, false},
		{"invalid", ModeAsk, true},
		{"", ModeAsk, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseMode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseMode(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeAsk, "ask"},
		{ModeSafe, "safe"},
		{ModeAuto, "auto"},
		{ModeYolo, "yolo"},
		{Mode(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckReadOperations(t *testing.T) {
	ctx := Context{WorkspacePath: "/workspace"}

	// Read operations should always be allowed (except yolo which allows everything)
	modes := []Mode{ModeAsk, ModeSafe, ModeAuto, ModeYolo}

	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			req := Request{Operation: OpRead, Path: "/any/path"}
			result := Check(mode, req, ctx)
			if result.Decision != DecisionAllow {
				t.Errorf("Check(%v, OpRead) = %v, want Allow", mode, result.Decision)
			}
		})
	}
}

func TestCheckWriteOperations(t *testing.T) {
	ctx := Context{
		WorkspacePath: "/workspace",
		TrustedPaths:  []string{"/trusted"},
	}

	tests := []struct {
		name         string
		mode         Mode
		path         string
		wantDecision Decision
	}{
		// Ask mode requires approval for all writes
		{"ask-workspace", ModeAsk, "/workspace/file.go", DecisionPrompt},
		{"ask-outside", ModeAsk, "/other/file.go", DecisionPrompt},

		// Safe mode allows workspace writes
		{"safe-workspace", ModeSafe, "/workspace/file.go", DecisionAllow},
		{"safe-outside", ModeSafe, "/other/file.go", DecisionPrompt},
		{"safe-trusted", ModeSafe, "/trusted/file.go", DecisionAllow},

		// Auto mode allows workspace writes
		{"auto-workspace", ModeAuto, "/workspace/file.go", DecisionAllow},
		{"auto-outside", ModeAuto, "/other/file.go", DecisionPrompt},

		// Yolo mode allows everything
		{"yolo-anywhere", ModeYolo, "/any/path/file.go", DecisionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{Operation: OpWrite, Path: tt.path}
			result := Check(tt.mode, req, ctx)
			if result.Decision != tt.wantDecision {
				t.Errorf("Check(%v, OpWrite, %q) = %v, want %v",
					tt.mode, tt.path, result.Decision, tt.wantDecision)
			}
		})
	}
}

func TestCheckDeniedPaths(t *testing.T) {
	ctx := Context{
		WorkspacePath: "/workspace",
		DeniedPaths:   []string{"/workspace/secrets", "/etc"},
	}

	tests := []struct {
		name string
		path string
		want Decision
	}{
		{"denied-secrets", "/workspace/secrets/api-key", DecisionDeny},
		{"denied-etc", "/etc/passwd", DecisionDeny},
		{"allowed-workspace", "/workspace/src/main.go", DecisionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Even in yolo mode, denied paths should be denied
			// Actually, yolo mode allows everything, let's test with auto
			req := Request{Operation: OpWrite, Path: tt.path}
			result := Check(ModeAuto, req, ctx)
			if result.Decision != tt.want {
				t.Errorf("Check(ModeAuto, OpWrite, %q) = %v, want %v",
					tt.path, result.Decision, tt.want)
			}
		})
	}
}

func TestCheckShellOperations(t *testing.T) {
	ctx := Context{
		WorkspacePath: "/workspace",
		AllowNetwork:  false,
	}

	tests := []struct {
		name         string
		mode         Mode
		operation    Operation
		command      string
		wantDecision Decision
	}{
		// Ask mode requires approval for all shell
		{"ask-shell-read", ModeAsk, OpShellRead, "ls -la", DecisionPrompt},
		{"ask-shell-write", ModeAsk, OpShellWrite, "rm -rf .", DecisionPrompt},

		// Safe mode allows read-only shell
		{"safe-shell-read", ModeSafe, OpShellRead, "ls -la", DecisionAllow},
		{"safe-shell-write", ModeSafe, OpShellWrite, "rm file", DecisionPrompt},

		// Auto mode allows shell in workspace
		{"auto-shell-read", ModeAuto, OpShellRead, "cat file", DecisionAllow},
		{"auto-shell-write-workspace", ModeAuto, OpShellWrite, "go build", DecisionAllow},

		// Network commands
		{"safe-network", ModeSafe, OpShellNetwork, "curl http://example.com", DecisionPrompt},
		{"auto-network-disabled", ModeAuto, OpShellNetwork, "curl http://example.com", DecisionPrompt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{Operation: tt.operation, Command: tt.command}
			result := Check(tt.mode, req, ctx)
			if result.Decision != tt.wantDecision {
				t.Errorf("Check(%v, %v, %q) = %v, want %v",
					tt.mode, tt.operation, tt.command, result.Decision, tt.wantDecision)
			}
		})
	}
}

func TestCheckNetworkWithAllowNetwork(t *testing.T) {
	ctx := Context{
		WorkspacePath: "/workspace",
		AllowNetwork:  true,
	}

	req := Request{Operation: OpShellNetwork, Command: "curl http://api.example.com"}
	result := Check(ModeAuto, req, ctx)
	if result.Decision != DecisionAllow {
		t.Errorf("Check(ModeAuto, network) with AllowNetwork=true = %v, want Allow", result.Decision)
	}
}

func TestCheckGitOperations(t *testing.T) {
	ctx := Context{WorkspacePath: "/workspace"}

	tests := []struct {
		name         string
		mode         Mode
		command      string
		wantDecision Decision
	}{
		// Git read always allowed
		{"any-git-read", ModeAsk, "git status", DecisionAllow},

		// Ask mode requires approval for git writes
		{"ask-commit", ModeAsk, "git commit -m 'test'", DecisionPrompt},
		{"ask-push", ModeAsk, "git push origin main", DecisionPrompt},

		// Safe mode allows local git, not remote
		{"safe-commit", ModeSafe, "git commit -m 'test'", DecisionAllow},
		{"safe-push", ModeSafe, "git push origin main", DecisionPrompt},

		// Auto mode allows most git except force push
		{"auto-commit", ModeAuto, "git commit -m 'test'", DecisionAllow},
		{"auto-push", ModeAuto, "git push origin main", DecisionAllow},
		{"auto-force-push", ModeAuto, "git push -f origin main", DecisionPrompt},
		{"auto-force-push-long", ModeAuto, "git push --force origin main", DecisionPrompt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := OpGitWrite
			if tt.command == "git status" {
				op = OpGitRead
			}
			req := Request{Operation: op, Command: tt.command}
			result := Check(tt.mode, req, ctx)
			if result.Decision != tt.wantDecision {
				t.Errorf("Check(%v, %v, %q) = %v, want %v",
					tt.mode, op, tt.command, result.Decision, tt.wantDecision)
			}
		})
	}
}

func TestClassifyCommand(t *testing.T) {
	tests := []struct {
		command string
		want    Operation
	}{
		{"ls -la", OpShellRead},
		{"cat file.txt", OpShellRead},
		{"grep pattern file", OpShellRead},
		{"git status", OpShellRead},
		{"curl http://example.com", OpShellNetwork},
		{"wget http://example.com/file", OpShellNetwork},
		{"git push origin main", OpShellNetwork},
		{"npm install express", OpShellNetwork},
		{"rm -rf ./build", OpShellWrite},
		{"go build ./...", OpShellWrite},
		{"make clean", OpShellWrite},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := ClassifyCommand(tt.command)
			if got != tt.want {
				t.Errorf("ClassifyCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	readOnly := []string{
		"ls", "ls -la", "cat file", "head -n 10 file",
		"grep pattern", "git status", "git log", "go version",
	}

	for _, cmd := range readOnly {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true", cmd)
		}
	}

	notReadOnly := []string{
		"rm file", "mkdir dir", "mv src dst",
		"echo 'data' > file", "go build",
	}

	for _, cmd := range notReadOnly {
		if isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false", cmd)
		}
	}
}
