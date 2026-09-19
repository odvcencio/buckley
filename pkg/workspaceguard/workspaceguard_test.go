package workspaceguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/launchcontract"
)

const testMITLicense = `MIT License

Copyright (c) 2026 Buckley Testers

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

func TestResolveLaunchProfile_ExactDisplayOnlyMetadata(t *testing.T) {
	for _, tt := range []struct {
		id                         string
		requests                   int
		input, output, total       int64
		request, turn, absoluteRun time.Duration
	}{
		{id: "gsxmail", requests: 12, input: 6_000_000, output: 393_216, total: 6_393_216, request: 15 * time.Minute, turn: 30 * time.Minute, absoluteRun: 90 * time.Minute},
		{id: "gosx", requests: 24, input: 12_000_000, output: 786_432, total: 12_786_432, request: 20 * time.Minute, turn: 45 * time.Minute, absoluteRun: 4 * time.Hour},
		{id: "tqwebp", requests: 24, input: 12_000_000, output: 786_432, total: 12_786_432, request: 20 * time.Minute, turn: 45 * time.Minute, absoluteRun: 4 * time.Hour},
	} {
		profile, err := ResolveLaunchProfile(tt.id)
		if err != nil {
			t.Fatalf("ResolveLaunchProfile(%q): %v", tt.id, err)
		}
		limits := profile.Limits
		if profile.ID != tt.id || limits.ModelRequests != tt.requests || limits.InputTokens != tt.input || limits.OutputTokens != tt.output || limits.TotalTokens != tt.total || limits.MaxOutputPerRequest != 32_768 || limits.RequestTimeout != tt.request || limits.TurnTimeout != tt.turn || limits.AbsoluteRunTimeout != tt.absoluteRun || limits.PricePolicy != LaunchPricePolicyFreeOnly || limits.GlobalCapacity != 2 || limits.PerRunParallelism != 2 || limits.Enforced || limits.State != LaunchStateAdmissionPending {
			t.Fatalf("profile %q = %+v", tt.id, profile)
		}
	}
	if _, err := ResolveLaunchProfile("unknown"); err == nil {
		t.Fatal("unknown profile unexpectedly resolved")
	}
}

func TestGitInspector_AllowsCleanMITRepositoryWithDeterministicManifest(t *testing.T) {
	root := newTestGitRepo(t)
	inspector := NewGitInspector(Options{})

	first, err := inspector.Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("first Inspect: %v", err)
	}
	if !first.Allowed || len(first.Findings) != 0 {
		t.Fatalf("first report = %+v, want allowed with no findings", first)
	}
	if first.Evidence.Schema != EvidenceSchema || len(first.Evidence.RootSHA256) != 64 || len(first.Evidence.HEAD) != 40 || len(first.Evidence.ManifestSHA256) != 64 {
		t.Fatalf("first evidence = %+v, want bounded SHA-256 evidence", first.Evidence)
	}
	if first.Evidence.TrackedFiles != 2 || first.Evidence.UntrackedFiles != 0 || first.Evidence.IgnoredFiles != 0 {
		t.Fatalf("first counts = %+v, want two tracked files and no loose files", first.Evidence)
	}
	if first.Evidence.LicenseID != "MIT" || first.Evidence.LicensePath != "LICENSE" || first.Evidence.LicenseSHA256 == "" || first.Evidence.LicenseManifestSHA256 == "" {
		t.Fatalf("first license evidence = %+v, want recognized MIT", first.Evidence)
	}

	second, err := inspector.Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("second Inspect: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated inspection changed report:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}

func TestGitInspector_ObserveWorkspaceBindsActualPreflightLicenseAndRoot(t *testing.T) {
	root := newTestGitRepo(t)
	profile, err := launchcontract.ResolveProfile("gosx")
	if err != nil {
		t.Fatal(err)
	}
	inspector := NewGitInspector(Options{})
	proof, err := inspector.VerifyLaunch(context.Background(), root, profile)
	if err != nil {
		t.Fatalf("VerifyLaunch: %v", err)
	}
	observation := proof.Snapshot()
	if observation.CanonicalRoot != root || observation.Schema != EvidenceSchema || observation.LicenseID != "MIT" || observation.LicensePath != "LICENSE" {
		t.Fatalf("observation = %+v", observation)
	}
	for _, digest := range []string{observation.RootSHA256, observation.ManifestSHA256, observation.PreflightSHA256, observation.LicenseSHA256, observation.LicenseManifestSHA256} {
		if len(digest) != 64 {
			t.Fatalf("unbound observation digest %q", digest)
		}
	}
	second, err := inspector.VerifyLaunch(context.Background(), root, profile)
	if err != nil || !reflect.DeepEqual(second.Snapshot(), observation) {
		t.Fatalf("repeat observation = %+v, %v", second.Snapshot(), err)
	}
	writeTestFile(t, root, "main.go", "package changed\n")
	if _, err := inspector.VerifyLaunch(context.Background(), root, profile); err == nil {
		t.Fatal("dirty workspace observation was admitted")
	}
}

func TestGitInspector_ObserveWorkspaceRejectsNoncanonicalAndWrongLicenseContract(t *testing.T) {
	root := newTestGitRepo(t)
	profile, err := launchcontract.ResolveProfile("gsxmail")
	if err != nil {
		t.Fatal(err)
	}
	inspector := NewGitInspector(Options{})
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := inspector.VerifyLaunch(context.Background(), link, profile); err == nil {
		t.Fatal("symlink workspace alias was admitted")
	}
	profile.License.AllowedIDs = []string{"Apache-2.0"}
	if _, err := inspector.VerifyLaunch(context.Background(), root, profile); err == nil {
		t.Fatal("mutated profile was admitted")
	}
}

func TestGitInspector_RejectsNonWorktree(t *testing.T) {
	root := t.TempDir()
	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonNonWorktree)
	if report.Allowed {
		t.Fatalf("report = %+v, want disallowed", report)
	}
}

func TestGitInspector_CanceledContextStopsBeforeGit(t *testing.T) {
	runner := &fixedGitRunner{top: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewGitInspector(Options{Git: runner}).Inspect(ctx, Request{Root: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want context canceled", err)
	}
	if runner.calls != 0 {
		t.Fatalf("canceled inspection made %d Git calls", runner.calls)
	}
}

func TestOpenRootBinding_PinsExactDirectoryAndRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := OpenRootBinding(root)
	if err != nil {
		t.Fatalf("OpenRootBinding: %v", err)
	}
	defer binding.Close()
	original := binding.info
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	bound, err := os.Stat(binding.Source())
	if err != nil || !os.SameFile(original, bound) {
		t.Fatalf("binding drifted after replacement: info=%v err=%v", bound, err)
	}
	replacement, err := os.Stat(root)
	if err != nil || os.SameFile(original, replacement) {
		t.Fatalf("replacement identity was not distinct: info=%v err=%v", replacement, err)
	}

	link := filepath.Join(parent, "workspace-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenRootBinding(link); err == nil || got != nil {
		t.Fatalf("symlink binding = %#v, %v; want unavailable", got, err)
	}
}

func TestReportMatchesOnlyExactRootBinding(t *testing.T) {
	root := newTestGitRepo(t)
	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil || !report.Allowed {
		t.Fatalf("Inspect = %+v, %v", report, err)
	}
	binding, err := OpenRootBinding(root)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	if !report.MatchesRoot(binding) {
		t.Fatal("report did not match the exact root binding")
	}
	otherBinding, err := OpenRootBinding(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer otherBinding.Close()
	if report.MatchesRoot(otherBinding) {
		t.Fatal("report matched a foreign root binding")
	}
}

func TestResolveTrustedGitBinary_IgnoresAmbientPath(t *testing.T) {
	fakeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeDir, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)
	got, err := resolveTrustedGitBinary()
	if err != nil {
		t.Fatalf("resolveTrustedGitBinary: %v", err)
	}
	if got != trustedGitBinary {
		t.Fatalf("git binary = %q, want %q", got, trustedGitBinary)
	}
}

func TestGitInspector_RejectsRootAliasAndMismatchedGitRoot(t *testing.T) {
	root := newTestGitRepo(t)
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "workspace-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("Symlink workspace alias: %v", err)
	}
	aliased, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: alias})
	if err != nil {
		t.Fatalf("aliased Inspect: %v", err)
	}
	assertHasCode(t, aliased, ReasonRootNotCanonical)
	if aliased.Allowed {
		t.Fatalf("aliased report = %+v, want disallowed", aliased)
	}

	otherRoot := t.TempDir()
	runner := &fixedGitRunner{top: otherRoot}
	mismatched, err := NewGitInspector(Options{Git: runner}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("mismatched Inspect: %v", err)
	}
	assertHasCode(t, mismatched, ReasonRootMismatch)
	if mismatched.Allowed || runner.calls != 1 {
		t.Fatalf("mismatched report = %+v, runner calls = %d; want one root check and disallowed", mismatched, runner.calls)
	}
}

func TestGitInspector_RejectsTrackedDirt(t *testing.T) {
	root := newTestGitRepo(t)
	writeTestFile(t, root, "main.go", "package main\n// dirty\n")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonTrackedDirty)
	if report.Allowed {
		t.Fatalf("report = %+v, want disallowed", report)
	}
}

func TestGitInspector_AllowsAndBindsBenignUntrackedAndIgnoredFiles(t *testing.T) {
	root := newTestGitRepo(t)
	writeTestFile(t, root, ".gitignore", "build/\n")
	testGit(t, root, "add", ".gitignore")
	testGit(t, root, "commit", "--quiet", "-m", "ignore fixture")
	writeTestFile(t, root, "notes.tmp", "ordinary notes\n")
	if err := os.Mkdir(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatalf("Mkdir build: %v", err)
	}
	writeTestFile(t, root, "build/output.txt", "ordinary generated output\n")

	first, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !first.Allowed || first.Evidence.UntrackedFiles != 1 || first.Evidence.IgnoredFiles != 1 {
		t.Fatalf("report = %+v, want allowed with one untracked and one ignored file", first)
	}
	second, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated report = %+v, %v; first = %+v", second, err, first)
	}
}

func TestGitInspector_RejectsRepositoryWithoutValidHEAD(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "--quiet")
	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonInvalidHEAD)
}

func TestGitInspector_FindsUntrackedAndIgnoredSecretPathAndContent(t *testing.T) {
	root := newTestGitRepo(t)
	writeTestFile(t, root, ".gitignore", "ignored.env\n")
	testGit(t, root, "add", ".gitignore")
	testGit(t, root, "commit", "--quiet", "-m", "ignore fixture")

	untrackedSecret := "AWS_SECRET_ACCESS_" + "KEY=" + strings.Repeat("Ab3dEf7h", 5)
	ignoredSecret := "client_" + "secret=" + strings.Repeat("Cd5jKl9m", 4)
	writeTestFile(t, root, ".env", untrackedSecret+"\n")
	writeTestFile(t, root, "ignored.env", ignoredSecret+"\n")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSecretPath)
	assertHasCode(t, report, ReasonSecretContent)
	if report.Evidence.UntrackedFiles != 1 || report.Evidence.IgnoredFiles != 1 {
		t.Fatalf("loose file counts = %+v, want one untracked and one ignored file", report.Evidence)
	}
	assertReportBodyFree(t, report, untrackedSecret, ignoredSecret, "untracked-secret-body", "ignored-secret-body")
}

func TestGitInspector_FindsTrackedSecretPathsAndStreamingContent(t *testing.T) {
	root := newTestGitRepo(t)
	envBody := "AWS_SECRET_ACCESS_" + "KEY=" + strings.Repeat("Ab3dEf7h", 5)
	marker := "client_" + "secret=" + strings.Repeat("Cd5jKl9m", 4)
	renamedMarker := "aws_secret_access_" + "key=" + strings.Repeat("aB9/cD2+eF7=", 4)
	writeTestFile(t, root, ".env.production", envBody+"\n")
	large := strings.Repeat("ordinary source line\n", (2<<20)/21) + marker + "\n"
	writeTestFile(t, root, "pkg-data.txt", large)
	writeTestFile(t, root, "payload.wasm", renamedMarker+"\n")
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "keys/private.key", "ordinary credential-shaped tracked path\n")
	testGit(t, root, "add", ".env.production", "pkg-data.txt", "payload.wasm", "keys/private.key")
	testGit(t, root, "commit", "--quiet", "-m", "tracked secret fixtures")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSecretPath)
	assertHasCode(t, report, ReasonSecretContent)
	if report.Allowed {
		t.Fatalf("tracked secret report = %+v, want blocked", report)
	}
	assertReportBodyFree(t, report, envBody, marker, renamedMarker, "tracked-secret-body", "tracked-token-body", "renamed-text-body")
}

func TestGitInspector_FindsTrackedPrivateKeyBehindBinarySuffixAcrossChunkBoundary(t *testing.T) {
	root := newTestGitRepo(t)
	header := "-----BEGIN OPENSSH " + "PRIVATE KEY-----\n"
	payload := strings.Repeat("Ab3dEf7h", 8)
	body := strings.Repeat("x", trackedPrefixBytes-len(header)/2) + header + payload + "\n"
	writeTestFile(t, root, "payload.wasm", body)
	testGit(t, root, "add", "payload.wasm")
	testGit(t, root, "commit", "--quiet", "-m", "tracked disguised private key")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	assertHasCode(t, report, ReasonSecretContent)
	if report.Allowed {
		t.Fatal("tracked private key behind binary suffix was admitted")
	}
	assertReportBodyFree(t, report, header, payload)
}

func TestGitInspector_AllowsLargeTrackedBinaryAndTextArtifacts(t *testing.T) {
	root := newTestGitRepo(t)
	binary := make([]byte, 10<<20)
	for idx := range binary {
		binary[idx] = byte(idx % 251)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "app.wasm"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "dist/app.js.map", strings.Repeat("source-map-text\n", (6<<20)/16))
	testGit(t, root, "add", "dist/app.wasm", "dist/app.js.map")
	testGit(t, root, "commit", "--quiet", "-m", "large tracked artifacts")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.Allowed {
		t.Fatalf("large safe tracked artifacts blocked: %+v", report)
	}
}

func TestGitInspector_RehashesTrackedTextWhenGitMetadataIsStable(t *testing.T) {
	root := newTestGitRepo(t)
	path := filepath.Join(root, "main.go")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "update-index", "--assume-unchanged", "main.go")
	t.Cleanup(func() { testGit(t, root, "update-index", "--no-assume-unchanged", "main.go") })
	inspector := NewGitInspector(Options{AfterSnapshot: func() {
		if err := os.WriteFile(path, []byte("package xxxx\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
	}})
	report, err := inspector.Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonWorkspaceChanged)
}

func TestGitInspector_RejectsSuppressedTrackedDirt(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(strings.TrimPrefix(flag, "--"), func(t *testing.T) {
			root := newTestGitRepo(t)
			testGit(t, root, "update-index", flag, "main.go")
			writeTestFile(t, root, "main.go", "package changed\n")
			report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			assertHasCode(t, report, ReasonTrackedDirty)
			if report.Allowed {
				t.Fatal("suppressed tracked dirt was admitted")
			}
		})
	}
}

func TestGitInspector_LinkedWorktreeGitFileDoesNotSkipWorkspaceScan(t *testing.T) {
	main := newTestGitRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	testGit(t, main, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
	writeTestFile(t, linked, ".env", "client_"+"secret="+strings.Repeat("Ef7nOp2q", 4)+"\n")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: linked})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSecretPath)
	assertHasCode(t, report, ReasonSecretContent)
	assertReportBodyFree(t, report, "linked-worktree-secret")
}

func TestGitInspector_FailsClosedOnUnreadableSuspiciousFile(t *testing.T) {
	root := newTestGitRepo(t)
	path := filepath.Join(root, ".env.production")
	writeTestFile(t, root, ".env.production", "ordinary-but-sensitive-name\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSecretPath)
	assertHasCode(t, report, ReasonUnreadablePath)
	assertReportBodyFree(t, report, "ordinary-but-sensitive-name")
}

func TestGitInspector_FindsNestedGitAndGitmodules(t *testing.T) {
	root := newTestGitRepo(t)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("Mkdir nested: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested", ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir nested .git: %v", err)
	}
	writeTestFile(t, root, ".gitmodules", "[submodule \"example\"]\n\tpath = nested/example\n\turl = https://example.invalid/repository.git\n")
	testGit(t, root, "add", ".gitmodules")
	testGit(t, root, "commit", "--quiet", "-m", "boundary fixture")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonNestedGit)
	assertHasCode(t, report, ReasonGitmodules)
	assertReportBodyFree(t, report, "https://example.invalid/repository.git")
}

func TestGitInspector_FindsTrackedSymlink(t *testing.T) {
	root := newTestGitRepo(t)
	writeTestFile(t, root, "target.txt", "target\n")
	if err := os.Symlink("target.txt", filepath.Join(root, "tracked-link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	testGit(t, root, "add", "target.txt", "tracked-link.txt")
	testGit(t, root, "commit", "--quiet", "-m", "symlink fixture")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSymlink)
	assertReportBodyFree(t, report, "target\n")
}

func TestGitInspector_FindsTrackedSubmodule(t *testing.T) {
	root := newTestGitRepo(t)
	child := t.TempDir()
	testGit(t, child, "init", "--quiet")
	writeTestFile(t, child, "child.txt", "child\n")
	testGit(t, child, "add", "child.txt")
	testGit(t, child, "commit", "--quiet", "-m", "child fixture")

	testGit(t, root, "submodule", "add", child, "modules/child")
	testGit(t, root, "commit", "--quiet", "-m", "submodule fixture")

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSubmodule)
	assertHasCode(t, report, ReasonGitmodules)
}

func TestGitInspector_FindsUntrackedSymlinkEscapeAndNonregularPath(t *testing.T) {
	root := newTestGitRepo(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, filepath.Dir(outside), filepath.Base(outside), "outside secret body\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}

	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSymlink)
	assertHasCode(t, report, ReasonNonRegular)
	assertReportBodyFree(t, report, "outside secret body")
}

func TestGitInspector_FindsRelativeSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir workspace: %v", err)
	}
	testGit(t, root, "init", "--quiet")
	writeTestFile(t, root, "LICENSE", testMITLicense)
	writeTestFile(t, root, "main.go", "package main\n")
	testGit(t, root, "add", "LICENSE", "main.go")
	testGit(t, root, "commit", "--quiet", "-m", "initial fixture")
	writeTestFile(t, parent, "outside.txt", "outside relative secret\n")
	if err := os.Symlink("../outside.txt", filepath.Join(root, "relative-escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonSymlink)
	assertReportBodyFree(t, report, "outside relative secret")
}

func TestGitInspector_RejectsHardlinkedRegularFile(t *testing.T) {
	root := newTestGitRepo(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "hardlink.txt")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	testGit(t, root, "add", "hardlink.txt")
	testGit(t, root, "commit", "--quiet", "-m", "hardlink fixture")
	report, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonUnsafePath)
}

func TestStableFileReadsRejectHardlinkAddedDuringRead(t *testing.T) {
	for _, tt := range []struct {
		name string
		read func(*os.Root, string, func()) (snapshotRecord, int64, error)
	}{
		{name: "tracked", read: inspectTrackedFileWithHook},
		{name: "loose", read: inspectSnapshotFileWithHook},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file.txt")
			if err := os.WriteFile(path, []byte("ordinary source\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			link := filepath.Join(t.TempDir(), "outside-link.txt")
			_, _, err = tt.read(root, "file.txt", func() {
				if linkErr := os.Link(path, link); linkErr != nil {
					t.Skipf("hardlinks unavailable: %v", linkErr)
				}
			})
			if !errors.Is(err, errChanged) {
				t.Fatalf("stable read error = %v, want changed", err)
			}
		})
	}
}

func TestGitInspector_DetectsMutationAfterSnapshot(t *testing.T) {
	root := newTestGitRepo(t)
	mutated := false
	inspector := NewGitInspector(Options{AfterSnapshot: func() {
		mutated = true
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// changed after snapshot\n"), 0o644); err != nil {
			t.Fatalf("mutate snapshot fixture: %v", err)
		}
	}})

	report, err := inspector.Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !mutated {
		t.Fatal("AfterSnapshot hook was not called")
	}
	assertHasCode(t, report, ReasonWorkspaceChanged)
	if report.Allowed {
		t.Fatalf("report = %+v, want disallowed", report)
	}
}

func TestGitInspector_DetectsLooseContentMutationAtStablePath(t *testing.T) {
	root := newTestGitRepo(t)
	writeTestFile(t, root, "notes.txt", "before snapshot\n")
	inspector := NewGitInspector(Options{AfterSnapshot: func() {
		if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("after snapshot!\n"), 0o644); err != nil {
			t.Fatalf("mutate loose snapshot fixture: %v", err)
		}
	}})
	report, err := inspector.Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	assertHasCode(t, report, ReasonWorkspaceChanged)
}

func TestCheckDiagnostics_ReportsOnlyEnabledNetworkFlags(t *testing.T) {
	tests := []struct {
		name   string
		policy DiagnosticsPolicy
		codes  []ReasonCode
	}{
		{name: "disabled", codes: nil},
		{name: "network logs", policy: DiagnosticsPolicy{NetworkLogsEnabled: true}, codes: []ReasonCode{ReasonNetworkLogging}},
		{name: "telemetry payloads", policy: DiagnosticsPolicy{TelemetryPayloadsOverNetwork: true}, codes: []ReasonCode{ReasonTelemetryPayloads}},
		{name: "both", policy: DiagnosticsPolicy{NetworkLogsEnabled: true, TelemetryPayloadsOverNetwork: true}, codes: []ReasonCode{ReasonNetworkLogging, ReasonTelemetryPayloads}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := CheckDiagnostics(tt.policy)
			if len(findings) != len(tt.codes) {
				t.Fatalf("CheckDiagnostics(%+v) = %+v, want %v", tt.policy, findings, tt.codes)
			}
			for idx, code := range tt.codes {
				if findings[idx].Code != code || findings[idx].Label != "" {
					t.Fatalf("finding[%d] = %+v, want code %q and empty label", idx, findings[idx], code)
				}
			}
		})
	}
}

func TestGitInspector_BoundsFindingsAndNeverReturnsSnapshotBody(t *testing.T) {
	var report Report
	collector := findingCollector{report: &report}
	longLabel := strings.Repeat("x", MaxSafeLabelBytes+80)
	collector.add(ReasonUnsafePath, longLabel)
	for idx := 0; idx < MaxFindings+8; idx++ {
		collector.add(ReasonUnreadablePath, fmt.Sprintf("path-%03d", idx))
	}
	finished := collector.finish()
	if len(finished.Findings) != MaxFindings {
		t.Fatalf("finding count = %d, want bound %d", len(finished.Findings), MaxFindings)
	}
	assertHasCode(t, finished, ReasonCapacity)
	for _, finding := range finished.Findings {
		if len(finding.Label) > MaxSafeLabelBytes {
			t.Fatalf("finding label length = %d, want <= %d: %+v", len(finding.Label), MaxSafeLabelBytes, finding)
		}
	}

	root := newTestGitRepo(t)
	secretBody := "authorization: " + "bearer " + strings.Repeat("Gh9rSt4u", 4)
	writeTestFile(t, root, ".env", secretBody+"\n")
	writeTestFile(t, root, "oversized.txt", strings.Repeat("z", MaxSnapshotFileBytes+1))
	inspected, err := NewGitInspector(Options{}).Inspect(context.Background(), Request{Root: root})
	if err != nil {
		t.Fatalf("Inspect oversized fixture: %v", err)
	}
	assertHasCode(t, inspected, ReasonSecretContent)
	assertHasCode(t, inspected, ReasonCapacity)
	assertReportBodyFree(t, inspected, secretBody, "body-that-must-not-be-returned")
}

func TestReportValidate_RejectsAggregateEntryCountOverflow(t *testing.T) {
	report := Report{
		Allowed: false,
		Evidence: Evidence{
			Schema:         EvidenceSchema,
			TrackedFiles:   MaxWorkspaceEntries,
			UntrackedFiles: 1,
		},
		Findings: []Finding{{Code: ReasonCapacity, Label: "entries"}},
	}
	if err := report.Validate(); err == nil {
		t.Fatal("aggregate evidence count above workspace cap was accepted")
	}
	report.Evidence.TrackedFiles = MaxWorkspaceEntries
	report.Evidence.UntrackedFiles = 0
	report.Evidence.IgnoredFiles = 1
	if err := report.Validate(); err == nil {
		t.Fatal("aggregate ignored-file count above workspace cap was accepted")
	}
}

type fixedGitRunner struct {
	top   string
	calls int
}

func (r *fixedGitRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	return []byte(r.top + "\n"), nil
}

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init", "--quiet")
	writeTestFile(t, root, "LICENSE", testMITLicense)
	writeTestFile(t, root, "main.go", "package main\n")
	testGit(t, root, "add", "LICENSE", "main.go")
	testGit(t, root, "commit", "--quiet", "-m", "initial fixture")
	return root
}

func writeTestFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", filepath.Join(directory, name), err)
	}
}

func testGit(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	gitArgs := []string{
		"-c", "user.name=Buckley Test",
		"-c", "user.email=buckley-test@example.invalid",
		"-c", "protocol.file.allow=always",
		"-C", root,
	}
	gitArgs = append(gitArgs, args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/nonexistent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func assertHasCode(t *testing.T, report Report, want ReasonCode) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == want {
			return
		}
	}
	t.Fatalf("report = %+v, missing finding code %q", report, want)
}

func assertReportBodyFree(t *testing.T, report Report, bodies ...string) {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal report: %v", err)
	}
	for _, body := range bodies {
		if bytes.Contains(encoded, []byte(body)) {
			t.Fatalf("report JSON contains forbidden body %q: %s", body, encoded)
		}
	}
}
