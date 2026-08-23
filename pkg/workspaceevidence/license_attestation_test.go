package workspaceevidence

import (
	"bytes"
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
	"testing"
	"time"
)

func TestInspectRootLicenseBlob_DetectsConservativeSPDXHints(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		content  string
		spdxHint string
	}{
		{name: "MIT", path: "LICENSE", content: mitTestLicense(), spdxHint: "MIT"},
		{name: "Apache-2.0", path: "LICENSE.txt", content: apache20CanonicalText, spdxHint: "Apache-2.0"},
		{name: "BSD-2-Clause", path: "COPYING", content: bsd2TestLicense(), spdxHint: "BSD-2-Clause"},
		{name: "BSD-3-Clause", path: "LICENCE.md", content: bsd3TestLicense(), spdxHint: "BSD-3-Clause"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newLicenseTestRepo(t)
			writeLicenseTestFile(t, root, tt.path, tt.content)
			commit := commitLicenseTestRepo(t, root, "add root license")
			got, err := InspectRootLicenseBlob(t.Context(), root, commit)
			if err != nil {
				t.Fatalf("InspectRootLicenseBlob: %v", err)
			}
			if got.repository.root != root {
				t.Fatalf("internal canonical root = %q, want %q", got.repository.root, root)
			}
			if got.CommitOID() != commit {
				t.Fatalf("commit = %q, want %q", got.CommitOID(), commit)
			}
			if got.licensePath != tt.path {
				t.Fatalf("path = %q, want %q", got.licensePath, tt.path)
			}
			if got.DetectedSPDXHint() != tt.spdxHint {
				t.Fatalf("SPDX hint = %q, want %q", got.DetectedSPDXHint(), tt.spdxHint)
			}
			if got.hintVersion != SPDXHintClassifierVersion {
				t.Fatalf("hint classifier = %q, want %q", got.hintVersion, SPDXHintClassifierVersion)
			}
			wantBlob := gitLicenseTestOutput(t, root, "rev-parse", commit+":"+tt.path)
			if got.blobOID != wantBlob {
				t.Fatalf("blob = %q, want %q", got.blobOID, wantBlob)
			}
			sum := sha256.Sum256([]byte(tt.content))
			if got.contentSHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("content SHA-256 = %q, want %x", got.contentSHA256, sum)
			}
			if len(got.localBinding) != sha256.Size*2 || !isLowerHex(got.localBinding) {
				t.Fatalf("local binding is not lowercase SHA-256: %q", got.localBinding)
			}
			if len(got.repositoryID) != sha256.Size*2 || !isLowerHex(got.repositoryID) {
				t.Fatalf("private repository ID is not lowercase SHA-256: %q", got.repositoryID)
			}
			if err := got.Revalidate(t.Context()); err != nil {
				t.Fatalf("Revalidate: %v", err)
			}
			again, err := InspectRootLicenseBlob(t.Context(), root, commit)
			if err != nil {
				t.Fatalf("second InspectRootLicenseBlob: %v", err)
			}
			if again.localBinding != got.localBinding {
				t.Fatalf("local binding changed: %q != %q", again.localBinding, got.localBinding)
			}
		})
	}
}

func TestInspectRootLicenseBlob_HandlesSHA256ObjectFormat(t *testing.T) {
	root := newSHA256LicenseTestRepo(t)
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	commit := commitLicenseTestRepo(t, root, "licensed")
	if len(commit) != sha256.Size*2 {
		t.Fatalf("commit OID length = %d, want %d", len(commit), sha256.Size*2)
	}
	got, err := InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitOID() != commit || got.DetectedSPDXHint() != "MIT" {
		t.Fatalf("evidence = commit %q SPDX hint %q", got.CommitOID(), got.DetectedSPDXHint())
	}
}

func TestInspectRootLicenseBlob_DetectsPinnedTQWebPLicenseHint(t *testing.T) {
	content := mitTestLicenseForHolder("Oscar Vcencio / M31 Labs")
	if len(content) != 1081 {
		t.Fatalf("fixture length = %d, want 1081", len(content))
	}
	sum := sha256.Sum256([]byte(content))
	if got := hex.EncodeToString(sum[:]); got != "a4b901ff142bf983bd92a07ccc2f706b2c47a5b66a78a4ef9c319df29e18d8c8" {
		t.Fatalf("fixture SHA-256 = %s", got)
	}
	if content[len(content)-1] != '\n' {
		t.Fatal("fixture must retain its final newline")
	}

	root := newLicenseTestRepo(t)
	writeLicenseTestFile(t, root, "LICENSE", content)
	commit := commitLicenseTestRepo(t, root, "tqwebp license fixture")
	got, err := InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.DetectedSPDXHint() != "MIT" {
		t.Fatalf("SPDX hint = %q, want MIT", got.DetectedSPDXHint())
	}
}

func TestInspectRootLicenseBlob_UsesExactCommitObjectsNotWorktree(t *testing.T) {
	t.Run("modified tracked and extra untracked candidates are ignored", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "MIT base")
		writeLicenseTestFile(t, root, "LICENSE", "All rights reserved. No permission is granted.")
		writeLicenseTestFile(t, root, "COPYING", apache20CanonicalText)
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatalf("InspectRootLicenseBlob exact commit: %v", err)
		}
		if got.DetectedSPDXHint() != "MIT" || got.licensePath != "LICENSE" {
			t.Fatalf("evidence used worktree state: SPDX hint=%q path=%q", got.DetectedSPDXHint(), got.licensePath)
		}
	})
	t.Run("model-added untracked license cannot qualify an unlicensed commit", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "README.md", "no committed license\n")
		commit := commitLicenseTestRepo(t, root, "unlicensed base")
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrLicenseNotFound) {
			t.Fatalf("error = %v, want ErrLicenseNotFound", err)
		}
	})
	t.Run("worktree replacement cannot convert proprietary commit", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		proprietary := "Copyright 2026 Example. All rights reserved.\n"
		writeLicenseTestFile(t, root, "LICENSE", proprietary)
		commit := commitLicenseTestRepo(t, root, "proprietary base")
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		if got.DetectedSPDXHint() != "" {
			t.Fatalf("proprietary commit received SPDX hint %q", got.DetectedSPDXHint())
		}
		wantHash := sha256.Sum256([]byte(proprietary))
		if got.contentSHA256 != hex.EncodeToString(wantHash[:]) {
			t.Fatal("evidence did not bind the exact proprietary commit content")
		}
	})
}

func TestInspectRootLicenseBlob_RejectsNonRootAndNonRegularCandidates(t *testing.T) {
	t.Run("nested license", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, filepath.Join("docs", "LICENSE"), mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "nested license")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrLicenseNotFound) {
			t.Fatalf("error = %v, want ErrLicenseNotFound", err)
		}
	})
	t.Run("symlink license", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "terms.txt", mitTestLicense())
		if err := os.Symlink("terms.txt", filepath.Join(root, "LICENSE")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		commit := commitLicenseTestRepo(t, root, "symlink license")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrInvalidLicenseEntry) {
			t.Fatalf("error = %v, want ErrInvalidLicenseEntry", err)
		}
	})
	t.Run("tree named license", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, filepath.Join("LICENSE", "text"), mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "license tree")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrInvalidLicenseEntry) {
			t.Fatalf("error = %v, want ErrInvalidLicenseEntry", err)
		}
	})
	t.Run("gitlink named license", func(t *testing.T) {
		linked := newLicenseTestRepo(t)
		writeLicenseTestFile(t, linked, "README.md", "linked repository\n")
		linkedCommit := commitLicenseTestRepo(t, linked, "linked base")
		root := newLicenseTestRepo(t)
		gitLicenseTestRun(t, root, "update-index", "--add", "--cacheinfo", "160000,"+linkedCommit+",LICENSE")
		commit := commitLicenseTestIndex(t, root, "license gitlink")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrInvalidLicenseEntry) {
			t.Fatalf("error = %v, want ErrInvalidLicenseEntry", err)
		}
	})
}

func TestInspectRootLicenseBlob_RejectsAmbiguityAndLeavesUnknownHintsEmpty(t *testing.T) {
	t.Run("ambiguous root candidates", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		writeLicenseTestFile(t, root, "COPYING", apache20CanonicalText)
		commit := commitLicenseTestRepo(t, root, "ambiguous licenses")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrLicenseAmbiguous) {
			t.Fatalf("error = %v, want ErrLicenseAmbiguous", err)
		}
	})
	tests := []struct {
		name    string
		content string
	}{
		{name: "proprietary", content: "Copyright 2026 Example. All rights reserved.\n"},
		{name: "truncated MIT", content: "MIT License\n\nCopyright (c) 2026 Example\n\nPermission is hereby granted.\n"},
		{name: "extra MIT restriction", content: mitTestLicense() + "\nCommercial use requires written permission.\n"},
		{name: "restriction hidden in copyright line", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Buckley Test. All rights reserved.", 1)},
		{name: "all-rights identity phrase", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Acme All Rights Reserved.", 1)},
		{name: "commercial-use identity phrase", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Acme Commercial Use Prohibited", 1)},
		{name: "separate license hidden in copyright line", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Buckley Test — commercial exploitation requires a separate license", 1)},
		{name: "field restriction hidden after semicolon", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Buckley Test; military use requires approval", 1)},
		{name: "second legal sentence in copyright line", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Buckley Test. Redistribution requires approval.", 1)},
		{name: "multiple copyright notices", content: strings.Replace(mitTestLicense(), "Copyright (c) 2026 Buckley Test", "Copyright (c) 2026 Buckley Test\nCopyright (c) 2025 Other Holder", 1)},
		{name: "missing exact title", content: strings.TrimPrefix(mitTestLicense(), "MIT License\n\n")},
		{name: "truncated Apache", content: apache20CanonicalText[:len(apache20CanonicalText)/2]},
		{name: "BSD clause removed", content: strings.Replace(bsd3TestLicense(), "3. Neither the name", "Neither the name", 1)},
		{name: "markdown wrapped MIT", content: "\x60\x60\x60text\n" + mitTestLicense() + "\x60\x60\x60\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newLicenseTestRepo(t)
			writeLicenseTestFile(t, root, "LICENSE", tt.content)
			commit := commitLicenseTestRepo(t, root, "invalid license")
			got, err := InspectRootLicenseBlob(t.Context(), root, commit)
			if err != nil {
				t.Fatalf("exact evidence inspection failed: %v", err)
			}
			if got.DetectedSPDXHint() != "" {
				t.Fatalf("altered or unknown text received SPDX hint %q", got.DetectedSPDXHint())
			}
			if got.rootTreeOID == "" || got.blobOID == "" || got.contentSHA256 == "" {
				t.Fatal("dormant exact evidence facts are missing")
			}
		})
	}
	t.Run("lowercase candidate is not silently accepted", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "license", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "lowercase candidate")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrLicenseNotFound) {
			t.Fatalf("error = %v, want ErrLicenseNotFound", err)
		}
	})
}

func TestValidCopyrightNotice_ConservativeGrammar(t *testing.T) {
	valid := []string{
		"Copyright (c) 2026 Buckley Test",
		"Copyright © 2020-2026 M31 Labs",
		"Copyright (c) 2026 Oscar Vcencio / M31 Labs",
		"Copyright 2026 The Go Authors",
		"Copyright 2026 O'Donoghue Foundation",
		"Copyright 2026 Example Inc.",
	}
	for _, notice := range valid {
		if !validCopyrightNotice(notice) {
			t.Errorf("valid notice rejected: %q", notice)
		}
	}
	invalid := []string{
		"Copyright (c) 2026 Acme All Rights Reserved.",
		"Copyright (c) 2026 Acme Commercial Use Prohibited",
		"Copyright (c) 2026 Acme Personal Use Only",
		"Copyright (c) 2026 Acme Redistribution Subject to Written Approval",
		"Copyright (c) 2026 Acme Source Closed",
		"Copyright (c) 2026 Acme RightsReserved",
		"Copyright (c) 2026 Acme Rіghts Reserved",
		"Copyright (c) 2026 Buckley Test; commercial use requires approval",
		"Copyright (c) 2026 Buckley Test — military use prohibited",
		"Copyright (c) 2026 Buckley Test. Redistribution requires approval.",
		"Copyright (c) 2026 Buckley Test. Redistribution Requires Approval.",
		"Copyright (c) 2026 buckley test",
		"Copyright (c) 2026-2020 Buckley Test",
		"Copyright (c) 2026 Buckley, Inc.",
		"Copyright (c) 2026 Oscar Vcencio.",
		"Copyright (c) 2026 Oscar Vcencio/M31 Labs",
		"Copyright (c) 2026 Buckley Test and",
	}
	for _, notice := range invalid {
		if validCopyrightNotice(notice) {
			t.Errorf("invalid notice accepted: %q", notice)
		}
	}
}

func TestInspectRootLicenseBlob_RequiresCanonicalRootAndExactCommitOID(t *testing.T) {
	root := newLicenseTestRepo(t)
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	commit := commitLicenseTestRepo(t, root, "licensed base")
	blob := gitLicenseTestOutput(t, root, "rev-parse", commit+":LICENSE")
	tree := gitLicenseTestOutput(t, root, "rev-parse", commit+"^{tree}")
	for _, oid := range []string{"HEAD", commit[:12], strings.ToUpper(commit), commit + "^{}", blob, tree} {
		name := oid
		if len(name) > 12 {
			name = name[:12]
		}
		t.Run("OID_"+name, func(t *testing.T) {
			_, err := InspectRootLicenseBlob(t.Context(), root, oid)
			if !errors.Is(err, ErrInvalidCommitOID) {
				t.Fatalf("OID %q error = %v, want ErrInvalidCommitOID", oid, err)
			}
		})
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "repo-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for i, invalidRoot := range []string{filepath.Base(root), root + string(filepath.Separator), nested, alias} {
		t.Run("root_"+string(rune('a'+i)), func(t *testing.T) {
			_, err := InspectRootLicenseBlob(t.Context(), invalidRoot, commit)
			if !errors.Is(err, ErrInvalidRepositoryRoot) {
				t.Fatalf("root %q error = %v, want ErrInvalidRepositoryRoot", invalidRoot, err)
			}
		})
	}
}

func TestInspectRootLicenseBlob_DisablesGitReplacementObjects(t *testing.T) {
	root := newLicenseTestRepo(t)
	proprietary := "Copyright 2026 Example. All rights reserved.\n"
	writeLicenseTestFile(t, root, "LICENSE", proprietary)
	proprietaryCommit := commitLicenseTestRepo(t, root, "proprietary")
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	mitCommit := commitLicenseTestRepo(t, root, "MIT")
	gitLicenseTestRun(t, root, "replace", proprietaryCommit, mitCommit)
	got, err := InspectRootLicenseBlob(t.Context(), root, proprietaryCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got.DetectedSPDXHint() != "" {
		t.Fatalf("replacement object supplied SPDX hint %q", got.DetectedSPDXHint())
	}
	wantHash := sha256.Sum256([]byte(proprietary))
	if got.contentSHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatal("evidence did not bind the unreplaced proprietary content")
	}
}

func TestInspectRootLicenseBlob_DisablesPromisorLazyFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local ext transport helper fixture is POSIX-only")
	}
	root := newLicenseTestRepo(t)
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	commit := commitLicenseTestRepo(t, root, "licensed")
	blob := gitLicenseTestOutput(t, root, "rev-parse", commit+":LICENSE")

	helperDir := t.TempDir()
	marker := filepath.Join(helperDir, "invoked")
	helper := filepath.Join(helperDir, "fake-promisor")
	helperSource := "#!/bin/sh\n: > " + shellSingleQuote(marker) + "\nexit 97\n"
	if err := os.WriteFile(helper, []byte(helperSource), 0o755); err != nil {
		t.Fatal(err)
	}
	gitLicenseTestRun(t, root, "config", "core.repositoryFormatVersion", "1")
	gitLicenseTestRun(t, root, "config", "extensions.partialClone", "origin")
	gitLicenseTestRun(t, root, "config", "remote.origin.promisor", "true")
	gitLicenseTestRun(t, root, "config", "remote.origin.partialCloneFilter", "blob:none")
	gitLicenseTestRun(t, root, "config", "remote.origin.url", "ext::"+helper)
	gitLicenseTestRun(t, root, "config", "protocol.ext.allow", "always")
	if err := os.Remove(looseGitObjectPath(root, blob)); err != nil {
		t.Fatal(err)
	}

	control := exec.CommandContext(t.Context(), "git", "--no-pager", "cat-file", "blob", blob)
	control.Dir = root
	control.Env = licenseTestGitEnv()
	if output, err := control.CombinedOutput(); err == nil {
		t.Fatalf("missing-object control unexpectedly succeeded: %s", output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control did not invoke fake promisor helper: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectRootLicenseBlob(t.Context(), root, commit); err == nil {
		t.Fatal("inspection unexpectedly succeeded with a missing promised blob")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection invoked promisor helper despite GIT_NO_LAZY_FETCH=1: %v", err)
	}
}

func TestInspectRootLicenseBlob_VerifiesRawCommitAndRootTreeIdentities(t *testing.T) {
	t.Run("commit object content cannot be substituted under a pinned OID", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "README.md", "unlicensed base\n")
		unlicensedCommit := commitLicenseTestRepo(t, root, "unlicensed")
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		licensedCommit := commitLicenseTestRepo(t, root, "licensed")
		overwriteLooseGitObject(t, root, unlicensedCommit, licensedCommit)

		_, err := InspectRootLicenseBlob(t.Context(), root, unlicensedCommit)
		if !errors.Is(err, ErrInvalidCommitOID) {
			t.Fatalf("error = %v, want ErrInvalidCommitOID", err)
		}
	})

	t.Run("root tree content cannot be substituted under a pinned OID", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", "Copyright 2026 Example. All rights reserved.\n")
		proprietaryCommit := commitLicenseTestRepo(t, root, "proprietary")
		proprietaryTree := gitLicenseTestOutput(t, root, "rev-parse", proprietaryCommit+"^{tree}")
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		licensedCommit := commitLicenseTestRepo(t, root, "licensed")
		licensedTree := gitLicenseTestOutput(t, root, "rev-parse", licensedCommit+"^{tree}")
		overwriteLooseGitObject(t, root, proprietaryTree, licensedTree)

		_, err := InspectRootLicenseBlob(t.Context(), root, proprietaryCommit)
		if !errors.Is(err, ErrInvalidLicenseEntry) {
			t.Fatalf("error = %v, want ErrInvalidLicenseEntry", err)
		}
	})
}

func TestRootLicenseBlobEvidence_RevalidateBindsRepositoryAndObjects(t *testing.T) {
	t.Run("zero and mutated evidence values are invalid", func(t *testing.T) {
		var zero RootLicenseBlobEvidence
		if err := zero.Revalidate(t.Context()); !errors.Is(err, ErrEvidenceInvalid) {
			t.Fatalf("zero error = %v, want ErrEvidenceInvalid", err)
		}
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "licensed")
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		got.detectedSPDXHint = "Apache-2.0"
		if err := got.Revalidate(t.Context()); !errors.Is(err, ErrEvidenceInvalid) {
			t.Fatalf("mutated error = %v, want ErrEvidenceInvalid", err)
		}
	})

	t.Run("unchanged linked worktree remains valid", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "licensed")
		linkedParent := t.TempDir()
		linked := filepath.Join(linkedParent, "linked")
		gitLicenseTestRun(t, root, "worktree", "add", "--quiet", "--detach", linked, commit)
		resolved, err := filepath.EvalSymlinks(linked)
		if err != nil {
			t.Fatal(err)
		}
		linked = filepath.Clean(resolved)

		got, err := InspectRootLicenseBlob(t.Context(), linked, commit)
		if err != nil {
			t.Fatal(err)
		}
		if got.repository.gitDir == got.repository.commonGitDir {
			t.Fatal("linked worktree Git dir was not distinguished from common Git dir")
		}
		if err := got.Revalidate(t.Context()); err != nil {
			t.Fatalf("Revalidate: %v", err)
		}
	})

	t.Run("Git directory reassociation is stale", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "licensed")
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		gitDir := filepath.Join(root, ".git")
		if err := os.Rename(gitDir, filepath.Join(root, ".git-original")); err != nil {
			t.Fatal(err)
		}
		gitLicenseTestRun(t, root, "init", "--quiet")
		if err := got.Revalidate(t.Context()); !errors.Is(err, ErrEvidenceStale) {
			t.Fatalf("error = %v, want ErrEvidenceStale", err)
		}
	})

	t.Run("license object substitution is stale", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "licensed")
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		writeLicenseTestFile(t, root, "LICENSE", "Copyright 2026 Example. All rights reserved.\n")
		proprietaryCommit := commitLicenseTestRepo(t, root, "proprietary")
		proprietaryBlob := gitLicenseTestOutput(t, root, "rev-parse", proprietaryCommit+":LICENSE")
		overwriteLooseGitObject(t, root, got.blobOID, proprietaryBlob)
		if err := got.Revalidate(t.Context()); !errors.Is(err, ErrEvidenceStale) {
			t.Fatalf("error = %v, want ErrEvidenceStale", err)
		}
	})
}

func TestRootLicenseBlobEvidence_IsLocalOnlyAndRedactsHostPaths(t *testing.T) {
	root := newLicenseTestRepo(t)
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	commit := commitLicenseTestRepo(t, root, "licensed")
	got, err := InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{
		fmt.Sprintf("%v", got),
		fmt.Sprintf("%+v", got),
		fmt.Sprintf("%#v", got),
		fmt.Sprintf("%+v", &got),
		fmt.Sprintf("%#v", &got),
	} {
		if strings.Contains(formatted, root) || strings.Contains(formatted, got.licensePath) {
			t.Fatalf("formatted evidence leaked local path: %q", formatted)
		}
	}
	encoded, err := json.Marshal(got)
	if !errors.Is(err, ErrEvidenceLocalOnly) {
		t.Fatalf("JSON marshal error = %v, want ErrEvidenceLocalOnly", err)
	}
	if len(encoded) != 0 || strings.Contains(err.Error(), root) {
		t.Fatalf("JSON marshal returned data or leaked path: data=%q error=%q", encoded, err)
	}
	var restored RootLicenseBlobEvidence
	if err := json.Unmarshal([]byte(`{}`), &restored); !errors.Is(err, ErrEvidenceLocalOnly) {
		t.Fatalf("JSON unmarshal error = %v, want ErrEvidenceLocalOnly; resume must reinspect", err)
	}
	if restored.localBinding != "" {
		t.Fatal("failed deserialize mutated destination evidence")
	}
	typeOfEvidence := reflect.TypeOf(got)
	for _, name := range []string{"CanonicalRoot", "LicensePath"} {
		if _, exists := typeOfEvidence.MethodByName(name); exists {
			t.Fatalf("path accessor %s must not be exported", name)
		}
	}
	_, err = boundedGitOutput(t.Context(), root, maxGitMetadataBytes, "cat-file", "blob", strings.Repeat("0", 40))
	if err == nil {
		t.Fatal("missing object unexpectedly succeeded")
	}
	if !errors.Is(err, ErrGitCommandFailed) {
		t.Fatalf("Git error = %v, want ErrGitCommandFailed", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("Git error leaked repository root: %q", err)
	}
	_, err = InspectRootLicenseBlob(t.Context(), filepath.Join(root, "missing"), commit)
	if !errors.Is(err, ErrInvalidRepositoryRoot) {
		t.Fatalf("invalid-root error = %v, want ErrInvalidRepositoryRoot", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("repository error leaked repository root: %q", err)
	}
}

func TestRootLicenseBlobEvidence_ExportsNoAuthorityOrPathBindingSurface(t *testing.T) {
	assertMethods := func(t *testing.T, typ reflect.Type, allowed map[string]bool) {
		t.Helper()
		for i := 0; i < typ.NumMethod(); i++ {
			name := typ.Method(i).Name
			if !allowed[name] {
				t.Fatalf("unexpected exported evidence method %s could expand authority or disclosure surface", name)
			}
			delete(allowed, name)
		}
		for missing := range allowed {
			t.Errorf("expected evidence method %s is missing", missing)
		}
	}

	assertMethods(t, reflect.TypeOf(RootLicenseBlobEvidence{}), map[string]bool{
		"CommitOID":        true,
		"DetectedSPDXHint": true,
		"GoString":         true,
		"MarshalJSON":      true,
		"Revalidate":       true,
		"String":           true,
	})
	assertMethods(t, reflect.TypeOf(&RootLicenseBlobEvidence{}), map[string]bool{
		"CommitOID":        true,
		"DetectedSPDXHint": true,
		"GoString":         true,
		"MarshalJSON":      true,
		"Revalidate":       true,
		"String":           true,
		"UnmarshalJSON":    true,
	})
}

func TestBoundedEvidenceContext_AddsDeadline(t *testing.T) {
	ctx, cancel, err := boundedEvidenceContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("bounded context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > evidenceInspectionTimeout {
		t.Fatalf("deadline remaining = %v, want (0, %v]", remaining, evidenceInspectionTimeout)
	}
	if _, _, err := boundedEvidenceContext(nil); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("nil context error = %v, want ErrEvidenceInvalid", err)
	}
}

func TestBoundedGitOutput_UsesBoundTrustedExecutable(t *testing.T) {
	root := newLicenseTestRepo(t)
	t.Setenv("PATH", t.TempDir())
	output, err := boundedGitOutput(t.Context(), root, maxGitMetadataBytes, "rev-parse", "--show-object-format")
	if err != nil {
		t.Fatalf("bounded Git ignored its host-bound executable: %v", err)
	}
	format, err := singleGitLine(output)
	if err != nil || (format != "sha1" && format != "sha256") {
		t.Fatalf("object format = %q, error = %v", format, err)
	}
}

func TestGitExecutableIdentity_BindsContentAndBoundsSize(t *testing.T) {
	t.Run("same-inode content mutation is rejected", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "fake-git")
		original := []byte("#!/bin/sh\nexit 0\n")
		mutated := []byte("#!/bin/sh\nexit 9\n")
		if len(mutated) != len(original) {
			t.Fatal("test fixture mutation must preserve size")
		}
		if err := os.WriteFile(target, original, 0o700); err != nil {
			t.Fatal(err)
		}
		identity := captureGitExecutableAt(target)
		if identity.err != nil {
			t.Fatalf("capture controlled executable: %v", identity.err)
		}
		if want := sha256.Sum256(original); identity.contentSHA256 != want {
			t.Fatal("controlled executable content digest was not captured")
		}
		if _, err := revalidateGitExecutable(identity); err != nil {
			t.Fatalf("revalidate unchanged controlled executable: %v", err)
		}
		before, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(mutated); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() {
			t.Fatal("controlled mutation did not preserve filesystem identity and size")
		}
		if _, err := revalidateGitExecutable(identity); !errors.Is(err, ErrGitCommandFailed) {
			t.Fatalf("mutated executable error = %v, want ErrGitCommandFailed", err)
		}
	})

	t.Run("oversized executable is rejected before hashing", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "oversized-git")
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o700)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxTrustedGitExecutableSize + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if identity := captureGitExecutableAt(target); !errors.Is(identity.err, ErrGitCommandFailed) {
			t.Fatalf("oversized executable error = %v, want ErrGitCommandFailed", identity.err)
		}
	})
}

func TestInspectRootLicenseBlob_BoundsBlobAndBindsEveryLocalField(t *testing.T) {
	t.Run("oversized blob", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestBytes(t, root, "LICENSE", bytes.Repeat([]byte{'x'}, MaxLicenseBlobBytes+1))
		commit := commitLicenseTestRepo(t, root, "oversized license")
		_, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if !errors.Is(err, ErrLicenseTooLarge) {
			t.Fatalf("error = %v, want ErrLicenseTooLarge", err)
		}
	})
	t.Run("local binding fields", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		commit := commitLicenseTestRepo(t, root, "licensed base")
		got, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		base := got.localBinding
		mutations := []func(*RootLicenseBlobEvidence){
			func(e *RootLicenseBlobEvidence) { e.repository.root += "-other" },
			func(e *RootLicenseBlobEvidence) { e.repository.gitDir += "-other" },
			func(e *RootLicenseBlobEvidence) { e.repository.commonGitDir += "-other" },
			func(e *RootLicenseBlobEvidence) { e.repositoryID = strings.Repeat("3", len(e.repositoryID)) },
			func(e *RootLicenseBlobEvidence) { e.commitOID = strings.Repeat("0", len(e.commitOID)) },
			func(e *RootLicenseBlobEvidence) { e.rootTreeOID = strings.Repeat("4", len(e.rootTreeOID)) },
			func(e *RootLicenseBlobEvidence) { e.licensePath = "COPYING" },
			func(e *RootLicenseBlobEvidence) { e.blobOID = strings.Repeat("1", len(e.blobOID)) },
			func(e *RootLicenseBlobEvidence) { e.contentSHA256 = strings.Repeat("2", len(e.contentSHA256)) },
			func(e *RootLicenseBlobEvidence) { e.detectedSPDXHint = "Apache-2.0" },
			func(e *RootLicenseBlobEvidence) { e.hintVersion += "-other" },
		}
		for i, mutate := range mutations {
			changed := got
			mutate(&changed)
			if digest := rootLicenseEvidenceBinding(changed); digest == base {
				t.Fatalf("mutation %d did not change local binding", i)
			}
		}
	})
}

func mitTestLicense() string {
	return mitTestLicenseForHolder("Buckley Test")
}

func mitTestLicenseForHolder(holder string) string {
	return "MIT License\n\nCopyright (c) 2026 " + holder + "\n\n" + strings.TrimSpace(mitCanonicalBody) + "\n"
}

func bsd2TestLicense() string {
	return "BSD 2-Clause License\n\nCopyright (c) 2026 Buckley Test\nAll rights reserved.\n\n" +
		strings.TrimSpace(bsd2CanonicalBody) + "\n"
}

func bsd3TestLicense() string {
	return "BSD 3-Clause License\n\nCopyright (c) 2026 Buckley Test\nAll rights reserved.\n\n" +
		strings.TrimSpace(bsd3CanonicalBody) + "\n"
}

func newLicenseTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Clean(resolved)
	gitLicenseTestRun(t, root, "init", "--quiet")
	return root
}

func newSHA256LicenseTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Clean(resolved)
	cmd := exec.CommandContext(t.Context(), "git", "--no-pager", "init", "--quiet", "--object-format=sha256")
	cmd.Dir = root
	cmd.Env = licenseTestGitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Git SHA-256 repositories unavailable: %v: %s", err, output)
	}
	return root
}

func writeLicenseTestFile(t *testing.T, root, path, content string) {
	t.Helper()
	writeLicenseTestBytes(t, root, path, []byte(content))
}

func writeLicenseTestBytes(t *testing.T, root, path string, content []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func overwriteLooseGitObject(t *testing.T, root, targetOID, sourceOID string) {
	t.Helper()
	source, err := os.ReadFile(looseGitObjectPath(root, sourceOID))
	if err != nil {
		t.Fatalf("read source loose object: %v", err)
	}
	target := looseGitObjectPath(root, targetOID)
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("make target loose object writable: %v", err)
	}
	if err := os.WriteFile(target, source, 0o444); err != nil {
		t.Fatalf("substitute loose object: %v", err)
	}
}

func looseGitObjectPath(root, oid string) string {
	return filepath.Join(root, ".git", "objects", oid[:2], oid[2:])
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func commitLicenseTestRepo(t *testing.T, root, message string) string {
	t.Helper()
	gitLicenseTestRun(t, root, "add", "-A")
	return commitLicenseTestIndex(t, root, message)
}

func commitLicenseTestIndex(t *testing.T, root, message string) string {
	t.Helper()
	gitLicenseTestRun(t, root, "-c", "user.name=Buckley Test", "-c", "user.email=buckley@example.invalid",
		"commit", "--quiet", "-m", message)
	return gitLicenseTestOutput(t, root, "rev-parse", "HEAD")
}

func gitLicenseTestRun(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = gitLicenseTestOutput(t, root, args...)
}

func gitLicenseTestOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = root
	cmd.Env = licenseTestGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func licenseTestGitEnv() []string {
	return []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
}
