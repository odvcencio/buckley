package workspaceevidence

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
