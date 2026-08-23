package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const ossOneUseExactWire = `{"model":"stealth/ox-alpha","messages":[{"role":"user","content":"return only a focused patch\n"}],"max_completion_tokens":16384,"stream":false,"reasoning":{"effort":"medium"},"provider":{"allow_fallbacks":false,"data_collection":"deny","zdr":false}}`

func TestOneUseOSSOpenRouterClient_ExactBoundRequestAndContext(t *testing.T) {
	prompt := []byte("return only a focused patch\n")
	rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
	governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
	if err != nil {
		t.Fatalf("NewOneUseOSSOpenRouterClient: %v", err)
	}
	t.Cleanup(func() { _ = governed.Close() })
	if got := governed.client.ossHTTPClient.Timeout; got != oxAlphaOneUseTransportTimeout {
		t.Fatalf("OSS transport timeout = %v, want %v", got, oxAlphaOneUseTransportTimeout)
	}

	boundPrompt[0] = 'X'
	admitted, err := governed.admit(t.Context())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if admitted.openRouterContext == ([sha256.Size]byte{}) {
		t.Fatal("admitted request has an empty rule context")
	}
	if admitted.openRouterAdmission == nil || admitted.openRouterAdmission.contextBinding != admitted.openRouterContext {
		t.Fatal("admission does not carry the exact rule context")
	}
	body, err := marshalOpenRouterOSSFinalWire(admitted)
	if err != nil {
		t.Fatalf("marshal final wire: %v", err)
	}
	if !bytes.Equal(body, []byte(ossOneUseExactWire)) {
		t.Fatalf("final wire = %s, want %s", body, ossOneUseExactWire)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "max_completion_tokens", "stream", "reasoning", "provider"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("fixed request is missing %q: %s", key, body)
		}
	}
	if len(wire) != 6 {
		t.Fatalf("fixed request has alternate fields: %s", body)
	}
}

func TestOneUseOSSOpenRouterClient_CompletePatchDispatchesOnce(t *testing.T) {
	var calls atomic.Int32
	governed, _ := newOSSOneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Method != http.MethodPost || req.URL.String() != defaultBaseURL+"/chat/completions" {
			return nil, fmt.Errorf("request = %s %s", req.Method, req.URL)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(body, []byte(ossOneUseExactWire)) {
			return nil, fmt.Errorf("unexpected final wire: %s", body)
		}
		return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"stealth/ox-alpha","choices":[{"message":{"role":"assistant","content":"patch"},"finish_reason":"stop"}]}`), nil
	}))

	response, err := governed.CompletePatch(t.Context())
	if err != nil {
		t.Fatalf("CompletePatch: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "patch" {
		t.Fatalf("response = %#v", response)
	}
	if _, err := governed.CompletePatch(t.Context()); !errors.Is(err, ErrOpenRouterOSSOneUseSpent) {
		t.Fatalf("second CompletePatch error = %v, want spent client", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestOneUseOSSOpenRouterClient_WorktreeMutationStopsBeforeTransport(t *testing.T) {
	prompt := []byte("return only a focused patch\n")
	rule, boundPrompt, root := mintOSSOneUseTestRuleAtRoot(t, prompt)
	governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = governed.Close() })
	governed.client.rateLimiter = nil
	calls := &atomic.Int32{}
	governed.client.ossHTTPClient.Transport = ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"stealth/ox-alpha","choices":[{"message":{"role":"assistant","content":"patch"},"finish_reason":"stop"}]}`), nil
	})

	dirtyPath := filepath.Join(root, "dispatch-race.txt")
	if err := os.WriteFile(dirtyPath, []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := governed.CompletePatch(t.Context()); !errors.Is(err, workspaceevidence.ErrEvidenceStale) {
		t.Fatalf("CompletePatch() error = %v, want ErrEvidenceStale", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls after stale dispatch = %d, want 0", calls.Load())
	}
	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := governed.CompletePatch(t.Context()); err != nil {
		t.Fatalf("CompletePatch() after restoring exact clean worktree = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls after restored dispatch = %d, want 1", calls.Load())
	}
}

func TestOneUseOSSOpenRouterClient_RetainsUnclaimedRule(t *testing.T) {
	prompt := []byte("return only a focused patch\n")
	rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
	governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = governed.Close() })
	if binding, err := rule.ClaimForDispatch(t.Context(), prompt); err != nil || binding == ([sha256.Size]byte{}) {
		t.Fatalf("constructor claimed or invalidated rule: binding=%x error=%v", binding, err)
	}
	if _, err := governed.CompletePatch(t.Context()); !errors.Is(err, workspaceevidence.ErrOSSBlobRuleSpent) {
		t.Fatalf("CompletePatch error = %v, want spent rule", err)
	}
}

func TestOneUseOSSOpenRouterClient_RejectsCopiedAndTamperedAuthority(t *testing.T) {
	t.Run("copied rule", func(t *testing.T) {
		var calls atomic.Int32
		prompt := []byte("return only a focused patch\n")
		rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
		copiedRule := *rule
		governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, &copiedRule, boundPrompt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = governed.Close() })
		governed.client.rateLimiter = nil
		governed.client.ossHTTPClient.Transport = countingOSSOneUseTransport(&calls)
		if _, err := governed.CompletePatch(t.Context()); !errors.Is(err, workspaceevidence.ErrOSSBlobRuleInvalid) {
			t.Fatalf("CompletePatch error = %v, want invalid copied rule", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("transport calls = %d, want 0", calls.Load())
		}
	})

	t.Run("rule substitution", func(t *testing.T) {
		prompt := []byte("return only a focused patch\n")
		rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
		governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = governed.Close() })
		governed.rule = nil
		if _, err := governed.CompletePatch(t.Context()); err == nil || !strings.Contains(err.Error(), "client is invalid") {
			t.Fatalf("CompletePatch error = %v, want invalid substituted rule", err)
		}
		if binding, err := rule.ClaimForDispatch(t.Context(), prompt); err != nil || binding == ([sha256.Size]byte{}) {
			t.Fatalf("tamper consumed original authority: binding=%x error=%v", binding, err)
		}
	})

	t.Run("bound prompt mutation", func(t *testing.T) {
		prompt := []byte("return only a focused patch\n")
		rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
		governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = governed.Close() })
		governed.prompt[0] = 'X'
		if _, err := governed.CompletePatch(t.Context()); err == nil || !strings.Contains(err.Error(), "client is invalid") {
			t.Fatalf("CompletePatch error = %v, want invalid mutated prompt", err)
		}
		if binding, err := rule.ClaimForDispatch(t.Context(), prompt); err != nil || binding == ([sha256.Size]byte{}) {
			t.Fatalf("tamper consumed original authority: binding=%x error=%v", binding, err)
		}
	})
}

func newOSSOneUseTestClient(t *testing.T, transport http.RoundTripper) (*OneUseOSSOpenRouterClient, *atomic.Int32) {
	t.Helper()
	prompt := []byte("return only a focused patch\n")
	rule, boundPrompt := mintOSSOneUseTestRule(t, prompt)
	governed, err := NewOneUseOSSOpenRouterClient("test-key", "", OxAlphaOpenRouterModelID, rule, boundPrompt)
	if err != nil {
		t.Fatalf("NewOneUseOSSOpenRouterClient: %v", err)
	}
	t.Cleanup(func() { _ = governed.Close() })
	governed.client.rateLimiter = nil
	calls := &atomic.Int32{}
	if transport == nil {
		transport = countingOSSOneUseTransport(calls)
	}
	governed.client.ossHTTPClient.Transport = transport
	return governed, calls
}

func countingOSSOneUseTransport(calls *atomic.Int32) http.RoundTripper {
	if calls == nil {
		calls = &atomic.Int32{}
	}
	return ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return ossAdmissionResponse(req, http.StatusOK, `{"id":"unexpected","choices":[]}`), nil
	})
}

func mintOSSOneUseTestRule(t *testing.T, prompt []byte) (*workspaceevidence.OSSBlobRule, []byte) {
	t.Helper()
	rule, boundPrompt, _ := mintOSSOneUseTestRuleAtRoot(t, prompt)
	return rule, boundPrompt
}

func mintOSSOneUseTestRuleAtRoot(t *testing.T, prompt []byte) (*workspaceevidence.OSSBlobRule, []byte, string) {
	t.Helper()
	root := t.TempDir()
	ossOneUseGit(t, root, "init", "--quiet")
	ossOneUseGit(t, root, "config", "user.name", "Buckley Test")
	ossOneUseGit(t, root, "config", "user.email", "buckley@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(ossOneUseMITLicense), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "patch.txt"), prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	ossOneUseGit(t, root, "add", "LICENSE", "prompts/patch.txt")
	ossOneUseGit(t, root, "commit", "--quiet", "-m", "licensed prompt")
	commit := strings.TrimSpace(ossOneUseGit(t, root, "rev-parse", "HEAD"))
	evidence, err := workspaceevidence.InspectRootLicenseBlob(t.Context(), root, commit)
	if err != nil {
		t.Fatalf("InspectRootLicenseBlob: %v", err)
	}
	rule, boundPrompt, err := workspaceevidence.MintTrackedPromptOSSBlobRule(t.Context(), evidence, "prompts/patch.txt")
	if err != nil {
		t.Fatalf("MintTrackedPromptOSSBlobRule: %v", err)
	}
	return rule, boundPrompt, root
}

func ossOneUseGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

const ossOneUseMITLicense = `MIT License

Copyright (c) 2026 Buckley Test

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
