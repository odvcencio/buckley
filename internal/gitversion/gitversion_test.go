package gitversion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtLeast_ParsesGitVersionOutput(t *testing.T) {
	cases := []struct {
		name          string
		output        string
		major, minor  int
		wantOK        bool
		wantInstalled string
	}{
		{"newer minor", "git version 2.43.0\n", 2, 40, true, "2.43.0"},
		{"the same release", "git version 2.40.0\n", 2, 40, true, "2.40.0"},
		{"one minor older", "git version 2.39.5\n", 2, 40, false, "2.39.5"},
		{"minor compares as a number, not as text", "git version 2.9.5\n", 2, 29, false, "2.9.5"},
		{"a larger major wins over a smaller minor", "git version 3.0.0\n", 2, 40, true, "3.0.0"},
		{"a smaller major loses to a larger minor", "git version 1.99.0\n", 2, 0, false, "1.99.0"},
		{"vendor suffix in parentheses", "git version 2.39.5 (Apple Git-154)\n", 2, 40, false, "2.39.5"},
		{"vendor suffix after the number", "git version 2.44.0.windows.1\n", 2, 40, true, "2.44.0.windows.1"},
		{"release candidate", "git version 2.40.0-rc1\n", 2, 40, true, "2.40.0-rc1"},
		{"text that is not a version line", "hello\n", 2, 40, false, Unknown},
		{"empty output", "", 2, 40, false, Unknown},
		{"a version with no minor", "git version 2\n", 2, 40, false, Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, installed := atLeast(tc.output, tc.major, tc.minor)
			if ok != tc.wantOK || installed != tc.wantInstalled {
				t.Fatalf("atLeast(%q, %d, %d) = %v, %q, want %v, %q", tc.output, tc.major, tc.minor, ok, installed, tc.wantOK, tc.wantInstalled)
			}
		})
	}
}

func TestAtLeast_RunsTheGitOnPath(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'git version 2.39.1'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if ok, installed := AtLeast(2, 40); ok || installed != "2.39.1" {
		t.Fatalf("AtLeast(2, 40) = %v, %q, want false, 2.39.1", ok, installed)
	}
	if ok, installed := AtLeast(2, 39); !ok || installed != "2.39.1" {
		t.Fatalf("AtLeast(2, 39) = %v, %q, want true, 2.39.1", ok, installed)
	}
}

func TestAtLeast_MissingGitIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if ok, installed := AtLeast(2, 29); ok || installed != Unknown {
		t.Fatalf("AtLeast() = %v, %q, want false, %q", ok, installed, Unknown)
	}
}
