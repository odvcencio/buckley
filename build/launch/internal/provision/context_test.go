package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestToolchainLock_ExactAndFailClosed(t *testing.T) {
	assets := testAssetsRoot(t)
	lock, digest, err := LoadToolchainLock(filepath.Join(assets, "toolchain.lock"))
	if err != nil || lock != ExpectedToolchain() || !sha256Pattern.MatchString(digest) {
		t.Fatalf("LoadToolchainLock = %+v, %q, %v", lock, digest, err)
	}
	data, err := os.ReadFile(filepath.Join(assets, "toolchain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["tinygo_sha256"] = strings.Repeat("0", 64)
	tampered, _ := json.Marshal(raw)
	path := filepath.Join(t.TempDir(), "toolchain.lock")
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadToolchainLock(path); err == nil {
		t.Fatal("tampered toolchain lock accepted")
	}
	raw["tinygo_sha256"] = TinyGoSHA256
	raw["unknown"] = true
	unknown, _ := json.Marshal(raw)
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadToolchainLock(path); err == nil {
		t.Fatal("unknown toolchain field accepted")
	}
	hardlink := filepath.Join(t.TempDir(), "hardlinked.lock")
	if err := os.Link(filepath.Join(assets, "toolchain.lock"), hardlink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadToolchainLock(hardlink); err == nil {
		t.Fatal("hardlinked toolchain lock accepted")
	}
}

func TestTinyGoLicense_ExactOfficialBytesIncludingFinalNewline(t *testing.T) {
	path := filepath.Join(testAssetsRoot(t), "licenses", "TinyGo-LICENSE")
	data, err := readStableRegular(path, maxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != TinyGoLicenseSH {
		t.Fatalf("TinyGo license digest = %s, want %s", got, TinyGoLicenseSH)
	}
	if len(data) != 1835 || data[len(data)-1] != '\n' {
		t.Fatalf("TinyGo license byte contract = %d bytes, final=%#x", len(data), data[len(data)-1])
	}
}

func TestSynthesizeBuildContext_AllowlistedDeterministicAndSourceFree(t *testing.T) {
	roots := newModuleSourceFixtures(t)
	assets := testAssetsRoot(t)
	destination1 := filepath.Join(t.TempDir(), "context-one")
	manifest1, lock1, err := SynthesizeBuildContext(context.Background(), assets, destination1, roots)
	if err != nil {
		t.Fatalf("SynthesizeBuildContext one: %v", err)
	}
	destination2 := filepath.Join(t.TempDir(), "context-two")
	manifest2, lock2, err := SynthesizeBuildContext(context.Background(), assets, destination2, roots)
	if err != nil {
		t.Fatalf("SynthesizeBuildContext two: %v", err)
	}
	if manifest1 != manifest2 || !equalModuleLocks(lock1, lock2) {
		t.Fatalf("context is nondeterministic:\n%+v\n%+v", manifest1, manifest2)
	}
	if manifest1.Entries != 24 || manifest1.Bytes <= 0 {
		t.Fatalf("context bounds = %+v, want 24 nonempty entries", manifest1)
	}
	var paths []string
	err = filepath.WalkDir(destination1, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			info, statErr := entry.Info()
			if statErr != nil || info.ModTime().Unix() != SourceDateEpoch {
				t.Fatalf("directory timestamp %q = %+v, %v", path, info, statErr)
			}
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.ModTime().Unix() != SourceDateEpoch {
			t.Fatalf("file timestamp %q = %+v, %v", path, info, statErr)
		}
		relative, _ := filepath.Rel(destination1, path)
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, forbidden := range []string{"source.go", ".git", "go.work", "go.work.sum"} {
		for _, path := range paths {
			if strings.Contains(path, forbidden) {
				t.Fatalf("forbidden build-context path %q in %v", path, paths)
			}
		}
	}
	for _, required := range []string{"Dockerfile", "module-lock.json", "modules/gsxmail/go.mod", "modules/gosx/editor/go.mod", "modules/gosx/cmd/buildbootstrap/go.mod", "modules/tqwebp/bench/deepteams/go.mod", "launch/cmd/probe/main_linux.go", "launch/cmd/supervisor/main_linux.go"} {
		if _, err := os.Stat(filepath.Join(destination1, filepath.FromSlash(required))); err != nil {
			t.Fatalf("required context asset %q: %v", required, err)
		}
	}
}

func TestSynthesizeBuildContext_RejectsDirtyManifestAndSymlink(t *testing.T) {
	assets := testAssetsRoot(t)
	t.Run("dirty", func(t *testing.T) {
		roots := newModuleSourceFixtures(t)
		if err := os.WriteFile(filepath.Join(roots.GSXMail, "go.mod"), []byte("module example.com/drift\n\ngo 1.26.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "context")
		if _, _, err := SynthesizeBuildContext(context.Background(), assets, destination, roots); err == nil {
			t.Fatal("dirty module manifest accepted")
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed context not removed: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		roots := newModuleSourceFixtures(t)
		path := filepath.Join(roots.GoSX, "editor", "go.sum")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(roots.GoSX, "go.sum"), path); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "context")
		if _, _, err := SynthesizeBuildContext(context.Background(), assets, destination, roots); err == nil {
			t.Fatal("symlinked module manifest accepted")
		}
	})
}

func TestCollectModuleLock_RequiresExactTrackedHEADBytes(t *testing.T) {
	t.Run("required manifest absent from HEAD", func(t *testing.T) {
		roots := newModuleSourceFixtures(t)
		path := filepath.Join(roots.GSXMail, "go.mod")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, roots.GSXMail, "add", "-u", "--", "go.mod")
		runFixtureGit(t, roots.GSXMail, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "remove manifest")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := CollectModuleLock(context.Background(), roots); err == nil {
			t.Fatal("untracked replacement for absent HEAD manifest accepted")
		}
	})
	t.Run("assume unchanged drift", func(t *testing.T) {
		roots := newModuleSourceFixtures(t)
		runFixtureGit(t, roots.GSXMail, "update-index", "--assume-unchanged", "go.mod")
		if err := os.WriteFile(filepath.Join(roots.GSXMail, "go.mod"), []byte("module example.com/drift\n\ngo 1.26.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := CollectModuleLock(context.Background(), roots); err == nil {
			t.Fatal("assume-unchanged manifest drift accepted")
		}
	})
	t.Run("local fsmonitor is never invoked", func(t *testing.T) {
		roots := newModuleSourceFixtures(t)
		sentinel := filepath.Join(t.TempDir(), "fsmonitor-ran")
		script := filepath.Join(t.TempDir(), "fsmonitor")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf invoked > \""+sentinel+"\"\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, roots.GSXMail, "config", "core.fsmonitor", script)
		if _, err := CollectModuleLock(context.Background(), roots); err != nil {
			t.Fatalf("isolated repository inspection failed: %v", err)
		}
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("untrusted fsmonitor was invoked: %v", err)
		}
	})
}

func testAssetsRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func newModuleSourceFixtures(t *testing.T) SourceRoots {
	t.Helper()
	parent := t.TempDir()
	create := func(name string, files map[string]string) string {
		root := filepath.Join(parent, name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		files["LICENSE"] = "MIT License\n"
		files["source.go"] = "package source\n"
		files["go.work"] = "go 1.26.0\n"
		for path, content := range files {
			absolute := filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		runFixtureGit(t, root, "init", "-q")
		runFixtureGit(t, root, "add", "--", ".")
		runFixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
		return root
	}
	goMod := "module example.com/root\n\ngo 1.26.0\n"
	return SourceRoots{
		GSXMail: create("gsxmail", map[string]string{"go.mod": goMod, "go.sum": ""}),
		GoSX: create("gosx", map[string]string{
			"go.mod": goMod, "go.sum": "",
			"editor/go.mod": "module example.com/editor\n\ngo 1.26.0\nreplace example.com/root => ..\n", "editor/go.sum": "",
			"cmd/buildbootstrap/go.mod": "module example.com/bootstrap\n\ngo 1.26.0\n", "cmd/buildbootstrap/go.sum": "",
		}),
		TQWebP: create("tqwebp", map[string]string{
			"go.mod": goMod, "go.sum": "",
			"bench/deepteams/go.mod": "module example.com/deepteams\n\ngo 1.26.0\nreplace example.com/root => ../..\n", "bench/deepteams/go.sum": "",
		}),
	}
}

func runFixtureGit(t *testing.T, root string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command(trustedGitBinary, commandArgs...)
	cmd.Env = []string{"HOME=/nonexistent", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fixture: %v (%s)", err, output)
	}
}

func equalModuleLocks(left, right ModuleLock) bool {
	leftJSON, _ := canonicalJSON(left)
	rightJSON, _ := canonicalJSON(right)
	return string(leftJSON) == string(rightJSON)
}
