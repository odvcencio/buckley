package worktree

// Secure exact-workspace tests are isolated from the accepted legacy manager.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateAtUsesExactCommitAndIsolatesDirtyPrimary(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# committed second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "second")
	primaryHead := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if baseCommit == primaryHead {
		t.Fatal("test repository did not advance")
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# staged dirty primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	if err := os.WriteFile(filepath.Join(repo, "UNTRACKED_SECRET"), []byte("primary only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryStatus := gitOutputForTest(t, repo, "status", "--porcelain=v1", "--untracked-files=all")

	hookMarker := filepath.Join(t.TempDir(), "post-checkout-ran")
	hook := "#!/bin/sh\nprintf ran > \"" + hookMarker + "\"\n"
	hookPath := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "runs/exact-base", baseCommit)
	if err != nil {
		t.Fatalf("secure CreateAt returned error: %v", err)
	}

	if wt.Commit != baseCommit || gitOutputForTest(t, wt.path, "rev-parse", "--verify", "HEAD^{commit}") != baseCommit {
		t.Fatalf("created commit = %q, identity = %+v, want %q", gitOutputForTest(t, wt.path, "rev-parse", "HEAD"), wt, baseCommit)
	}
	contents, err := os.ReadFile(filepath.Join(wt.path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "# test" {
		t.Fatalf("worktree README = %q, want exact base-commit content", contents)
	}
	if _, err := os.Lstat(filepath.Join(wt.path, "UNTRACKED_SECRET")); !os.IsNotExist(err) {
		t.Fatalf("untracked primary file leaked into worktree: %v", err)
	}
	if _, err := os.Lstat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("repository checkout hook executed: %v", err)
	}
	primaryContents, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil || string(primaryContents) != "# staged dirty primary\n" {
		t.Fatalf("primary worktree changed: contents=%q err=%v", primaryContents, err)
	}
	if gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}") != primaryHead {
		t.Fatal("primary HEAD changed")
	}
	if got := gitOutputForTest(t, repo, "status", "--porcelain=v1", "--untracked-files=all"); got != primaryStatus {
		t.Fatalf("primary status changed:\n%s\nwant:\n%s", got, primaryStatus)
	}

	canonicalPath, err := filepath.EvalSymlinks(wt.path)
	if err != nil || canonicalPath != wt.path || !filepath.IsAbs(wt.path) {
		t.Fatalf("worktree path is not canonical: path=%q canonical=%q err=%v", wt.path, canonicalPath, err)
	}
	if wt.repositoryRoot != repo || !filepath.IsAbs(wt.gitDir) || wt.gitDir != wt.commonGitDir {
		t.Fatalf("isolated git identities are not canonical: %+v", wt)
	}
	if remotes := gitOutputForTest(t, wt.path, "remote"); remotes != "" {
		t.Fatalf("isolated checkout retained an implicit push target: %q", remotes)
	}
	wantCommon := gitOutputForTest(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	wantCommon, err = filepath.EvalSymlinks(wantCommon)
	if err != nil || wt.sourceCommonGitDir != wantCommon || wt.commonGitDir == wantCommon {
		t.Fatalf("source common git dir = %q, isolated common git dir = %q, want source %q (err=%v)", wt.sourceCommonGitDir, wt.commonGitDir, wantCommon, err)
	}
	if gitSucceeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/runs/exact-base") {
		t.Fatal("secure checkout created a branch in the source repository")
	}
	path := wt.path
	if err := secure.Remove(context.Background(), wt); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("secure checkout still exists after removal: %v", err)
	}
}

func TestSecureWorktreeJSONRoundTripDoesNotLeakPathsOrAuthority(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	checkout, err := secure.CreateAt(context.Background(), "json/opaque", commit)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"branch":"json/opaque","commit":%q}`, commit)
	if string(payload) != want {
		t.Fatalf("SecureWorktree JSON = %s, want %s", payload, want)
	}
	for description, secret := range map[string]string{
		"checkout path":        checkout.path,
		"source root":          checkout.repositoryRoot,
		"git directory":        checkout.gitDir,
		"common git directory": checkout.commonGitDir,
		"source git directory": checkout.sourceGitDir,
		"source common dir":    checkout.sourceCommonGitDir,
		"owner authority":      hex.EncodeToString(checkout.secureOwner[:]),
		"checkout authority":   hex.EncodeToString(checkout.secureID[:]),
	} {
		if secret != "" && strings.Contains(string(payload), secret) {
			t.Fatalf("SecureWorktree JSON leaked %s", description)
		}
	}

	var decoded SecureWorktree
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Branch != checkout.Branch || decoded.Commit != checkout.Commit || decoded.Path() != "" {
		t.Fatalf("decoded SecureWorktree unexpectedly retained private state: %+v", decoded)
	}
	if err := secure.Remove(context.Background(), &decoded); err == nil {
		t.Fatal("JSON round trip recreated cleanup authority")
	}
	if _, err := os.Stat(checkout.path); err != nil {
		t.Fatalf("rejected decoded handle changed checkout: %v", err)
	}
	if err := secure.Remove(context.Background(), checkout); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAtRejectsInvalidBranchNamesWithoutCreatingRefs(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	tests := []string{
		"",
		" feature",
		"feature ",
		"-force",
		"../escape",
		"feature..bad",
		"feature lock",
		"@{-1}",
		"HEAD",
		strings.Repeat("a", maxBranchNameBytes+1),
	}
	for _, branch := range tests {
		t.Run(strings.ReplaceAll(branch, "/", "_"), func(t *testing.T) {
			if _, err := secure.CreateAt(context.Background(), branch, commit); err == nil {
				t.Fatalf("CreateAt accepted invalid branch %q", branch)
			}
			if branch != "" && gitSucceeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch) {
				t.Fatalf("invalid branch ref was created: %q", branch)
			}
		})
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestCreateAtRejectsNonExactOrNonCommitObjects(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	blob := gitOutputForTest(t, repo, "rev-parse", "HEAD:README.md")
	tree := gitOutputForTest(t, repo, "rev-parse", "HEAD^{tree}")
	secure := newSecureManagerForTest(t, repo)
	tests := []struct {
		name string
		oid  string
	}{
		{name: "ref", oid: "HEAD"},
		{name: "abbreviation", oid: commit[:12]},
		{name: "uppercase", oid: strings.ToUpper(commit)},
		{name: "whitespace", oid: commit + "\n"},
		{name: "option", oid: "--help"},
		{name: "unknown", oid: strings.Repeat("0", len(commit))},
		{name: "blob", oid: blob},
		{name: "tree", oid: tree},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch := "invalid-object/" + string(rune('a'+i))
			if _, err := secure.CreateAt(context.Background(), branch, tt.oid); err == nil {
				t.Fatalf("CreateAt accepted %s object %q", tt.name, tt.oid)
			}
			if gitSucceeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch) {
				t.Fatalf("branch created for rejected %s object", tt.name)
			}
		})
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestCreateAtIgnoresGitReplacementObjects(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "replacement")
	replacementCommit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	runGit(t, repo, "replace", baseCommit, replacementCommit)
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")

	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "replacement/exact", baseCommit)
	if err != nil {
		t.Fatalf("CreateAt returned error: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(wt.path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "# test" {
		t.Fatalf("replacement object changed checkout: got %q", contents)
	}
}

func TestNewSecureManagerRequiresFreshDisjointRunRoot(t *testing.T) {
	repo := initGitRepo(t)
	insidePrimary := filepath.Join(repo, ".buckley", "secure-run")
	if _, err := NewSecureManager(context.Background(), repo, insidePrimary); err == nil {
		t.Fatal("NewSecureManager accepted a run root inside the primary worktree")
	}
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSecureManager(context.Background(), repo, existing); err == nil {
		t.Fatal("NewSecureManager accepted a pre-existing run root")
	}
}

func TestCreateAtRejectsSymlinkEscapeBelowWorktreeRoot(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	if err := os.Remove(secure.checkoutsRoot); err != nil {
		t.Fatal(err)
	}
	escape := t.TempDir()
	if err := os.Symlink(escape, secure.checkoutsRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := secure.CreateAt(context.Background(), "escape/test", commit); err == nil {
		t.Fatal("CreateAt followed a symlink outside the worktree root")
	}
	assertDirectoryEmpty(t, escape)
}

func TestCreateAtRejectsModifiedTemplateBeforeCommitResolution(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	marker := filepath.Join(secure.templateRoot, "unexpected")
	if err := os.WriteFile(marker, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := secure.CreateAt(context.Background(), "template/tampered", commit); err == nil {
		t.Fatal("CreateAt accepted a non-empty pinned template")
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "tampered" {
		t.Fatalf("template evidence changed: contents=%q err=%v", contents, err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestCreateAtRejectsReplacedSourceObjectDirectory(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	moved := secure.sourceObjectsDir + "-moved"
	if err := os.Rename(secure.sourceObjectsDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secure.sourceObjectsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(secure.sourceObjectsDir, "preserve")
	if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := secure.CreateAt(context.Background(), "source-objects/replaced", commit); err == nil {
		t.Fatal("CreateAt accepted a replaced source object directory")
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "replacement" {
		t.Fatalf("replacement source object directory changed: contents=%q err=%v", contents, err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestSecureManagerScrubsGitRepositorySelectors(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	other := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# redirected"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "README.md")
	runGit(t, other, "commit", "-m", "redirected")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(other, ".git", "objects"))

	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "selectors/exact", commit)
	if err != nil {
		t.Fatalf("CreateAt returned error with hostile selectors: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(wt.path, "README.md"))
	if err != nil || string(contents) != "# test" {
		t.Fatalf("selector redirection changed checkout: contents=%q err=%v", contents, err)
	}
	if wt.repositoryRoot != repo {
		t.Fatalf("repository root = %q, want %q", wt.repositoryRoot, repo)
	}
}

func TestSecureManagerDoesNotExecuteSourceCheckoutFilters(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("payload.txt filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "payload.txt"), []byte("raw payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitattributes", "payload.txt")
	runGit(t, repo, "commit", "-m", "add hostile filter attribute")
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	runGit(t, repo, "config", "filter.hostile.smudge", "false")
	runGit(t, repo, "config", "filter.hostile.process", "false")
	runGit(t, repo, "config", "filter.hostile.required", "true")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "filter.hostile.smudge")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_CONFIG_KEY_1", "filter.hostile.process")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")

	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "filters/exact", commit)
	if err != nil {
		t.Fatalf("CreateAt executed or inherited hostile filter config: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(wt.path, "payload.txt"))
	if err != nil || string(contents) != "raw payload\n" {
		t.Fatalf("filtered checkout contents=%q err=%v", contents, err)
	}
	if gitSucceeds(wt.path, "config", "--local", "--get-regexp", `^filter\.`) {
		t.Fatal("isolated checkout copied source filter configuration")
	}
}

func TestSecureManagerRejectsPromisorSourceWithoutInvokingHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	helperDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(helperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(marker)+"'\nexit 1\n")
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	secure := newSecureManagerForTest(t, repo)
	if !containsEnvironment(secure.environment, "GIT_NO_LAZY_FETCH=1") {
		t.Fatal("secure Git environment does not disable lazy fetch")
	}

	runGit(t, repo, "config", "core.repositoryformatversion", "1")
	runGit(t, repo, "config", "extensions.partialClone", "origin")
	runGit(t, repo, "config", "remote.origin.promisor", "true")
	runGit(t, repo, "config", "remote.origin.url", "hostile::missing")
	objectPath := filepath.Join(repo, ".git", "objects", commit[:2], commit[2:])
	if err := os.Rename(objectPath, filepath.Join(t.TempDir(), "missing-commit")); err != nil {
		t.Fatalf("make promised commit object unavailable: %v", err)
	}

	if _, err := secure.CreateAt(context.Background(), "promisor/source", commit); err == nil {
		t.Fatal("CreateAt accepted a promisor source with a missing object")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("promisor helper was invoked: %v", err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	assertDirectoryEmpty(t, secure.quarantineRoot)
}

func TestNewSecureManagerRejectsIncludedHelperAndPromisorConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	helperDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(helperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked >> '"+shellSingleQuote(marker)+"'\nexit 1\n")
	includePath := filepath.Join(t.TempDir(), "hostile.config")
	includeConfig := "[remote \"hostile\"]\n\turl = hostile::missing\n\tpromisor = true\n\tvcs = hostile\n[credential]\n\thelper = !false\n"
	if err := os.WriteFile(includePath, []byte(includeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "include.path", includePath)
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runRoot := filepath.Join(t.TempDir(), "secure-run")
	manager, err := NewSecureManager(context.Background(), repo, runRoot)
	if err == nil || manager != nil || !strings.Contains(strings.ToLower(err.Error()), "include.path") {
		t.Fatalf("NewSecureManager with included posture = (%+v, %v)", manager, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("included remote helper was invoked: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected included posture created run root: %v", err)
	}
}

func TestNewSecureManagerRejectsConditionalIncludeConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	helperDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(helperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked >> '"+shellSingleQuote(marker)+"'\nexit 1\n")
	includePath := filepath.Join(t.TempDir(), "conditional.config")
	includeConfig := "[remote \"hostile\"]\n\turl = hostile::missing\n\tpromisor = true\n\tvcs = hostile\n"
	if err := os.WriteFile(includePath, []byte(includeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	gitDirPattern := filepath.ToSlash(filepath.Join(repo, ".git")) + "/"
	runGit(t, repo, "config", "includeIf.gitdir:"+gitDirPattern+".path", includePath)
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runRoot := filepath.Join(t.TempDir(), "secure-run")
	manager, err := NewSecureManager(context.Background(), repo, runRoot)
	if err == nil || manager != nil || !strings.Contains(strings.ToLower(err.Error()), "includeif.") {
		t.Fatalf("NewSecureManager with conditional include = (%+v, %v)", manager, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("conditional include remote helper was invoked: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected conditional include created run root: %v", err)
	}
}

func TestNewSecureManagerRejectsWorktreeScopedHelperAndPromisorConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	helperDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(helperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked >> '"+shellSingleQuote(marker)+"'\nexit 1\n")
	runGit(t, repo, "config", "core.repositoryformatversion", "1")
	runGit(t, repo, "config", "extensions.worktreeConfig", "true")
	runGit(t, repo, "config", "--worktree", "remote.hostile.url", "hostile::missing")
	runGit(t, repo, "config", "--worktree", "remote.hostile.promisor", "true")
	runGit(t, repo, "config", "--worktree", "remote.hostile.vcs", "hostile")
	runGit(t, repo, "config", "--worktree", "credential.helper", "!false")
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runRoot := filepath.Join(t.TempDir(), "secure-run")
	manager, err := NewSecureManager(context.Background(), repo, runRoot)
	if err == nil || manager != nil || !strings.Contains(strings.ToLower(err.Error()), "extensions.worktreeconfig") {
		t.Fatalf("NewSecureManager with worktree config = (%+v, %v)", manager, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("worktree-scoped remote helper was invoked: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected worktree posture created run root: %v", err)
	}
}

func TestSecureManagerRejectsPromisorPackMarker(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	marker := filepath.Join(secure.sourceObjectsDir, "pack", "pack-untrusted.promisor")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("promisor"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := secure.CreateAt(context.Background(), "promisor/marker", commit); err == nil {
		t.Fatal("CreateAt accepted a promisor pack marker")
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestSecureManagerRejectsSourceObjectAlternates(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	alternates := filepath.Join(secure.sourceObjectsDir, "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alternates, []byte(t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := secure.CreateAt(context.Background(), "alternates/source", commit); err == nil {
		t.Fatal("CreateAt accepted a source object alternate")
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestSecureManagerRejectsPromisorTargetWithoutInvokingHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	helperDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(helperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(marker)+"'\nexit 1\n")
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			target := args[len(args)-1]
			if _, configErr := realRunner(ctx, target, "config", "extensions.partialClone", "origin"); configErr != nil {
				return nil, configErr
			}
			if _, configErr := realRunner(ctx, target, "config", "remote.origin.promisor", "true"); configErr != nil {
				return nil, configErr
			}
			if _, configErr := realRunner(ctx, target, "config", "remote.origin.url", "hostile::missing"); configErr != nil {
				return nil, configErr
			}
		}
		return output, err
	}

	if _, err := secure.CreateAt(context.Background(), "promisor/target", commit); err == nil {
		t.Fatal("CreateAt accepted a promisor isolated target")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("promisor helper was invoked: %v", err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	assertOneResidualForTest(t, secure)
	if err := secure.Close(); err != nil {
		t.Fatalf("Close did not clean residual checkout evidence: %v", err)
	}
}

func TestSecureManagerRejectsTargetFilterBeforeCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	marker := filepath.Join(t.TempDir(), "filter-invoked")
	helper := filepath.Join(t.TempDir(), "hostile-filter")
	writeShellExecutable(t, helper, "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(marker)+"'\ncat\n")
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			target := args[len(args)-1]
			if _, configErr := realRunner(ctx, target, "config", "filter.hostile.smudge", helper); configErr != nil {
				return nil, configErr
			}
			if _, configErr := realRunner(ctx, target, "config", "filter.hostile.required", "true"); configErr != nil {
				return nil, configErr
			}
		}
		return output, err
	}

	if _, err := secure.CreateAt(context.Background(), "filters/target", commit); err == nil {
		t.Fatal("CreateAt accepted an executable target filter")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("target filter was invoked: %v", err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestSecureCloneInterceptProvesStandaloneNoCheckoutAndRejectsHostileFilter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell filter fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("payload.txt filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "payload.txt"), []byte("unfiltered payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitattributes", "payload.txt")
	runGit(t, repo, "commit", "-m", "add filter-bearing payload")
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	blob := gitOutputForTest(t, repo, "rev-parse", "HEAD:payload.txt")

	marker := filepath.Join(t.TempDir(), "filter-invoked")
	helper := filepath.Join(t.TempDir(), "hostile-filter")
	writeShellExecutable(t, helper, "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(marker)+"'\ncat\n")
	secure := newSecureManagerForTest(t, repo)
	sourceObject := filepath.Join(secure.sourceObjectsDir, blob[:2], blob[2:])
	sourceObjectInfo, err := os.Stat(sourceObject)
	if err != nil {
		t.Fatalf("known source blob is not loose: %v", err)
	}

	realRunner := secure.runGit
	var cloneArgs []string
	var targetWasEmpty, looseObjectCopied bool
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, runErr := realRunner(ctx, dir, args...)
		if runErr != nil || len(args) == 0 || args[0] != "clone" {
			return output, runErr
		}
		cloneArgs = append([]string(nil), args...)
		target := args[len(args)-1]
		entries, readErr := os.ReadDir(target)
		if readErr != nil {
			return nil, fmt.Errorf("inspect post-clone target: %w", readErr)
		}
		targetWasEmpty = len(entries) == 1 && entries[0].Name() == ".git"
		targetObject := filepath.Join(target, ".git", "objects", blob[:2], blob[2:])
		targetObjectInfo, statErr := os.Stat(targetObject)
		if statErr != nil {
			return nil, fmt.Errorf("inspect copied loose object: %w", statErr)
		}
		looseObjectCopied = !os.SameFile(sourceObjectInfo, targetObjectInfo)
		if _, configErr := realRunner(ctx, target, "config", "filter.hostile.smudge", helper); configErr != nil {
			return nil, configErr
		}
		if _, configErr := realRunner(ctx, target, "config", "filter.hostile.required", "true"); configErr != nil {
			return nil, configErr
		}
		return output, nil
	}

	checkout, err := secure.CreateAt(context.Background(), "clone/intercept", commit)
	if err == nil || checkout != nil || !strings.Contains(strings.ToLower(err.Error()), "filter.hostile") {
		t.Fatalf("CreateAt with intercepted target filter = (%+v, %v)", checkout, err)
	}
	for _, required := range []string{"--no-hardlinks", "--no-checkout"} {
		if !containsArgument(cloneArgs, required) {
			t.Fatalf("clone argv %q omitted %s", cloneArgs, required)
		}
	}
	if !targetWasEmpty {
		t.Fatal("post-clone target contained a working tree before validation")
	}
	if !looseObjectCopied {
		t.Fatal("--no-hardlinks clone reused the known loose source object inode")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("hostile target filter was invoked: %v", err)
	}
	assertOneResidualForTest(t, secure)
	if err := secure.Close(); err != nil {
		t.Fatalf("Close after rejected clone: %v", err)
	}
}

func TestPromisorPostureRejectsLocallyAvailableCommitBeforeObjectResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording shell Git fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	commitObject := filepath.Join(repo, ".git", "objects", commit[:2], commit[2:])
	if _, err := os.Stat(commitObject); err != nil {
		t.Fatalf("exact commit must remain locally available: %v", err)
	}
	runGit(t, repo, "config", "core.repositoryformatversion", "1")
	runGit(t, repo, "config", "extensions.partialClone", "origin")
	runGit(t, repo, "config", "remote.origin.promisor", "true")
	runGit(t, repo, "config", "remote.origin.url", "hostile::missing")

	wrapperDir, logPath := installRecordingGitForTest(t)
	helperMarker := filepath.Join(t.TempDir(), "helper-invoked")
	writeShellExecutable(t, filepath.Join(wrapperDir, "git-remote-hostile"), "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(helperMarker)+"'\nexit 1\n")
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, selector := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(selector, filepath.Join(t.TempDir(), "hostile-selector"))
	}
	runRoot := filepath.Join(t.TempDir(), "secure-run")
	manager, err := NewSecureManager(context.Background(), repo, runRoot)
	if err == nil || manager != nil || !strings.Contains(strings.ToLower(err.Error()), "extensions.partialclone") {
		t.Fatalf("NewSecureManager with local promisor posture = (%+v, %v)", manager, err)
	}
	invocations := readInvocationLogForTest(t, logPath)
	if len(invocations) == 0 {
		t.Fatal("recording Git wrapper captured no admission commands")
	}
	for _, invocation := range invocations {
		if !strings.Contains(invocation, "lazy=1;git_dir=unset;work_tree=unset;common=unset;object=unset;alternates=unset;") {
			t.Fatalf("secure Git invocation retained lazy-fetch or selector posture: %q", invocation)
		}
		if strings.Contains(invocation, "[cat-file]") {
			t.Fatalf("promisor posture reached object resolution: %q", invocation)
		}
	}
	if _, err := os.Lstat(helperMarker); !os.IsNotExist(err) {
		t.Fatalf("promisor helper was invoked: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected promisor posture created run root: %v", err)
	}
}

func TestPostCloneObjectAlternateIsRejectedBeforeCheckoutOrResolution(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	postClone := false
	var commandsAfterClone []string
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if postClone && len(args) != 0 {
			commandsAfterClone = append(commandsAfterClone, args[0])
		}
		output, runErr := realRunner(ctx, dir, args...)
		if runErr == nil && len(args) != 0 && args[0] == "clone" {
			target := args[len(args)-1]
			alternates := filepath.Join(target, ".git", "objects", "info", "alternates")
			if mkdirErr := os.MkdirAll(filepath.Dir(alternates), 0o700); mkdirErr != nil {
				return nil, mkdirErr
			}
			if writeErr := os.WriteFile(alternates, []byte(secure.sourceObjectsDir+"\n"), 0o600); writeErr != nil {
				return nil, writeErr
			}
			postClone = true
		}
		return output, runErr
	}

	checkout, err := secure.CreateAt(context.Background(), "alternates/post-clone", commit)
	if err == nil || checkout != nil || !strings.Contains(strings.ToLower(err.Error()), "alternates") {
		t.Fatalf("CreateAt with injected target alternate = (%+v, %v)", checkout, err)
	}
	for _, forbidden := range []string{"cat-file", "checkout"} {
		if containsString(commandsAfterClone, forbidden) {
			t.Fatalf("target alternate reached forbidden %s command: %v", forbidden, commandsAfterClone)
		}
	}
	assertOneResidualForTest(t, secure)
	if err := secure.Close(); err != nil {
		t.Fatalf("Close after target alternate rejection: %v", err)
	}
}

func TestPinnedGitRejectsByteIdenticalPathReplacementEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording shell Git fixture is not portable to Windows")
	}
	for _, timing := range []string{"before_create", "between_create_invocations"} {
		t.Run(timing, func(t *testing.T) {
			repo := initGitRepo(t)
			commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
			wrapperDir, logPath := installRecordingGitForTest(t)
			t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			secure := newSecureManagerForTest(t, repo)
			baseline := len(readInvocationLogForTest(t, logPath))
			if timing == "before_create" {
				replaceExecutablePathWithIdenticalFileForTest(t, secure.gitExecutable, secure.gitExecutableIdentity)
				checkout, err := secure.CreateAt(context.Background(), "git-replacement/before", commit)
				if err == nil || checkout != nil || !strings.Contains(strings.ToLower(err.Error()), "identity changed") {
					t.Fatalf("CreateAt after pre-call Git replacement = (%+v, %v)", checkout, err)
				}
				if got := len(readInvocationLogForTest(t, logPath)); got != baseline {
					t.Fatalf("replacement Git invocation count = %d, want unchanged %d", got, baseline)
				}
			} else {
				realRunner := secure.runGit
				replaced := false
				var firstArgs []string
				secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
					output, runErr := realRunner(ctx, dir, args...)
					if runErr == nil && !replaced {
						firstArgs = append([]string(nil), args...)
						replaceExecutablePathWithIdenticalFileForTest(t, secure.gitExecutable, secure.gitExecutableIdentity)
						replaced = true
					}
					return output, runErr
				}
				checkout, err := secure.CreateAt(context.Background(), "git-replacement/between", commit)
				if err == nil || checkout != nil || !strings.Contains(strings.ToLower(err.Error()), "identity changed") {
					t.Fatalf("CreateAt after between-command Git replacement = (%+v, %v)", checkout, err)
				}
				if !replaced || len(firstArgs) == 0 {
					t.Fatal("fixture did not replace Git between secure invocations")
				}
				if got := len(readInvocationLogForTest(t, logPath)); got != baseline+1 {
					t.Fatalf("replacement Git invocation count = %d, want exactly one post-baseline command", got)
				}
			}
			assertDirectoryEmpty(t, secure.checkoutsRoot)
			if err := secure.Close(); err != nil {
				t.Fatalf("Close after Git identity drift: %v", err)
			}
		})
	}
}

func TestSecureManagerPinsGitExecutableAcrossPathChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	fakeDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fake-git-invoked")
	writeShellExecutable(t, filepath.Join(fakeDir, "git"), "#!/bin/sh\nprintf invoked > '"+shellSingleQuote(marker)+"'\nexit 1\n")
	t.Setenv("PATH", fakeDir)

	wt, err := secure.CreateAt(context.Background(), "pinned/git", commit)
	if err != nil {
		t.Fatalf("CreateAt followed the changed PATH: %v", err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("replacement PATH git was invoked: %v", err)
	}
	if err := secure.Remove(context.Background(), wt); err != nil {
		t.Fatal(err)
	}
}

func TestSecureManagerRejectsSameInodeGitContentMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("copied Git executable fixture is not portable to Windows")
	}
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(realGit)
	if err != nil {
		t.Fatal(err)
	}
	fakeDir := t.TempDir()
	fakeGit := filepath.Join(fakeDir, "git")
	if err := os.WriteFile(fakeGit, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	secure := newSecureManagerForTest(t, repo)
	pinned := secure.gitExecutableIdentity

	mutated := append([]byte(nil), contents...)
	mutated[len(mutated)/2] ^= 0xff
	if err := os.WriteFile(fakeGit, mutated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fakeGit, pinned.modTime, pinned.modTime); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(fakeGit)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(after, pinned.fileInfo) || after.Size() != pinned.size || !after.ModTime().Equal(pinned.modTime) {
		t.Fatalf("fixture did not preserve inode, size, and mtime")
	}

	wt, err := secure.CreateAt(context.Background(), "pinned/content", commit)
	if err == nil || wt != nil || !strings.Contains(err.Error(), "content identity changed") {
		t.Fatalf("CreateAt after in-place Git mutation = (%+v, %v)", wt, err)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	runRoot := secure.runRoot
	if err := secure.Close(); err != nil {
		t.Fatalf("Close after Git drift returned error: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("Close after Git drift left run root: %v", err)
	}
}

func TestSecureExecutableIdentityRejectsPathReplacementAndOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git-fixture")
	if err := os.WriteFile(path, []byte("trusted-git\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity, err := captureExecutableIdentity(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	moved := path + "-moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("trusted-git\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, identity.modTime, identity.modTime); err != nil {
		t.Fatal(err)
	}
	if err := validateFileIdentity(context.Background(), path, identity, "test executable"); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("path replacement validation error = %v", err)
	}

	oversize := filepath.Join(t.TempDir(), "oversize-git")
	file, err := os.OpenFile(oversize, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxGitExecutableBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := captureExecutableIdentity(context.Background(), oversize, maxGitExecutableBytes); err == nil || !strings.Contains(err.Error(), "exceeds content limit") {
		t.Fatalf("oversize executable validation error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureExecutableIdentity(canceled, moved, maxGitExecutableBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled executable hash error = %v", err)
	}
}

func TestNewSecureManagerRejectsOversizeGitExecutableAtFormation(t *testing.T) {
	repo := initGitRepo(t)
	fakeDir := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	fakeGit := filepath.Join(fakeDir, name)
	file, err := os.OpenFile(fakeGit, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxGitExecutableBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)
	manager, err := NewSecureManager(context.Background(), repo, filepath.Join(t.TempDir(), "secure-run"))
	if err == nil || manager != nil || !strings.Contains(err.Error(), "exceeds content limit") {
		t.Fatalf("NewSecureManager with oversize Git = (%+v, %v)", manager, err)
	}
}

func TestSecureCreateAtHasSingleTotalDeadline(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	secure.createTimeout = 25 * time.Millisecond
	realRunner := secure.runGit
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "check-ref-format" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return realRunner(ctx, dir, args...)
	}
	started := time.Now()
	if _, err := secure.CreateAt(context.Background(), "deadline/total", commit); err == nil {
		t.Fatal("CreateAt ignored its total deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("CreateAt exceeded its bounded deadline: %v", elapsed)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
}

func TestSecureCreateAtDeadlineBoundsLifecycleContention(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	secure.createTimeout = 25 * time.Millisecond
	if err := acquireLifecycleGate(context.Background(), secure.lifecycleGate, "hold test lifecycle"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	wt, err := secure.CreateAt(context.Background(), "deadline/contention", commit)
	elapsed := time.Since(started)
	releaseLifecycleGate(secure.lifecycleGate)
	if err == nil || wt != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended CreateAt = (%+v, %v)", wt, err)
	}
	if elapsed > time.Second {
		t.Fatalf("contended CreateAt exceeded its operation bound: %v", elapsed)
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	assertDirectoryEmpty(t, secure.quarantineRoot)
}

func TestSecureCleanupDeadlinesBeginBeforeLifecycleAdmission(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")

	for _, exact := range []bool{false, true} {
		name := "remove"
		if exact {
			name = "remove_exact"
		}
		t.Run(name, func(t *testing.T) {
			secure := newSecureManagerForTest(t, repo)
			checkout, err := secure.CreateAt(context.Background(), "deadline/"+name, commit)
			if err != nil {
				t.Fatal(err)
			}
			secure.cleanupTimeout = 25 * time.Millisecond
			if err := acquireLifecycleGate(context.Background(), secure.lifecycleGate, "hold lifecycle gate"); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if exact {
				err = secure.RemoveExact(nil, checkout)
			} else {
				err = secure.Remove(nil, checkout)
			}
			elapsed := time.Since(started)
			releaseLifecycleGate(secure.lifecycleGate)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s before gate = %v, want deadline exceeded", name, err)
			}
			if elapsed > time.Second {
				t.Fatalf("%s gate admission exceeded total budget: %v", name, elapsed)
			}
			if _, err := os.Stat(checkout.path); err != nil {
				t.Fatalf("%s mutated checkout after deadline-before-gate: %v", name, err)
			}
			secure.cleanupTimeout = secureGitTimeout
			if err := secure.RemoveExact(context.Background(), checkout); err != nil {
				t.Fatalf("retry %s: %v", name, err)
			}
			if err := secure.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, contextual := range []bool{false, true} {
		name := "close"
		if contextual {
			name = "close_context"
		}
		t.Run(name, func(t *testing.T) {
			secure := newSecureManagerForTest(t, repo)
			secure.cleanupTimeout = 25 * time.Millisecond
			if err := acquireLifecycleGate(context.Background(), secure.lifecycleGate, "hold lifecycle gate"); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			var err error
			if contextual {
				err = secure.CloseContext(nil)
			} else {
				err = secure.Close()
			}
			elapsed := time.Since(started)
			releaseLifecycleGate(secure.lifecycleGate)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s before gate = %v, want deadline exceeded", name, err)
			}
			if elapsed > time.Second {
				t.Fatalf("%s gate admission exceeded total budget: %v", name, elapsed)
			}
			if _, err := os.Stat(secure.runRoot); err != nil {
				t.Fatalf("%s mutated run root after deadline-before-gate: %v", name, err)
			}
			secure.cleanupTimeout = secureGitTimeout
			if err := secure.Close(); err != nil {
				t.Fatalf("retry %s: %v", name, err)
			}
		})
	}
}

func TestSecureCreateFailureQuarantinesWithoutRecursiveRollback(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	recursiveRemovalCalled := false
	secure.beforeRecursiveRemoval = func(string) {
		recursiveRemovalCalled = true
		time.Sleep(2 * time.Second)
	}
	realRunner := secure.runGit
	var rollbackStarted time.Time
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			rollbackStarted = time.Now()
			return nil, errors.New("injected post-clone failure")
		}
		return output, err
	}
	wt, err := secure.CreateAt(context.Background(), "rollback/quarantine-only", commit)
	if err == nil || wt != nil {
		t.Fatalf("CreateAt after injected clone failure = (%+v, %v)", wt, err)
	}
	if rollbackStarted.IsZero() {
		t.Fatal("CreateAt did not reach injected post-clone failure")
	}
	if elapsed := time.Since(rollbackStarted); elapsed > time.Second {
		t.Fatalf("quarantine-only rollback exceeded bound: %v", elapsed)
	}
	if recursiveRemovalCalled {
		t.Fatal("failed CreateAt entered recursive cleanup")
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	assertOneResidualForTest(t, secure)
	secure.beforeRecursiveRemoval = nil
	if err := secure.Close(); err != nil {
		t.Fatalf("Close did not clean quarantined residual: %v", err)
	}
}

func TestSecureCreateRollsBackCloneThatReturnsFailureAfterMutation(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			return nil, errors.New("injected post-clone failure")
		}
		return output, err
	}
	if _, err := secure.CreateAt(context.Background(), "rollback/clone", commit); err == nil {
		t.Fatal("CreateAt succeeded despite injected post-clone failure")
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	if gitSucceeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/rollback/clone") {
		t.Fatal("failed isolated clone changed source refs")
	}
}

func TestSecureCreateFailingQuarantinePreservesReplacementAndCanRetryClose(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	var rollbackStarted time.Time
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "clone" {
			rollbackStarted = time.Now()
			return nil, errors.New("injected post-clone failure")
		}
		return output, err
	}
	var moved, marker string
	var hookErr error
	secure.beforeQuarantineRename = func(source, quarantine string) {
		moved = source + "-moved"
		if err := os.Rename(source, moved); err != nil {
			hookErr = err
			return
		}
		if err := os.Mkdir(source, 0o700); err != nil {
			hookErr = err
			return
		}
		marker = filepath.Join(source, "preserve")
		hookErr = os.WriteFile(marker, []byte("replacement"), 0o600)
	}
	wt, err := secure.CreateAt(context.Background(), "rollback/replacement", commit)
	if err == nil || wt != nil {
		t.Fatalf("CreateAt after failing quarantine = (%+v, %v)", wt, err)
	}
	if rollbackStarted.IsZero() {
		t.Fatal("CreateAt did not reach injected post-clone failure")
	}
	if elapsed := time.Since(rollbackStarted); elapsed > time.Second {
		t.Fatalf("failing quarantine exceeded bound: %v", elapsed)
	}
	if hookErr != nil {
		t.Fatalf("quarantine swap hook failed: %v", hookErr)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "replacement" {
		t.Fatalf("replacement changed: contents=%q err=%v", contents, err)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("issued directory moved by adversary changed: %v", err)
	}
	if err := secure.Close(); err == nil {
		t.Fatal("Close accepted unresolved replacement evidence")
	}
	secure.beforeQuarantineRename = nil
	if err := os.RemoveAll(filepath.Dir(marker)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, filepath.Dir(marker)); err != nil {
		t.Fatal(err)
	}
	if err := secure.Close(); err != nil {
		t.Fatalf("retry Close after restoring exact identity: %v", err)
	}
}

func TestSecureCreateRollsBackPostCheckoutVerificationFailure(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	realRunner := secure.runGit
	checkoutComplete := false
	secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		output, err := realRunner(ctx, dir, args...)
		if err == nil && len(args) > 0 && args[0] == "checkout" {
			checkoutComplete = true
			return output, nil
		}
		if checkoutComplete && len(args) > 0 && args[0] == "rev-parse" && containsArgument(args, "--show-toplevel") {
			return nil, errors.New("injected post-checkout verification failure")
		}
		return output, err
	}
	if _, err := secure.CreateAt(context.Background(), "rollback/verify", commit); err == nil {
		t.Fatal("CreateAt succeeded despite injected verification failure")
	}
	assertDirectoryEmpty(t, secure.checkoutsRoot)
	if gitSucceeds(repo, "show-ref", "--verify", "--quiet", "refs/heads/rollback/verify") {
		t.Fatal("failed isolated checkout changed source refs")
	}
}

func TestSecureRemoveUsesIssuedIdentityNotPresentationFields(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "cleanup/exact", commit)
	if err != nil {
		t.Fatal(err)
	}
	issuedPath := wt.path
	victim := t.TempDir()
	marker := filepath.Join(victim, "keep")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	forged := &SecureWorktree{path: issuedPath, Branch: wt.Branch, Commit: wt.Commit}
	if err := secure.Remove(context.Background(), forged); err == nil {
		t.Fatal("Remove accepted a forged checkout presentation")
	}
	if _, err := os.Stat(issuedPath); err != nil {
		t.Fatalf("forged cleanup changed issued checkout: %v", err)
	}
	wt.path = victim
	wt.Branch = "confused/target"
	if err := secure.Remove(context.Background(), wt); err != nil {
		t.Fatalf("identity-bound Remove returned error: %v", err)
	}
	if _, err := os.Lstat(issuedPath); !os.IsNotExist(err) {
		t.Fatalf("issued checkout still exists: %v", err)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "preserve" {
		t.Fatalf("cleanup touched presentation target: contents=%q err=%v", contents, err)
	}
	if err := secure.Remove(context.Background(), wt); err == nil {
		t.Fatal("Remove reused a consumed checkout capability")
	}
}

func TestSecureRemoveRefusesReplacedContainer(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "cleanup/swapped", commit)
	if err != nil {
		t.Fatal(err)
	}

	record := secureRecordForTest(t, secure, wt.secureID)
	moved := record.containerDir + "-moved"
	if err := os.Rename(record.containerDir, moved); err != nil {
		t.Fatalf("move issued container: %v", err)
	}
	if err := os.Mkdir(record.containerDir, 0o700); err != nil {
		t.Fatalf("create replacement container: %v", err)
	}
	marker := filepath.Join(record.containerDir, "preserve")
	if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := secure.Remove(context.Background(), wt); err == nil {
		t.Fatal("Remove accepted a replacement at the issued container path")
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "replacement" {
		t.Fatalf("replacement contents changed: contents=%q err=%v", contents, err)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("moved issued container changed: %v", err)
	}
	retained := secureCapabilityRetainedForTest(t, secure, wt.secureID)
	if !retained {
		t.Fatal("failed cleanup consumed the checkout capability")
	}
}

func TestSecureRemoveRefusesMissingContainer(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "cleanup/missing", commit)
	if err != nil {
		t.Fatal(err)
	}

	record := secureRecordForTest(t, secure, wt.secureID)
	moved := record.containerDir + "-moved"
	if err := os.Rename(record.containerDir, moved); err != nil {
		t.Fatalf("move issued container: %v", err)
	}

	if err := secure.Remove(context.Background(), wt); err == nil {
		t.Fatal("Remove accepted a missing issued container")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("moved issued container changed: %v", err)
	}
	retained := secureCapabilityRetainedForTest(t, secure, wt.secureID)
	if !retained {
		t.Fatal("failed cleanup consumed the checkout capability")
	}
}

func TestSecureRemoveRestoresReplacementSwappedBetweenValidationAndRename(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "cleanup/hook-swap", commit)
	if err != nil {
		t.Fatal(err)
	}
	record := secureRecordForTest(t, secure, wt.secureID)
	moved := record.issuedDir + "-moved"
	marker := filepath.Join(record.issuedDir, "preserve")
	var hookErr error
	secure.beforeQuarantineRename = func(source, quarantine string) {
		if err := os.Rename(source, moved); err != nil {
			hookErr = err
			return
		}
		if err := os.Mkdir(source, 0o700); err != nil {
			hookErr = err
			return
		}
		hookErr = os.WriteFile(marker, []byte("replacement"), 0o600)
	}

	if err := secure.Remove(context.Background(), wt); err == nil {
		t.Fatal("Remove accepted an entry swapped between validation and rename")
	}
	if hookErr != nil {
		t.Fatalf("swap hook failed: %v", hookErr)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "replacement" {
		t.Fatalf("replacement was not restored intact: contents=%q err=%v", contents, err)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("issued directory moved by adversary was changed: %v", err)
	}
	assertDirectoryEmpty(t, secure.quarantineRoot)
	retained := secureCapabilityRetainedForTest(t, secure, wt.secureID)
	if !retained {
		t.Fatal("failed quarantine consumed the checkout capability")
	}
}

func TestSecureRemoveCancellationLeavesRetryableResidualWithoutPostReturnMutation(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	checkout, err := secure.CreateAt(context.Background(), "cleanup/large-cancel", commit)
	if err != nil {
		t.Fatal(err)
	}
	payloadRoot := filepath.Join(checkout.path, "large-removal-tree")
	for directory := 0; directory < 32; directory++ {
		dir := filepath.Join(payloadRoot, fmt.Sprintf("dir-%03d", directory))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for file := 0; file < 64; file++ {
			path := filepath.Join(dir, fmt.Sprintf("file-%03d", file))
			if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink("dir-031/file-063", filepath.Join(payloadRoot, "zz-surviving-link")); err != nil && runtime.GOOS != "windows" {
		t.Fatalf("create snapshot symlink fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	entryCount := 0
	secure.beforeRemovalEntry = func(string) {
		entryCount++
		if entryCount == 96 {
			cancel()
		}
	}
	if err := secure.RemoveExact(ctx, checkout); !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveExact cancellation = %v, want context canceled", err)
	}
	record := secureRecordForTest(t, secure, checkout.secureID)
	if !record.quarantined || record.containerDir == record.issuedDir {
		t.Fatalf("canceled removal did not retain quarantine authority: %+v", record)
	}
	if _, err := os.Lstat(record.issuedDir); !os.IsNotExist(err) {
		t.Fatalf("issued path remains after quarantine: %v", err)
	}
	beforeWait := snapshotTreeForTest(t, record.containerDir)
	time.Sleep(100 * time.Millisecond)
	afterWait := snapshotTreeForTest(t, record.containerDir)
	if afterWait != beforeWait {
		t.Fatal("quarantined tree mutated after canceled RemoveExact returned")
	}

	secure.beforeRemovalEntry = nil
	if err := secure.RemoveExact(context.Background(), checkout); err != nil {
		t.Fatalf("retry RemoveExact: %v", err)
	}
	if secureCapabilityRetainedForTest(t, secure, checkout.secureID) {
		t.Fatal("successful retry retained consumed capability")
	}
	if err := secure.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveQuarantinedTreeCancelsAtSecondRootBatchWithExactResidual(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quarantined")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 129; i++ {
		path := filepath.Join(root, fmt.Sprintf("entry-%03d", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("payload-%03d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rootFrames := 0
	err = removeQuarantinedTree(ctx, root, expected, func(path string) {
		if path == root {
			rootFrames++
			if rootFrames == 2 {
				cancel()
			}
		}
	}, "batch fixture")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("second-batch cancellation = %v, want context canceled", err)
	}
	if rootFrames != 2 {
		t.Fatalf("root frame callbacks = %d, want 2", rootFrames)
	}
	remaining, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 65 {
		t.Fatalf("remaining entries = %d, want 65 after exactly 64 removals", len(remaining))
	}
	after, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(expected, after) {
		t.Fatal("canceled batch did not retain the exact retryable root identity")
	}
	if err := removeQuarantinedTree(context.Background(), root, expected, nil, "batch fixture retry"); err != nil {
		t.Fatalf("retry exact residual: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("retry left quarantined root: %v", err)
	}
}

func TestSecureRemoveBlocksSynchronouslyAndStopsCallbacksBeforeReturn(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	checkout, err := secure.CreateAt(context.Background(), "cleanup/blocking-hook", commit)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var callbackCount atomic.Int64
	secure.beforeRemovalEntry = func(string) {
		if callbackCount.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- secure.RemoveExact(context.Background(), checkout)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("RemoveExact did not enter blocking cleanup hook")
	}
	select {
	case err := <-done:
		t.Fatalf("RemoveExact returned before hook release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RemoveExact after hook release: %v", err)
	}
	afterReturn := callbackCount.Load()
	time.Sleep(100 * time.Millisecond)
	if got := callbackCount.Load(); got != afterReturn {
		t.Fatalf("cleanup callbacks advanced after public return: before=%d after=%d", afterReturn, got)
	}
	if err := secure.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCopiedSecureCapabilitiesRaceToExactlyOneConsumption(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	checkout, err := secure.CreateAt(context.Background(), "cleanup/copied-race", commit)
	if err != nil {
		t.Fatal(err)
	}
	first := *checkout
	second := *checkout
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		results <- secure.Remove(context.Background(), &first)
	}()
	go func() {
		defer workers.Done()
		<-start
		results <- secure.RemoveExact(context.Background(), &second)
	}()
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	consumed := 0
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		if strings.Contains(strings.ToLower(result.Error()), "unknown or already consumed") {
			consumed++
			continue
		}
		t.Fatalf("unexpected copied-capability result: %v", result)
	}
	if successes != 1 || consumed != 1 {
		t.Fatalf("copied capability results: successes=%d consumed=%d, want 1/1", successes, consumed)
	}
	if err := secure.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSecureRemoveRejectsQuarantineEntrySwapBeforeTraversal(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	checkout, err := secure.CreateAt(context.Background(), "cleanup/quarantine-swap", commit)
	if err != nil {
		t.Fatal(err)
	}
	issuedRecord := secureRecordForTest(t, secure, checkout.secureID)
	var quarantine, moved, marker string
	var hookErr error
	secure.beforeRecursiveRemoval = func(path string) {
		quarantine = path
		moved = path + "-moved"
		if err := os.Rename(path, moved); err != nil {
			hookErr = err
			return
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			hookErr = err
			return
		}
		marker = filepath.Join(path, "replacement-marker")
		hookErr = os.WriteFile(marker, []byte("replacement"), 0o600)
	}
	err = secure.Remove(context.Background(), checkout)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "identity changed") {
		t.Fatalf("Remove after quarantine entry swap = %v", err)
	}
	if hookErr != nil {
		t.Fatalf("quarantine swap hook: %v", hookErr)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "replacement" {
		t.Fatalf("quarantine replacement changed: contents=%q err=%v", contents, err)
	}
	movedInfo, err := os.Stat(moved)
	if err != nil {
		t.Fatalf("moved issued identity missing: %v", err)
	}
	if !os.SameFile(issuedRecord.containerIdentity, movedInfo) {
		t.Fatal("moved quarantine entry is not the issued container identity")
	}
	retained := secureRecordForTest(t, secure, checkout.secureID)
	if !retained.quarantined || retained.containerDir != quarantine || !os.SameFile(retained.containerIdentity, movedInfo) {
		t.Fatalf("failed traversal did not retain exact quarantine capability: %+v", retained)
	}
	secure.beforeRecursiveRemoval = nil
	if err := os.RemoveAll(quarantine); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, quarantine); err != nil {
		t.Fatal(err)
	}
	if err := secure.RemoveExact(context.Background(), checkout); err != nil {
		t.Fatalf("retry restored quarantine identity: %v", err)
	}
	if err := secure.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSecureCloseCancellationLeavesRetryableRunResidualWithoutPostReturnMutation(t *testing.T) {
	repo := initGitRepo(t)
	secure := newSecureManagerForTest(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	secure.beforeRemovalEntry = func(string) {
		cancel()
	}
	if err := secure.CloseContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CloseContext cancellation = %v, want context canceled", err)
	}
	if secure.closeResidual == "" || secure.closeResidualIdentity == nil {
		t.Fatal("canceled CloseContext did not retain run-root quarantine authority")
	}
	if _, err := os.Lstat(secure.runRoot); !os.IsNotExist(err) {
		t.Fatalf("original run root remains after quarantine: %v", err)
	}
	beforeWait := snapshotTreeForTest(t, secure.closeResidual)
	time.Sleep(100 * time.Millisecond)
	afterWait := snapshotTreeForTest(t, secure.closeResidual)
	if afterWait != beforeWait {
		t.Fatal("run-root residual mutated after canceled CloseContext returned")
	}

	secure.beforeRemovalEntry = nil
	if err := secure.CloseContext(context.Background()); err != nil {
		t.Fatalf("retry CloseContext: %v", err)
	}
	if !secure.closed || secure.closeResidual != "" || secure.closeResidualIdentity != nil {
		t.Fatal("successful close retry retained residual authority")
	}
}

func TestSecureManagerCloseRequiresNoActiveCapabilitiesAndRemovesExactHierarchy(t *testing.T) {
	repo := initGitRepo(t)
	commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
	secure := newSecureManagerForTest(t, repo)
	wt, err := secure.CreateAt(context.Background(), "close/exact", commit)
	if err != nil {
		t.Fatal(err)
	}
	runRoot := secure.runRoot
	if err := secure.Close(); err == nil {
		t.Fatal("Close accepted an active checkout capability")
	}
	if err := secure.Remove(context.Background(), wt); err != nil {
		t.Fatal(err)
	}
	if err := secure.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, err := os.Lstat(runRoot); !os.IsNotExist(err) {
		t.Fatalf("closed run root remains: %v", err)
	}
	if err := secure.Close(); err != nil {
		t.Fatalf("idempotent Close returned error: %v", err)
	}
}

func TestSecureManagerCloseRefusesQuarantineEvidence(t *testing.T) {
	repo := initGitRepo(t)
	secure := newSecureManagerForTest(t, repo)
	marker := filepath.Join(secure.quarantineRoot, "preserve")
	if err := os.WriteFile(marker, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := secure.Close(); err == nil {
		t.Fatal("Close accepted non-empty quarantine evidence")
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "evidence" {
		t.Fatalf("Close changed quarantine evidence: contents=%q err=%v", contents, err)
	}
}

func TestNewSecureManagerRejectsRunRootOverlapMatrixWithoutMutation(t *testing.T) {
	repo := initGitRepo(t)
	sourceMarker := filepath.Join(repo, "source-marker")
	if err := os.WriteFile(sourceMarker, []byte("source-preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitDir := gitOutputForTest(t, repo, "rev-parse", "--path-format=absolute", "--git-dir")
	gitDir, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	commonDir := gitOutputForTest(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	objectsDir := gitOutputForTest(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	objectsDir, err = filepath.EvalSymlinks(objectsDir)
	if err != nil {
		t.Fatal(err)
	}
	secondary := filepath.Join(t.TempDir(), "secondary-worktree")
	runGit(t, repo, "worktree", "add", "--detach", secondary, "HEAD")
	secondaryMarker := filepath.Join(secondary, "secondary-marker")
	if err := os.WriteFile(secondaryMarker, []byte("secondary-preserved"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		runRoot    string
		preexists  bool
		wantDetail string
	}{
		{name: "source_ancestor", runRoot: filepath.Dir(repo), preexists: true, wantDetail: "must not already exist"},
		{name: "inside_source_git", runRoot: filepath.Join(gitDir, "secure-run-inside-git"), wantDetail: "overlaps source"},
		{name: "inside_source_common", runRoot: filepath.Join(commonDir, "secure-run-inside-common"), wantDetail: "overlaps source"},
		{name: "inside_source_objects", runRoot: filepath.Join(objectsDir, "secure-run-inside-objects"), wantDetail: "overlaps source"},
		{name: "inside_registered_secondary", runRoot: filepath.Join(secondary, "secure-run-inside-secondary"), wantDetail: "overlaps registered worktree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewSecureManager(context.Background(), repo, tt.runRoot)
			if err == nil || manager != nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantDetail) {
				t.Fatalf("NewSecureManager overlap = (%+v, %v), want detail %q", manager, err, tt.wantDetail)
			}
			if !tt.preexists {
				if _, err := os.Lstat(tt.runRoot); !os.IsNotExist(err) {
					t.Fatalf("rejected overlap created run root: %v", err)
				}
			}
			if contents, err := os.ReadFile(sourceMarker); err != nil || string(contents) != "source-preserved" {
				t.Fatalf("source overlap marker changed: contents=%q err=%v", contents, err)
			}
			if contents, err := os.ReadFile(secondaryMarker); err != nil || string(contents) != "secondary-preserved" {
				t.Fatalf("secondary overlap marker changed: contents=%q err=%v", contents, err)
			}
		})
	}
}

func TestSecureManagerIdentitySwapMatrixFailsBeforeResolutionOrCleanup(t *testing.T) {
	tests := []struct {
		name       string
		linkedRepo bool
		closePath  bool
		selectPath func(*SecureManager) string
	}{
		{name: "source_root", selectPath: func(sm *SecureManager) string { return sm.sourceRoot }},
		{name: "source_git_directory", linkedRepo: true, selectPath: func(sm *SecureManager) string { return sm.sourceGitDir }},
		{name: "source_common_directory", linkedRepo: true, selectPath: func(sm *SecureManager) string { return sm.sourceCommonGitDir }},
		{name: "run_parent", closePath: true, selectPath: func(sm *SecureManager) string { return sm.runParent }},
		{name: "run_root", closePath: true, selectPath: func(sm *SecureManager) string { return sm.runRoot }},
		{name: "empty_template", selectPath: func(sm *SecureManager) string { return sm.templateRoot }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initGitRepo(t)
			if tt.linkedRepo {
				linked := filepath.Join(t.TempDir(), "linked-source")
				runGit(t, repo, "worktree", "add", "--detach", linked, "HEAD")
				repo = linked
			}
			commit := gitOutputForTest(t, repo, "rev-parse", "--verify", "HEAD^{commit}")
			secure := newSecureManagerForTest(t, repo)
			selected := tt.selectPath(secure)
			var moved, replacementMarker string
			var originalIdentity os.FileInfo
			var restore func()
			switch tt.name {
			case "source_root":
				moved, replacementMarker, originalIdentity, restore = swapAncestorIdentityPreservingChildrenForTest(t, selected, []string{secure.sourceGitDir})
			case "source_common_directory":
				moved, replacementMarker, originalIdentity, restore = swapAncestorIdentityPreservingChildrenForTest(t, selected, []string{secure.sourceGitDir, secure.sourceObjectsDir})
			case "run_parent":
				moved, replacementMarker, originalIdentity, restore = swapAncestorIdentityPreservingChildrenForTest(t, selected, []string{secure.runRoot})
			default:
				moved, replacementMarker, originalIdentity, restore = swapDirectoryIdentityForTest(t, selected)
			}

			realRunner := secure.runGit
			var gitCalls atomic.Int64
			secure.runGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
				gitCalls.Add(1)
				return realRunner(ctx, dir, args...)
			}
			var cleanupCallbacks atomic.Int64
			secure.beforeRemovalEntry = func(string) {
				cleanupCallbacks.Add(1)
			}
			var operationErr error
			if tt.closePath {
				operationErr = secure.CloseContext(context.Background())
			} else {
				_, operationErr = secure.CreateAt(context.Background(), "identity-swap/"+tt.name, commit)
			}
			if operationErr == nil || (!strings.Contains(strings.ToLower(operationErr.Error()), "identity") && !strings.Contains(strings.ToLower(operationErr.Error()), "missing")) {
				t.Fatalf("operation after %s swap = %v", tt.name, operationErr)
			}
			if got := gitCalls.Load(); got != 0 {
				t.Fatalf("%s swap reached Git/object-resolution commands: %d", tt.name, got)
			}
			if got := cleanupCallbacks.Load(); got != 0 {
				t.Fatalf("%s swap reached recursive cleanup: %d callbacks", tt.name, got)
			}
			if contents, err := os.ReadFile(replacementMarker); err != nil || string(contents) != "replacement" {
				t.Fatalf("%s replacement marker changed: contents=%q err=%v", tt.name, contents, err)
			}
			movedInfo, err := os.Stat(moved)
			if err != nil || !os.SameFile(originalIdentity, movedInfo) {
				t.Fatalf("%s moved identity changed: info=%v err=%v", tt.name, movedInfo, err)
			}
			if !tt.closePath {
				assertDirectoryEmpty(t, secure.checkoutsRoot)
				assertDirectoryEmpty(t, secure.quarantineRoot)
			}

			restore()
			if tt.closePath {
				assertDirectoryEmpty(t, secure.checkoutsRoot)
				assertDirectoryEmpty(t, secure.quarantineRoot)
			}
			secure.runGit = realRunner
			secure.beforeRemovalEntry = nil
			if err := secure.Close(); err != nil {
				t.Fatalf("Close after restoring %s identity: %v", tt.name, err)
			}
		})
	}
}

func TestBoundedGitOutputCancelsAtLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	output := &boundedGitOutput{limit: 4, cancel: cancel}
	if written, err := output.Write([]byte("excess")); err != nil || written != len("excess") {
		t.Fatalf("Write = (%d, %v)", written, err)
	}
	if !output.Exceeded() || output.String() != "exce" {
		t.Fatalf("bounded output = %q exceeded=%v", output.String(), output.Exceeded())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("output limit did not cancel command context")
	}
}

func newSecureManagerForTest(t *testing.T, repo string) *SecureManager {
	t.Helper()
	runRoot := filepath.Join(t.TempDir(), "secure-run")
	manager, err := NewSecureManager(context.Background(), repo, runRoot)
	if err != nil {
		t.Fatalf("NewSecureManager returned error: %v", err)
	}
	return manager
}

func secureRecordForTest(t *testing.T, manager *SecureManager, id [32]byte) secureCheckoutRecord {
	t.Helper()
	if err := acquireLifecycleGate(context.Background(), manager.lifecycleGate, "inspect test checkout lifecycle"); err != nil {
		t.Fatal(err)
	}
	defer releaseLifecycleGate(manager.lifecycleGate)
	record, ok := manager.active[id]
	if !ok {
		t.Fatal("secure checkout record is missing")
	}
	return record
}

func secureCapabilityRetainedForTest(t *testing.T, manager *SecureManager, id [32]byte) bool {
	t.Helper()
	if err := acquireLifecycleGate(context.Background(), manager.lifecycleGate, "inspect test checkout lifecycle"); err != nil {
		t.Fatal(err)
	}
	defer releaseLifecycleGate(manager.lifecycleGate)
	_, ok := manager.active[id]
	return ok
}

func assertOneResidualForTest(t *testing.T, manager *SecureManager) {
	t.Helper()
	if err := acquireLifecycleGate(context.Background(), manager.lifecycleGate, "inspect test residual lifecycle"); err != nil {
		t.Fatal(err)
	}
	defer releaseLifecycleGate(manager.lifecycleGate)
	if len(manager.residual) != 1 {
		t.Fatalf("residual checkout count = %d, want 1", len(manager.residual))
	}
	entries, err := os.ReadDir(manager.quarantineRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quarantine entry count = %d, want 1", len(entries))
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %s: %v", path, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory %s is not empty: %v", path, names)
	}
}

func snapshotTreeForTest(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contentDigest := "-"
		linkTarget := "-"
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(contents)
			contentDigest = hex.EncodeToString(digest[:])
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			linkTarget = target
		}
		_, _ = fmt.Fprintf(
			&snapshot,
			"%s\x00%s\x00%d\x00%s\x00%s\x00%s\n",
			relative,
			info.Mode(),
			info.Size(),
			filesystemIdentityForTest(info),
			contentDigest,
			linkTarget,
		)
		return nil
	}); err != nil {
		t.Fatalf("snapshot tree %s: %v", root, err)
	}
	return snapshot.String()
}

func filesystemIdentityForTest(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return "unavailable"
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "unavailable"
		}
		value = value.Elem()
	}
	var fields []string
	for _, name := range []string{"Dev", "Ino", "VolumeSerialNumber", "FileIndexHigh", "FileIndexLow"} {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		switch field.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			fields = append(fields, fmt.Sprintf("%s=%d", name, field.Uint()))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fields = append(fields, fmt.Sprintf("%s=%d", name, field.Int()))
		}
	}
	return fmt.Sprintf("%T{%s}", info.Sys(), strings.Join(fields, ","))
}

func containsArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsEnvironment(environment []string, target string) bool {
	for _, entry := range environment {
		if entry == target {
			return true
		}
	}
	return false
}

func shellSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "'\"'\"'")
}

func writeShellExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func installRecordingGitForTest(t *testing.T) (string, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err = filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git-invocations")
	quotedLog := shellSingleQuote(logPath)
	script := "#!/bin/sh\n" +
		"git_dir=\"${GIT_DIR+set}\"; [ -n \"$git_dir\" ] || git_dir=unset\n" +
		"work_tree=\"${GIT_WORK_TREE+set}\"; [ -n \"$work_tree\" ] || work_tree=unset\n" +
		"common=\"${GIT_COMMON_DIR+set}\"; [ -n \"$common\" ] || common=unset\n" +
		"object=\"${GIT_OBJECT_DIRECTORY+set}\"; [ -n \"$object\" ] || object=unset\n" +
		"alternates=\"${GIT_ALTERNATE_OBJECT_DIRECTORIES+set}\"; [ -n \"$alternates\" ] || alternates=unset\n" +
		"printf 'lazy=%s;git_dir=%s;work_tree=%s;common=%s;object=%s;alternates=%s;argv=' \"${GIT_NO_LAZY_FETCH-unset}\" \"$git_dir\" \"$work_tree\" \"$common\" \"$object\" \"$alternates\" >> '" + quotedLog + "'\n" +
		"for arg in \"$@\"; do printf '[%s]' \"$arg\" >> '" + quotedLog + "'; done\n" +
		"printf '\\n' >> '" + quotedLog + "'\n" +
		"exec '" + shellSingleQuote(realGit) + "' \"$@\"\n"
	writeShellExecutable(t, filepath.Join(wrapperDir, "git"), script)
	return wrapperDir, logPath
}

func readInvocationLogForTest(t *testing.T, path string) []string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(contents))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func replaceExecutablePathWithIdenticalFileForTest(t *testing.T, path string, expected secureExecutableIdentity) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	moved := path + "-pinned-original"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, expected.mode.Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, expected.mode.Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, expected.modTime, expected.modTime); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(replacement, expected.fileInfo) {
		t.Fatal("byte-identical executable replacement retained the pinned inode")
	}
	if replacement.Size() != expected.size || replacement.Mode() != expected.mode || !replacement.ModTime().Equal(expected.modTime) {
		t.Fatalf("replacement did not restore size/mode/mtime: size=%d mode=%s mtime=%s", replacement.Size(), replacement.Mode(), replacement.ModTime())
	}
}

func swapDirectoryIdentityForTest(t *testing.T, path string) (string, string, os.FileInfo, func()) {
	t.Helper()
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	moved := path + "-issued-identity"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, original.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "replacement-marker")
	if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		t.Helper()
		if restored {
			return
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatalf("remove replacement directory %s: %v", path, err)
		}
		if err := os.Rename(moved, path); err != nil {
			t.Fatalf("restore issued directory %s: %v", path, err)
		}
		restored = true
	}
	t.Cleanup(restore)
	return moved, marker, original, restore
}

func swapAncestorIdentityPreservingChildrenForTest(t *testing.T, ancestor string, children []string) (string, string, os.FileInfo, func()) {
	t.Helper()
	original, err := os.Stat(ancestor)
	if err != nil {
		t.Fatal(err)
	}
	stageRoot := ancestor + "-preserved-children"
	if err := os.Mkdir(stageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := make([]string, len(children))
	for index, child := range children {
		if !isStrictDescendant(ancestor, child) {
			t.Fatalf("preserved child %s is not below ancestor %s", child, ancestor)
		}
		staged[index] = filepath.Join(stageRoot, fmt.Sprintf("child-%d", index))
		if err := os.Rename(child, staged[index]); err != nil {
			t.Fatalf("stage preserved child %s: %v", child, err)
		}
	}
	moved := ancestor + "-issued-identity"
	if err := os.Rename(ancestor, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ancestor, original.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	for index, child := range children {
		if err := os.MkdirAll(filepath.Dir(child), 0o700); err != nil {
			t.Fatalf("recreate preserved child parent %s: %v", child, err)
		}
		if err := os.Rename(staged[index], child); err != nil {
			t.Fatalf("restore preserved child into replacement ancestor %s: %v", child, err)
		}
	}
	marker := filepath.Join(ancestor, "replacement-marker")
	if err := os.WriteFile(marker, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		t.Helper()
		if restored {
			return
		}
		for index, child := range children {
			if err := os.Rename(child, staged[index]); err != nil {
				t.Fatalf("restage preserved child %s: %v", child, err)
			}
		}
		if err := os.RemoveAll(ancestor); err != nil {
			t.Fatalf("remove replacement ancestor %s: %v", ancestor, err)
		}
		if err := os.Rename(moved, ancestor); err != nil {
			t.Fatalf("restore issued ancestor %s: %v", ancestor, err)
		}
		for index, child := range children {
			if err := os.MkdirAll(filepath.Dir(child), 0o700); err != nil {
				t.Fatalf("recreate issued child parent %s: %v", child, err)
			}
			if err := os.Rename(staged[index], child); err != nil {
				t.Fatalf("restore issued child %s: %v", child, err)
			}
		}
		if err := os.Remove(stageRoot); err != nil {
			t.Fatalf("remove preserved-child stage %s: %v", stageRoot, err)
		}
		restored = true
	}
	t.Cleanup(restore)
	return moved, marker, original, restore
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitSucceeds(dir string, args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}
