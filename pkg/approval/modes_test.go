package approval

import (
	"testing"
)

// Read operations should always be allowed (except yolo which allows everything)

// Ask mode requires approval for all writes

// Safe mode allows workspace writes

// Auto mode allows workspace writes

// Yolo mode allows everything

// Even in yolo mode, denied paths should be denied
// Actually, yolo mode allows everything, let's test with auto

// Ask mode requires approval for all shell

// Safe mode allows read-only shell

// Auto mode allows shell in workspace

// Network commands

// Git read always allowed

// Ask mode requires approval for git writes

// Safe mode allows local git, not remote

// Auto mode allows most git except force push

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
