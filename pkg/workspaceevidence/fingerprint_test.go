package workspaceevidence

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitStateFingerprint_DetectsTrackedAndUntrackedContentChanges(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, "tracked.txt", "base\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	clean := fingerprintForTest(t, root)
	if again := fingerprintForTest(t, root); again != clean {
		t.Fatalf("stable workspace fingerprints differ: %s != %s", clean, again)
	}

	writeFingerprintTestFile(t, root, "tracked.txt", "first dirty value\n")
	firstDirty := fingerprintForTest(t, root)
	if firstDirty == clean {
		t.Fatal("tracked edit did not change fingerprint")
	}
	writeFingerprintTestFile(t, root, "tracked.txt", "second dirty value\n")
	secondDirty := fingerprintForTest(t, root)
	if secondDirty == firstDirty {
		t.Fatal("further edit to an already-dirty file did not change fingerprint")
	}

	writeFingerprintTestFile(t, root, "new.txt", "one\n")
	untracked := fingerprintForTest(t, root)
	writeFingerprintTestFile(t, root, "new.txt", "two\n")
	if changed := fingerprintForTest(t, root); changed == untracked {
		t.Fatal("untracked content edit did not change fingerprint")
	}
}

func TestGitStateFingerprint_TracksPathsAndIgnoresBuildArtifacts(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, ".gitignore", "*.test\ncoverage.out\n")
	writeFingerprintTestFile(t, root, "tracked.txt", "base\n")
	runGitForFingerprintTest(t, root, "add", ".gitignore", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	clean := fingerprintForTest(t, root)
	writeFingerprintTestFile(t, root, "buckley.test", "generated binary\n")
	writeFingerprintTestFile(t, root, "coverage.out", "generated coverage\n")
	if got := fingerprintForTest(t, root); got != clean {
		t.Fatalf("ignored build artifacts changed fingerprint: got %s want %s", got, clean)
	}

	if err := os.Rename(filepath.Join(root, "tracked.txt"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatalf("rename tracked file: %v", err)
	}
	renamed := fingerprintForTest(t, root)
	if renamed == clean {
		t.Fatal("tracked rename did not change fingerprint")
	}
	if err := os.Rename(filepath.Join(root, "renamed.txt"), filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatalf("restore tracked file: %v", err)
	}
	if restored := fingerprintForTest(t, root); restored != clean {
		t.Fatalf("restored tracked path changed fingerprint: got %s want %s", restored, clean)
	}
	if err := os.Remove(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatalf("delete tracked file: %v", err)
	}
	if deleted := fingerprintForTest(t, root); deleted == clean {
		t.Fatal("tracked deletion did not change fingerprint")
	}
}

func TestGitStateFingerprint_HandlesLargeUntrackedSparseFile(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, ".gitkeep", "")
	runGitForFingerprintTest(t, root, "add", ".gitkeep")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	path := filepath.Join(root, "large.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create sparse file: %v", err)
	}
	if err := file.Truncate(520 << 20); err != nil {
		_ = file.Close()
		t.Fatalf("truncate sparse file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse file: %v", err)
	}

	first := fingerprintForTest(t, root)
	file, err = os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open sparse file: %v", err)
	}
	if _, err := file.WriteAt([]byte{0x42}, 260<<20); err != nil {
		_ = file.Close()
		t.Fatalf("write sparse file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse file after write: %v", err)
	}
	if second := fingerprintForTest(t, root); second == first {
		t.Fatal("large sparse untracked content edit did not change fingerprint")
	}
}

func TestGitStateFingerprint_DetectsSameSizeMiddleRewriteWithRestoredMtime(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	body := strings.Repeat("a", 256<<10)
	writeFingerprintTestFile(t, root, "tracked.txt", body)
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	path := filepath.Join(root, "tracked.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat tracked file: %v", err)
	}
	firstBody := []byte(body)
	firstBody[len(firstBody)/2] = 'b'
	if err := os.WriteFile(path, firstBody, 0o644); err != nil {
		t.Fatalf("write first dirty body: %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore first mtime: %v", err)
	}
	first := fingerprintForTest(t, root)

	secondBody := []byte(body)
	secondBody[len(secondBody)/2] = 'c'
	if err := os.WriteFile(path, secondBody, 0o644); err != nil {
		t.Fatalf("write second dirty body: %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore second mtime: %v", err)
	}
	if second := fingerprintForTest(t, root); second == first {
		t.Fatal("same-size middle rewrite with restored mtime did not change fingerprint")
	}
}

func TestGitStateFingerprint_NormalizesSubdirectoryWorkdir(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	writeFingerprintTestFile(t, root, "pkg/tracked.txt", "base\n")
	runGitForFingerprintTest(t, root, "add", "pkg/tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	subdir := filepath.Join(root, "pkg")
	base := fingerprintForTest(t, subdir)
	writeFingerprintTestFile(t, root, "pkg/tracked.txt", "dirty\n")
	if dirty := fingerprintForTest(t, subdir); dirty == base {
		t.Fatal("subdirectory workdir did not observe repo-root-relative tracked change")
	}
}

func TestGitStateFingerprint_HeadMovementChangesFingerprint(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, "tracked.txt", "one\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "one")
	first := fingerprintForTest(t, root)

	writeFingerprintTestFile(t, root, "tracked.txt", "two\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "two")
	if second := fingerprintForTest(t, root); second == first {
		t.Fatal("HEAD movement did not change fingerprint")
	}
}

func TestGitStateFingerprint_IndexOnlyTransitionChangesFingerprint(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, "tracked.txt", "base\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")

	writeFingerprintTestFile(t, root, "tracked.txt", "dirty\n")
	unstaged := fingerprintForTest(t, root)
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	if staged := fingerprintForTest(t, root); staged == unstaged {
		t.Fatal("index-only transition did not change fingerprint")
	}
}

func TestGitStateFingerprint_UnbornRepositoryTracksIndexWorktreeAndFirstCommit(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, "tracked.txt", "staged\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")

	staged := fingerprintForTest(t, root)
	if again := fingerprintForTest(t, root); again != staged {
		t.Fatalf("stable unborn fingerprints differ: %s != %s", staged, again)
	}

	writeFingerprintTestFile(t, root, "tracked.txt", "worktree edit\n")
	dirty := fingerprintForTest(t, root)
	if dirty == staged {
		t.Fatal("unborn worktree edit did not change fingerprint")
	}

	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	runGitForFingerprintTest(t, root, "commit", "-qm", "first")
	committed := fingerprintForTest(t, root)
	if committed == dirty || committed == staged {
		t.Fatal("first commit did not change the unborn workspace fingerprint")
	}
}

func TestGitStateFingerprint_UnbornBranchIdentityChangesFingerprint(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	writeFingerprintTestFile(t, root, "tracked.txt", "staged\n")
	runGitForFingerprintTest(t, root, "add", "tracked.txt")
	first := fingerprintForTest(t, root)

	runGitForFingerprintTest(t, root, "symbolic-ref", "HEAD", "refs/heads/alternate")
	if second := fingerprintForTest(t, root); second == first {
		t.Fatal("unborn symbolic branch ref did not change fingerprint")
	}
}

func TestGitStateFingerprint_CorruptSymbolicBranchFailsClosed(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	refRaw, err := exec.Command("git", "-C", root, "symbolic-ref", "--no-recurse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve unborn branch: %v", err)
	}
	refPath := filepath.Join(root, ".git", filepath.FromSlash(strings.TrimSpace(string(refRaw))))
	if err := os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
		t.Fatalf("create corrupt ref directory: %v", err)
	}
	if err := os.WriteFile(refPath, []byte("not-an-object-id\n"), 0o644); err != nil {
		t.Fatalf("write corrupt branch ref: %v", err)
	}

	if _, err := GitStateFingerprint(context.Background(), root); err == nil || !strings.Contains(err.Error(), "observe HEAD state") {
		t.Fatalf("GitStateFingerprint error = %v, want corrupt symbolic branch failure", err)
	}
}

func TestGitStateFingerprint_CorruptHEADFailsClosed(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("not-a-ref\n"), 0o644); err != nil {
		t.Fatalf("write corrupt HEAD: %v", err)
	}

	if _, err := GitStateFingerprint(context.Background(), root); err == nil {
		t.Fatal("GitStateFingerprint succeeded with corrupt HEAD")
	}
}

func TestGitStateFingerprint_FailsClosedWhenManifestChangesDuringObservation(t *testing.T) {
	root := t.TempDir()
	oldGitOutput := gitOutput
	t.Cleanup(func() { gitOutput = oldGitOutput })
	nameOnlyCalls := 0
	gitOutput = func(_ context.Context, _ string, _ int64, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "rev-parse --show-toplevel":
			return []byte(root + "\n"), nil
		case joined == "rev-parse --verify HEAD":
			return []byte("abc123\n"), nil
		case strings.HasPrefix(joined, "diff --cached --raw"):
			return nil, nil
		case strings.HasPrefix(joined, "diff --name-only"):
			nameOnlyCalls++
			if nameOnlyCalls == 1 {
				return nil, nil
			}
			return []byte("tracked.txt\x00"), nil
		case strings.HasPrefix(joined, "ls-files --others"):
			return nil, nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return nil, nil
		}
	}

	_, err := GitStateFingerprint(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "workspace git manifest changed during observation") {
		t.Fatalf("GitStateFingerprint error = %v, want manifest changed failure", err)
	}
}

func TestGitStateFingerprint_FailsClosedWhenContentChangesWithSameManifest(t *testing.T) {
	root := t.TempDir()
	writeFingerprintTestFile(t, root, "tracked.txt", "first\n")
	oldGitOutput := gitOutput
	t.Cleanup(func() { gitOutput = oldGitOutput })
	nameOnlyCalls := 0
	gitOutput = func(_ context.Context, _ string, _ int64, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "rev-parse --show-toplevel":
			return []byte(root + "\n"), nil
		case joined == "rev-parse --verify HEAD":
			return []byte("abc123\n"), nil
		case strings.HasPrefix(joined, "diff --cached --raw"):
			return nil, nil
		case strings.HasPrefix(joined, "diff --name-only"):
			nameOnlyCalls++
			if nameOnlyCalls == 2 {
				writeFingerprintTestFile(t, root, "tracked.txt", "second\n")
			}
			return []byte("tracked.txt\x00"), nil
		case strings.HasPrefix(joined, "ls-files --others"):
			return nil, nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return nil, nil
		}
	}

	_, err := GitStateFingerprint(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "workspace state changed during observation") {
		t.Fatalf("GitStateFingerprint error = %v, want content changed failure", err)
	}
}

func TestGitStateFingerprint_HonorsCancellation(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GitStateFingerprint(ctx, root)
	if err == nil {
		t.Fatal("GitStateFingerprint succeeded with canceled context")
	}
}

func TestGitStateFingerprint_ExternalTiming(t *testing.T) {
	root := os.Getenv("BUCKLEY_FINGERPRINT_TIMING_ROOT")
	if root == "" {
		t.Skip("set BUCKLEY_FINGERPRINT_TIMING_ROOT to time an external workspace")
	}
	for i := 1; i <= 2; i++ {
		start := time.Now()
		if _, err := GitStateFingerprint(context.Background(), root); err != nil {
			t.Fatalf("fingerprint pass %d: %v", i, err)
		}
		t.Logf("fingerprint pass %d elapsed %s", i, time.Since(start).Round(time.Millisecond))
	}
}

func fingerprintForTest(t *testing.T, root string) string {
	t.Helper()
	digest, err := GitStateFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeFingerprintTestFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitForFingerprintTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
