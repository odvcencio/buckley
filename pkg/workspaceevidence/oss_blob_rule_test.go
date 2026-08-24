package workspaceevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMintTrackedPromptOSSBlobRule_AllowsOnlyVersionedCanonicalLicenses(t *testing.T) {
	tests := []struct {
		name        string
		licensePath string
		license     string
	}{
		{name: "Apache-2.0", licensePath: "LICENSE.txt", license: apache20CanonicalText},
		{name: "BSD-2-Clause", licensePath: "COPYING", license: bsd2TestLicense()},
		{name: "BSD-3-Clause", licensePath: "LICENCE.md", license: bsd3TestLicense()},
		{name: "MIT", licensePath: "LICENSE", license: mitTestLicense()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := []byte("Implement the bounded parser repair.\n")
			_, _, evidence := newOSSBlobRuleTestRepo(t, tt.licensePath, tt.license, ".buckley/ox-tasks/parser.md", prompt)

			rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, ".buckley/ox-tasks/parser.md")
			if err != nil {
				t.Fatalf("MintTrackedPromptOSSBlobRule() error = %v", err)
			}
			if !bytes.Equal(gotPrompt, prompt) {
				t.Fatalf("prompt = %q, want exact committed bytes %q", gotPrompt, prompt)
			}
			binding, err := rule.ClaimForDispatch(t.Context(), gotPrompt)
			if err != nil {
				t.Fatalf("ClaimForDispatch() error = %v", err)
			}
			if binding == ([32]byte{}) {
				t.Fatal("ClaimForDispatch() returned an empty binding")
			}
			if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); !errors.Is(err, ErrOSSBlobRuleSpent) {
				t.Fatalf("second ClaimForDispatch() error = %v, want ErrOSSBlobRuleSpent", err)
			}
		})
	}
}

func TestMintTrackedPromptOSSBlobRule_BindsSHA256Repository(t *testing.T) {
	root := newSHA256LicenseTestRepo(t)
	prompt := []byte("Repair the exact bounded slice.\n")
	writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
	writeLicenseTestBytes(t, root, "tasks/repair.md", prompt)
	commit := commitLicenseTestRepo(t, root, "licensed prompt")
	evidence, err := InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "tasks/repair.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); err != nil {
		t.Fatal(err)
	}
}

func TestMintTrackedPromptOSSBlobRule_RejectsUnknownLicense(t *testing.T) {
	_, _, evidence := newOSSBlobRuleTestRepo(
		t,
		"LICENSE",
		"Copyright 2026 Buckley Test. All rights reserved.\n",
		"task.md",
		[]byte("Do not dispatch this.\n"),
	)
	_, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if !errors.Is(err, ErrOSSBlobRuleDenied) {
		t.Fatalf("error = %v, want ErrOSSBlobRuleDenied", err)
	}
}

func TestMintTrackedPromptOSSBlobRule_TamperedHintCannotAuthorize(t *testing.T) {
	_, _, evidence := newOSSBlobRuleTestRepo(
		t,
		"LICENSE",
		"Copyright 2026 Buckley Test. All rights reserved.\n",
		"task.md",
		[]byte("Do not dispatch this.\n"),
	)
	// Even recomputing the private local binding cannot turn the diagnostic
	// hint into authority because revalidation compares exact fresh evidence.
	evidence.detectedSPDXHint = "MIT"
	evidence.localBinding = rootLicenseEvidenceBinding(evidence)
	if _, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md"); !errors.Is(err, ErrEvidenceStale) {
		t.Fatalf("error = %v, want ErrEvidenceStale", err)
	}
}

func TestEvaluateCanonicalOSSLicense_DoesNotUseEvidenceHint(t *testing.T) {
	if got, ok := evaluateCanonicalOSSLicense([]byte(mitTestLicense())); !ok || got != "MIT" {
		t.Fatalf("canonical MIT decision = %q, %v", got, ok)
	}
	if got, ok := evaluateCanonicalOSSLicense([]byte("not an OSS license\n")); ok || got != "" {
		t.Fatalf("unknown license decision = %q, %v", got, ok)
	}
}

func TestMintTrackedPromptOSSBlobRule_AllowsAppliedApache20License(t *testing.T) {
	const eosLicenseBlobOID = "97f06839de1194d942d90a956354c1d1f8d5111d"
	prompt := []byte("Repair strict Eos string escapes.\n")
	_, _, evidence := newOSSBlobRuleTestRepo(
		t,
		"LICENSE",
		eosApache20License(),
		"evidence/tasks/strict-string-escapes.md",
		prompt,
	)
	if evidence.blobOID != eosLicenseBlobOID {
		t.Fatalf("license blob OID = %s, want exact Eos blob %s", evidence.blobOID, eosLicenseBlobOID)
	}

	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "evidence/tasks/strict-string-escapes.md")
	if err != nil {
		t.Fatalf("MintTrackedPromptOSSBlobRule() error = %v", err)
	}
	if !bytes.Equal(gotPrompt, prompt) {
		t.Fatalf("prompt = %q, want exact committed bytes %q", gotPrompt, prompt)
	}
	if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); err != nil {
		t.Fatalf("ClaimForDispatch() error = %v", err)
	}
}

func TestEvaluateCanonicalOSSLicense_RejectsAlteredAppliedApache20License(t *testing.T) {
	license := eosApache20License()
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "mutated terms",
			content: strings.Replace(license, "shall mean the terms and conditions", "may mean the terms and conditions", 1),
		},
		{
			name:    "mutated boilerplate",
			content: strings.Replace(license, "distributed under the License", "distributed under this License", 1),
		},
		{
			name:    "proprietary copyright tail",
			content: strings.Replace(license, "Copyright 2026 Oscar Villavicencio", "Copyright 2026 Oscar Villavicencio. All rights reserved.", 1),
		},
		{
			name:    "arbitrary trailing restriction",
			content: license + "\nCommercial use requires written permission.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := evaluateCanonicalOSSLicense([]byte(tt.content)); ok || got != "" {
				t.Fatalf("altered Apache decision = %q, %v", got, ok)
			}
		})
	}
}

func TestMintTrackedPromptOSSBlobRule_RejectsUnboundPaths(t *testing.T) {
	root, _, evidence := newOSSBlobRuleTestRepo(
		t,
		"LICENSE",
		mitTestLicense(),
		"tasks/bound.md",
		[]byte("Bound prompt.\n"),
	)
	writeLicenseTestFile(t, root, "tasks/untracked.md", "Untracked prompt.\n")

	tests := []string{
		"",
		".",
		"../task.md",
		"tasks/../task.md",
		"/tmp/task.md",
		"tasks\\task.md",
		"tasks/untracked.md",
		"tasks/missing.md",
		strings.Repeat("x", maxOSSPromptPathBytes+1),
		strings.Repeat("x/", maxOSSPromptPathDepth) + "task.md",
	}
	for _, promptPath := range tests {
		t.Run(strings.ReplaceAll(promptPath, "/", "_"), func(t *testing.T) {
			_, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, promptPath)
			if !errors.Is(err, ErrOSSPromptInvalid) {
				t.Fatalf("path %q error = %v, want ErrOSSPromptInvalid", promptPath, err)
			}
		})
	}
}

func TestMintTrackedPromptOSSBlobRule_RejectsNonRegularPromptEntries(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		writeLicenseTestFile(t, root, "tasks/target.md", "Target prompt.\n")
		if err := os.Symlink("target.md", filepath.Join(root, "tasks", "link.md")); err != nil {
			t.Fatal(err)
		}
		commit := commitLicenseTestRepo(t, root, "symlink prompt")
		evidence, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = MintTrackedPromptOSSBlobRule(t.Context(), evidence, "tasks/link.md")
		if !errors.Is(err, ErrOSSPromptInvalid) {
			t.Fatalf("error = %v, want ErrOSSPromptInvalid", err)
		}
	})

	t.Run("tree", func(t *testing.T) {
		_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "tasks/prompt.md", []byte("Prompt.\n"))
		_, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "tasks")
		if !errors.Is(err, ErrOSSPromptInvalid) {
			t.Fatalf("error = %v, want ErrOSSPromptInvalid", err)
		}
	})

	t.Run("gitlink", func(t *testing.T) {
		root := newLicenseTestRepo(t)
		writeLicenseTestFile(t, root, "LICENSE", mitTestLicense())
		baseCommit := commitLicenseTestRepo(t, root, "licensed base")
		gitLicenseTestRun(t, root, "update-index", "--add", "--cacheinfo", "160000,"+baseCommit+",task-module")
		commit := commitLicenseTestIndex(t, root, "gitlink prompt")
		evidence, err := InspectRootLicenseBlob(t.Context(), root, commit)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task-module")
		if !errors.Is(err, ErrOSSPromptInvalid) {
			t.Fatalf("error = %v, want ErrOSSPromptInvalid", err)
		}
	})
}

func TestMintTrackedPromptOSSBlobRule_RejectsInvalidPromptContent(t *testing.T) {
	tests := []struct {
		name   string
		prompt []byte
	}{
		{name: "empty", prompt: nil},
		{name: "oversized", prompt: bytes.Repeat([]byte{'x'}, MaxOSSPromptBlobBytes+1)},
		{name: "invalid UTF-8", prompt: []byte{0xff}},
		{name: "NUL", prompt: []byte("prompt\x00bytes")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", tt.prompt)
			_, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
			if !errors.Is(err, ErrOSSPromptInvalid) {
				t.Fatalf("error = %v, want ErrOSSPromptInvalid", err)
			}
		})
	}
}

func TestOSSBlobRule_ClaimRejectsMismatchWithoutSpending(t *testing.T) {
	prompt := []byte("Exact prompt bytes.\n")
	_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	gotPrompt[0] ^= 0x20
	if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); !errors.Is(err, ErrOSSPromptMismatch) {
		t.Fatalf("mismatched claim error = %v, want ErrOSSPromptMismatch", err)
	}
	if _, err := rule.ClaimForDispatch(t.Context(), prompt); err != nil {
		t.Fatalf("exact claim after mismatch error = %v", err)
	}
}

func TestOSSBlobRule_ConcurrentClaimAllowsExactlyOneDispatch(t *testing.T) {
	prompt := []byte("One dispatch only.\n")
	_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := rule.ClaimForDispatch(t.Context(), gotPrompt)
			if err == nil {
				succeeded.Add(1)
				return
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	if got := succeeded.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
	for err := range errs {
		if !errors.Is(err, ErrOSSBlobRuleSpent) {
			t.Errorf("losing claim error = %v, want ErrOSSBlobRuleSpent", err)
		}
	}
}

func TestOSSBlobRule_SealRejectsCopiesAndMutation(t *testing.T) {
	prompt := []byte("Bound prompt.\n")
	_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	copied := *rule
	if _, err := copied.ClaimForDispatch(t.Context(), gotPrompt); !errors.Is(err, ErrOSSBlobRuleInvalid) {
		t.Fatalf("copied rule error = %v, want ErrOSSBlobRuleInvalid", err)
	}

	mutated, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	mutated.promptPath = "other.md"
	if _, err := mutated.ClaimForDispatch(t.Context(), gotPrompt); !errors.Is(err, ErrOSSBlobRuleInvalid) {
		t.Fatalf("mutated rule error = %v, want ErrOSSBlobRuleInvalid", err)
	}
}

func TestOSSBlobRule_BindingCoversEvidencePromptAndRunScope(t *testing.T) {
	prompt := []byte("Bound prompt.\n")
	_, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	base := ossBlobRuleBinding(rule)
	mutations := []struct {
		name   string
		mutate func(*OSSBlobRule)
	}{
		{name: "repository", mutate: func(r *OSSBlobRule) { r.evidence.repositoryID += "x" }},
		{name: "commit", mutate: func(r *OSSBlobRule) { r.evidence.commitOID = strings.Repeat("0", len(r.evidence.commitOID)) }},
		{name: "root tree", mutate: func(r *OSSBlobRule) { r.evidence.rootTreeOID = strings.Repeat("1", len(r.evidence.rootTreeOID)) }},
		{name: "license path", mutate: func(r *OSSBlobRule) { r.evidence.licensePath += ".txt" }},
		{name: "license blob", mutate: func(r *OSSBlobRule) { r.evidence.blobOID = strings.Repeat("2", len(r.evidence.blobOID)) }},
		{name: "license content", mutate: func(r *OSSBlobRule) { r.evidence.contentSHA256 = strings.Repeat("3", len(r.evidence.contentSHA256)) }},
		{name: "license hint", mutate: func(r *OSSBlobRule) { r.evidence.detectedSPDXHint = "Apache-2.0" }},
		{name: "classifier version", mutate: func(r *OSSBlobRule) { r.evidence.hintVersion += "x" }},
		{name: "license local binding", mutate: func(r *OSSBlobRule) { r.evidence.localBinding += "x" }},
		{name: "license rule version", mutate: func(r *OSSBlobRule) { r.licenseRuleVersion += "x" }},
		{name: "license id", mutate: func(r *OSSBlobRule) { r.licenseID = "Apache-2.0" }},
		{name: "prompt path", mutate: func(r *OSSBlobRule) { r.promptPath += ".txt" }},
		{name: "prompt mode", mutate: func(r *OSSBlobRule) { r.promptMode = "100755" }},
		{name: "prompt blob", mutate: func(r *OSSBlobRule) { r.promptBlobOID = strings.Repeat("4", len(r.promptBlobOID)) }},
		{name: "prompt content", mutate: func(r *OSSBlobRule) { r.promptContentSHA256[0] ^= 0xff }},
		{name: "run scope", mutate: func(r *OSSBlobRule) { r.runScope[0] ^= 0xff }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			changed := *rule
			tt.mutate(&changed)
			if got := ossBlobRuleBinding(&changed); got == base {
				t.Fatal("mutation did not change binding")
			}
		})
	}

	second, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	if second.binding == rule.binding {
		t.Fatal("fresh run scope did not change the binding")
	}
}

func TestOSSBlobRule_IsLocalOnlyRedactedAndNarrow(t *testing.T) {
	prompt := []byte("Bound prompt.\n")
	root, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, _, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := rule.String(); strings.Contains(got, root) || strings.Contains(got, "task.md") {
		t.Fatalf("String() leaked bound paths: %q", got)
	}
	if got := rule.GoString(); strings.Contains(got, root) || strings.Contains(got, "task.md") {
		t.Fatalf("GoString() leaked bound paths: %q", got)
	}
	if _, err := json.Marshal(rule); !errors.Is(err, ErrOSSBlobRuleLocalOnly) {
		t.Fatalf("Marshal() error = %v, want ErrOSSBlobRuleLocalOnly", err)
	}
	if _, err := json.Marshal(*rule); !errors.Is(err, ErrOSSBlobRuleLocalOnly) {
		t.Fatalf("Marshal(value) error = %v, want ErrOSSBlobRuleLocalOnly", err)
	}
	var restored OSSBlobRule
	if err := json.Unmarshal([]byte(`{}`), &restored); !errors.Is(err, ErrOSSBlobRuleLocalOnly) {
		t.Fatalf("Unmarshal() error = %v, want ErrOSSBlobRuleLocalOnly", err)
	}

	allowed := map[string]bool{
		"ClaimForDispatch": true,
		"GoString":         true,
		"MarshalJSON":      true,
		"String":           true,
		"UnmarshalJSON":    true,
	}
	typ := reflect.TypeOf(rule)
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !allowed[name] {
			t.Fatalf("unexpected exported OSSBlobRule method %s", name)
		}
		delete(allowed, name)
	}
	for missing := range allowed {
		t.Errorf("expected OSSBlobRule method %s is missing", missing)
	}
}

func newOSSBlobRuleTestRepo(t *testing.T, licensePath, license, promptPath string, prompt []byte) (string, string, RootLicenseBlobEvidence) {
	t.Helper()
	root := newLicenseTestRepo(t)
	writeLicenseTestFile(t, root, licensePath, license)
	writeLicenseTestBytes(t, root, promptPath, prompt)
	commit := commitLicenseTestRepo(t, root, "licensed prompt")
	evidence, err := InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatal(err)
	}
	return root, commit, evidence
}

func eosApache20License() string {
	canonical := strings.TrimPrefix(apache20CanonicalText, "\n")
	terms, _, ok := strings.Cut(canonical, "\n\n   APPENDIX: How to apply the Apache License to your work.")
	if !ok {
		panic("canonical Apache-2.0 terms marker is missing")
	}
	_, boilerplate, ok := strings.Cut(canonical, "   Copyright [yyyy] [name of copyright owner]")
	if !ok {
		panic("canonical Apache-2.0 copyright template is missing")
	}
	return terms + "\n\n   Copyright 2026 Oscar Villavicencio" + boilerplate + "\n"
}
