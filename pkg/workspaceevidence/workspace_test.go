package workspaceevidence

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceStateFingerprint_DirectoryChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*testing.T, string)
		changed bool
	}{
		{name: "same size edit", changed: true, change: func(t *testing.T, root string) {
			before, err := os.Stat(filepath.Join(root, "source.txt"))
			if err != nil {
				t.Fatal(err)
			}
			writeFingerprintTestFile(t, root, "source.txt", "next\n")
			if err := os.Chtimes(filepath.Join(root, "source.txt"), before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "create", changed: true, change: func(t *testing.T, root string) {
			writeFingerprintTestFile(t, root, "new.txt", "new\n")
		}},
		{name: "rename", changed: true, change: func(t *testing.T, root string) {
			if err := os.Rename(filepath.Join(root, "source.txt"), filepath.Join(root, "renamed.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "delete", changed: true, change: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "source.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty directory", changed: true, change: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "runtime files", change: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".buckley"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFingerprintTestFile(t, root, ".buckley/buckley.db-wal", "runtime changes\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFingerprintTestFile(t, root, "source.txt", "base\n")
			before, err := WorkspaceStateFingerprint(context.Background(), root)
			if err != nil || !strings.HasPrefix(before, "files:") {
				t.Fatalf("initial fingerprint = %q, %v", before, err)
			}
			tc.change(t, root)
			after, err := WorkspaceStateFingerprint(context.Background(), root)
			if err != nil || (after != before) != tc.changed {
				t.Fatalf("changed=%v want=%v error=%v", after != before, tc.changed, err)
			}
			stable, err := WorkspaceStateFingerprintWithFallback(context.Background(), root)
			if err != nil || stable.Digest != after || stable.Warning != "" {
				t.Fatalf("stable fingerprint = %+v, %v", stable, err)
			}
		})
	}
}

func TestWorkspaceStateFingerprint_SymlinkTargetsStayOutsideSnapshot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFingerprintTestFile(t, outside, "private.txt", "outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := WorkspaceStateFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeFingerprintTestFile(t, outside, "private.txt", "changed\n")
	after, err := WorkspaceStateFingerprint(context.Background(), root)
	if err != nil || after != before {
		t.Fatalf("outside contents entered snapshot: %q, %v", after, err)
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "private.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	retargeted, err := WorkspaceStateFingerprint(context.Background(), root)
	if err != nil || retargeted == before {
		t.Fatalf("symlink retargeting was not observed: %q, %v", retargeted, err)
	}
}

func TestWorkspaceStateFingerprint_GitIdentityRemainsStrict(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	writeFingerprintTestFile(t, root, "source.txt", "base\n")
	want := fingerprintForTest(t, root)
	got, err := WorkspaceStateFingerprint(context.Background(), root)
	if err != nil || got != want {
		t.Fatalf("Git identity changed: %q want %q, %v", got, want, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("corrupt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, err := WorkspaceStateFingerprintWithFallback(context.Background(), root); err == nil || state.Digest != "" {
		t.Fatalf("corrupt Git repository used directory fallback: %+v, %v", state, err)
	}
}

func TestWorkspaceStateFingerprint_InvalidParentRepositoryFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceStateFingerprint(context.Background(), child); err == nil {
		t.Fatal("invalid ancestor repository was treated as an ordinary folder")
	}
}

func TestWorkspaceStateFingerprint_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WorkspaceStateFingerprint(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestWorkspaceStateFingerprint_RejectsSpecialFile(t *testing.T) {
	root := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(root, "socket"))
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if _, err := WorkspaceStateFingerprint(context.Background(), root); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("special file error = %v", err)
	}
}

func TestWorkspaceStateFingerprint_ExplicitGitDirectoryFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(root, "missing-git"))
	if state, err := WorkspaceStateFingerprintWithFallback(context.Background(), root); err == nil || state.Digest != "" {
		t.Fatalf("explicit invalid Git directory used fallback: %+v, %v", state, err)
	}
}

func TestReadDirectoryManifest_Bounds(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"a.txt", "b.txt", "c.txt"} {
		writeFingerprintTestFile(t, dir, path, "data\n")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	for _, tc := range []struct {
		name       string
		maxEntries int
		maxPaths   int64
		want       string
	}{
		{name: "entries", maxEntries: 2, maxPaths: 100, want: "2-entry"},
		{name: "paths", maxEntries: 3, maxPaths: 9, want: "9-byte"},
		{name: "exact bounds", maxEntries: 3, maxPaths: 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDirectoryManifest(context.Background(), root, tc.maxEntries, tc.maxPaths)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("limit error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWorkspaceStateFingerprint_RejectsOversizedContentBeforeReading(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFingerprintContentBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceStateFingerprint(context.Background(), root); err == nil || !strings.Contains(err.Error(), "content observation limit") {
		t.Fatalf("content limit error = %v", err)
	}
}

func TestWorkspaceMutationRecorder_OrdinaryAndGitFolders(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%v", git), func(t *testing.T) {
			root := t.TempDir()
			if git {
				runGitForFingerprintTest(t, root, "init", "-q")
			}
			writeFingerprintTestFile(t, root, "source.txt", "base\n")
			recorder, err := NewWorkspaceMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := recorder.Observe(context.Background()); err != nil || changed {
				t.Fatalf("stable folder = %v, %v", changed, err)
			}
			writeFingerprintTestFile(t, root, "source.txt", "edit\n")
			if changed, err := recorder.Observe(context.Background()); err != nil || !changed {
				t.Fatalf("changed folder = %v, %v", changed, err)
			}
			writeFingerprintTestFile(t, root, "source.txt", "base\n")
			if changed, err := recorder.Observe(context.Background()); err != nil || changed {
				t.Fatalf("restored folder = %v, %v", changed, err)
			}
		})
	}
}
