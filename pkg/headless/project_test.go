package headless

import (
	"testing"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid URLs
		{name: "https URL", input: "https://github.com/user/repo.git", expected: true},
		{name: "https URL without .git", input: "https://github.com/user/repo", expected: true},
		{name: "http URL", input: "http://github.com/user/repo.git", expected: true},
		{name: "ssh URL", input: "ssh://git@github.com/user/repo.git", expected: true},
		{name: "git protocol URL", input: "git://github.com/user/repo.git", expected: true},
		{name: "file URL", input: "file:///path/to/repo", expected: true},
		{name: "scp-style URL", input: "git@github.com:user/repo.git", expected: true},
		{name: "scp-style URL without .git", input: "git@github.com:user/repo", expected: true},
		{name: "scp-style with path", input: "user@host:org/project/repo", expected: true},

		// Invalid URLs
		{name: "empty string", input: "", expected: false},
		{name: "whitespace only", input: "   ", expected: false},
		{name: "local path", input: "/home/user/repo", expected: false},
		{name: "relative path", input: "./repo", expected: false},
		{name: "Windows absolute path", input: "C:/Users/repo", expected: false},
		{name: "Windows drive letter path", input: "D:\\Projects\\repo", expected: false},
		{name: "unknown scheme", input: "ftp://host/repo", expected: false},
		{name: "no path after colon", input: "git@github.com:", expected: false},
		{name: "colon at start", input: ":user/repo", expected: false},
		{name: "whitespace in URL", input: "git@github.com:user /repo", expected: false},
		{name: "just hostname with colon", input: "github.com:", expected: false},
		{name: "invalid scheme URL", input: "mailto://host/repo", expected: false},
		{name: "path with slash in host part", input: "user/host:repo", expected: false},
		{name: "path with backslash in host part", input: "user\\host:repo", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsGitURL(tc.input)
			if got != tc.expected {
				t.Errorf("IsGitURL(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsDriveLetterPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "C drive", input: "C:\\Users", expected: true},
		{name: "D drive", input: "D:/Projects", expected: true},
		{name: "lowercase drive", input: "c:\\users", expected: true},
		{name: "short path", input: "C:", expected: true},
		{name: "too short", input: "C", expected: false},
		{name: "no colon", input: "C/Users", expected: false},
		{name: "number instead of letter", input: "1:\\Users", expected: false},
		{name: "unix path", input: "/home/user", expected: false},
		{name: "empty", input: "", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isDriveLetterPath(tc.input)
			if got != tc.expected {
				t.Errorf("isDriveLetterPath(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsWithinDir(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		target   string
		expected bool
	}{
		{name: "same directory", base: "/home/user", target: "/home/user", expected: true},
		{name: "subdirectory", base: "/home/user", target: "/home/user/projects", expected: true},
		{name: "deep subdirectory", base: "/home/user", target: "/home/user/projects/foo/bar", expected: true},
		{name: "parent directory", base: "/home/user/projects", target: "/home/user", expected: false},
		{name: "sibling directory", base: "/home/user", target: "/home/other", expected: false},
		{name: "unrelated directory", base: "/home/user", target: "/var/log", expected: false},
		{name: "path traversal attempt", base: "/home/user", target: "/home/user/../other", expected: false},
		{name: "dot path in target", base: "/home/user", target: "/home/user/./projects", expected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isWithinDir(tc.base, tc.target)
			if got != tc.expected {
				t.Errorf("isWithinDir(%q, %q) = %v, want %v", tc.base, tc.target, got, tc.expected)
			}
		})
	}
}
