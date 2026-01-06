package builtin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkDirAwareExecContextDefaultNoDeadline(t *testing.T) {
	w := &workDirAware{}
	ctx, cancel := w.execContext()
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatalf("expected no deadline")
	}
}

func TestWorkDirAwareExecContextSetsDeadline(t *testing.T) {
	w := &workDirAware{}
	w.SetMaxExecTimeSeconds(1)

	ctx, cancel := w.execContext()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("expected deadline")
	}
	if time.Until(deadline) <= 0 {
		t.Fatalf("expected deadline in the future")
	}
}

func TestWorkDirAwareSetters(t *testing.T) {
	t.Run("SetWorkDir", func(t *testing.T) {
		w := &workDirAware{}
		w.SetWorkDir("/some/path")
		if w.workDir != "/some/path" {
			t.Errorf("workDir = %q, want %q", w.workDir, "/some/path")
		}
		w.SetWorkDir("  /trimmed  ")
		if w.workDir != "/trimmed" {
			t.Errorf("workDir = %q, want %q", w.workDir, "/trimmed")
		}
	})

	t.Run("SetWorkDir nil receiver", func(t *testing.T) {
		var w *workDirAware
		w.SetWorkDir("/path") // should not panic
	})

	t.Run("SetEnv", func(t *testing.T) {
		w := &workDirAware{}
		w.SetEnv(map[string]string{"FOO": "bar"})
		if w.env == nil || w.env["FOO"] != "bar" {
			t.Errorf("env not set correctly")
		}
	})

	t.Run("SetEnv nil receiver", func(t *testing.T) {
		var w *workDirAware
		w.SetEnv(map[string]string{"FOO": "bar"}) // should not panic
	})

	t.Run("SetMaxFileSizeBytes", func(t *testing.T) {
		w := &workDirAware{}
		w.SetMaxFileSizeBytes(1024)
		if w.maxFileSizeBytes != 1024 {
			t.Errorf("maxFileSizeBytes = %d, want 1024", w.maxFileSizeBytes)
		}
		w.SetMaxFileSizeBytes(0)
		if w.maxFileSizeBytes != 0 {
			t.Errorf("maxFileSizeBytes = %d, want 0", w.maxFileSizeBytes)
		}
		w.SetMaxFileSizeBytes(-1)
		if w.maxFileSizeBytes != 0 {
			t.Errorf("maxFileSizeBytes = %d, want 0", w.maxFileSizeBytes)
		}
	})

	t.Run("SetMaxFileSizeBytes nil receiver", func(t *testing.T) {
		var w *workDirAware
		w.SetMaxFileSizeBytes(1024) // should not panic
	})

	t.Run("SetMaxOutputBytes", func(t *testing.T) {
		w := &workDirAware{}
		w.SetMaxOutputBytes(512)
		if w.maxOutputBytes != 512 {
			t.Errorf("maxOutputBytes = %d, want 512", w.maxOutputBytes)
		}
		w.SetMaxOutputBytes(0)
		if w.maxOutputBytes != 0 {
			t.Errorf("maxOutputBytes = %d, want 0", w.maxOutputBytes)
		}
		w.SetMaxOutputBytes(-1)
		if w.maxOutputBytes != 0 {
			t.Errorf("maxOutputBytes = %d, want 0", w.maxOutputBytes)
		}
	})

	t.Run("SetMaxOutputBytes nil receiver", func(t *testing.T) {
		var w *workDirAware
		w.SetMaxOutputBytes(512) // should not panic
	})

	t.Run("SetMaxExecTimeSeconds", func(t *testing.T) {
		w := &workDirAware{}
		w.SetMaxExecTimeSeconds(30)
		if w.maxExecTime != 30*time.Second {
			t.Errorf("maxExecTime = %v, want 30s", w.maxExecTime)
		}
		w.SetMaxExecTimeSeconds(0)
		if w.maxExecTime != 0 {
			t.Errorf("maxExecTime = %v, want 0", w.maxExecTime)
		}
		w.SetMaxExecTimeSeconds(-1)
		if w.maxExecTime != 0 {
			t.Errorf("maxExecTime = %v, want 0", w.maxExecTime)
		}
	})

	t.Run("SetMaxExecTimeSeconds nil receiver", func(t *testing.T) {
		var w *workDirAware
		w.SetMaxExecTimeSeconds(30) // should not panic
	})
}

func TestExecContextNilReceiver(t *testing.T) {
	var w *workDirAware
	ctx, cancel := w.execContext()
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("nil receiver should return context without deadline")
	}
}

func TestIsWithinDir(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		target string
		want   bool
	}{
		{name: "empty base", base: "", target: "/foo", want: false},
		{name: "empty target", base: "/foo", target: "", want: false},
		{name: "same directory", base: "/foo", target: "/foo", want: true},
		{name: "child directory", base: "/foo", target: "/foo/bar", want: true},
		{name: "nested child", base: "/foo", target: "/foo/bar/baz", want: true},
		{name: "parent escape", base: "/foo/bar", target: "/foo", want: false},
		{name: "sibling escape", base: "/foo", target: "/bar", want: false},
		{name: "dotdot escape", base: "/foo", target: "/foo/../bar", want: false},
		{name: "whitespace trimmed", base: "  /foo  ", target: "  /foo/bar  ", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isWithinDir(tc.base, tc.target)
			if got != tc.want {
				t.Errorf("isWithinDir(%q, %q) = %v, want %v", tc.base, tc.target, got, tc.want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		workDir   string
		raw       string
		wantErr   bool
		wantAbs   bool
		checkPath string
	}{
		{
			name:    "empty raw path",
			workDir: tmpDir,
			raw:     "",
			wantErr: true,
		},
		{
			name:    "whitespace raw path",
			workDir: tmpDir,
			raw:     "   ",
			wantErr: true,
		},
		{
			name:    "relative path within workDir",
			workDir: tmpDir,
			raw:     "subdir/file.txt",
			wantErr: false,
			wantAbs: true,
		},
		{
			name:    "parent escape blocked",
			workDir: tmpDir,
			raw:     "../escape.txt",
			wantErr: true,
		},
		{
			name:    "absolute path within workDir",
			workDir: tmpDir,
			raw:     filepath.Join(tmpDir, "file.txt"),
			wantErr: false,
			wantAbs: true,
		},
		{
			name:    "absolute path escapes workDir",
			workDir: tmpDir,
			raw:     "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "empty workDir allows any path",
			workDir: "",
			raw:     "/some/path",
			wantErr: false,
			wantAbs: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePath(tc.workDir, tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tc.wantAbs && !filepath.IsAbs(got) {
				t.Errorf("expected absolute path, got %q", got)
			}
		})
	}
}

func TestResolveRelPath(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("within workDir", func(t *testing.T) {
		abs, rel, err := resolveRelPath(tmpDir, "subdir/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !filepath.IsAbs(abs) {
			t.Errorf("abs should be absolute, got %q", abs)
		}
		if filepath.IsAbs(rel) {
			t.Errorf("rel should be relative, got %q", rel)
		}
	})

	t.Run("empty workDir", func(t *testing.T) {
		abs, rel, err := resolveRelPath("", "file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if abs != rel {
			t.Errorf("with empty workDir, abs and rel should be equal")
		}
	})

	t.Run("error path", func(t *testing.T) {
		_, _, err := resolveRelPath(tmpDir, "../escape.txt")
		if err == nil {
			t.Error("expected error for escaped path")
		}
	})
}

func TestEvalSymlinksFallback(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		got := evalSymlinksFallback("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("whitespace path", func(t *testing.T) {
		got := evalSymlinksFallback("   ")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("real path", func(t *testing.T) {
		tmpDir := t.TempDir()
		got := evalSymlinksFallback(tmpDir)
		if got == "" {
			t.Error("expected non-empty path")
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		got := evalSymlinksFallback("/nonexistent/path/12345")
		if got == "" {
			t.Error("should return cleaned path even if nonexistent")
		}
	})
}

func TestEvalSymlinksFallbackForTarget(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		got := evalSymlinksFallbackForTarget("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("whitespace path", func(t *testing.T) {
		got := evalSymlinksFallbackForTarget("   ")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		file := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
		got := evalSymlinksFallbackForTarget(file)
		if got == "" {
			t.Error("expected non-empty path")
		}
	})

	t.Run("nonexistent target in existing dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "nonexistent.txt")
		got := evalSymlinksFallbackForTarget(target)
		if got == "" {
			t.Error("expected non-empty path")
		}
	})

	t.Run("nonexistent parent", func(t *testing.T) {
		got := evalSymlinksFallbackForTarget("/nonexistent/parent/file.txt")
		if got == "" {
			t.Error("expected non-empty path")
		}
	})
}
