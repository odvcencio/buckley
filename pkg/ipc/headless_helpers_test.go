package ipc

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsWithinPath(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		target string
		want   bool
	}{
		{
			name:   "same path",
			base:   "/home/user/project",
			target: "/home/user/project",
			want:   true,
		},
		{
			name:   "child path",
			base:   "/home/user/project",
			target: "/home/user/project/src",
			want:   true,
		},
		{
			name:   "deeply nested child",
			base:   "/home/user/project",
			target: "/home/user/project/src/pkg/main.go",
			want:   true,
		},
		{
			name:   "parent path - not allowed",
			base:   "/home/user/project",
			target: "/home/user",
			want:   false,
		},
		{
			name:   "sibling path - not allowed",
			base:   "/home/user/project",
			target: "/home/user/other",
			want:   false,
		},
		{
			name:   "path traversal attempt",
			base:   "/home/user/project",
			target: "/home/user/project/../other",
			want:   false,
		},
		{
			name:   "normalized child path",
			base:   "/home/user/project",
			target: "/home/user/project/./src/../src/main.go",
			want:   true,
		},
		{
			name:   "completely different path",
			base:   "/home/user/project",
			target: "/tmp/something",
			want:   false,
		},
	}

	for _, tt := range tests {
		// Skip tests that rely on Unix paths on Windows
		if runtime.GOOS == "windows" {
			continue
		}

		t.Run(tt.name, func(t *testing.T) {
			got := isWithinPath(tt.base, tt.target)
			if got != tt.want {
				t.Errorf("isWithinPath(%q, %q) = %v, want %v", tt.base, tt.target, got, tt.want)
			}
		})
	}
}

func TestIsWithinPathRelative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix paths")
	}

	// Test with relative-looking paths that get cleaned
	base := "/home/user/project"
	target := filepath.Join(base, "subdir")

	if !isWithinPath(base, target) {
		t.Errorf("expected %q to be within %q", target, base)
	}
}
