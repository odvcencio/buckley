package workspaceevidence

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStateFingerprintWithFallback_LargeRepository(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q")
	for i := 0; i < 1200; i++ {
		writeFingerprintTestFile(t, root, fmt.Sprintf("file-%04d.txt", i), "base\n")
	}
	runGitForFingerprintTest(t, root, "add", ".")
	runGitForFingerprintTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	if timeout := fingerprintTimeout(root); timeout <= defaultFingerprintTimeout {
		t.Fatalf("timeout did not scale: %v", timeout)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := fmt.Sprintf("#!/bin/sh\nif [ \"$3\" = diff ]; then echo 'injected diff timeout' >&2; exit 124; fi\nexec %q \"$@\"\n", git)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || !strings.Contains(first.Warning, "used git status") || first.Digest == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || second.Digest != first.Digest {
		t.Fatalf("unstable fallback: %+v %v", second, err)
	}
	writeFingerprintTestFile(t, root, "file-0600.txt", "edit\n")
	dirty, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || dirty.Digest == first.Digest {
		t.Fatalf("edit not seen: %+v %v", dirty, err)
	}
	writeFingerprintTestFile(t, root, "file-0600.txt", "more\n")
	again, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || again.Digest == dirty.Digest {
		t.Fatalf("same-size edit not seen: %+v %v", again, err)
	}
	runGitForFingerprintTest(t, root, "add", "file-0600.txt")
	staged, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || staged.Digest == again.Digest {
		t.Fatalf("index change not seen: %+v %v", staged, err)
	}
	writeFingerprintTestFile(t, root, "new file.txt", "new\n")
	untracked, err := GitStateFingerprintWithFallback(context.Background(), root)
	if err != nil || untracked.Digest == staged.Digest {
		t.Fatalf("untracked not seen: %+v %v", untracked, err)
	}
}
