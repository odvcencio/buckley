package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/workspaceevidence"
)

// capturingOneshotClient records every request that reaches the underlying
// completion client so tests can assert the exact governed wire shape.
type capturingOneshotClient struct {
	requests []model.ChatRequest
}

func (c *capturingOneshotClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	c.requests = append(c.requests, req)
	return &model.ChatResponse{}, nil
}

func setOneshotTestWorkspace(t *testing.T, root string) {
	t.Helper()
	prev := oneshotWorkspaceFn
	oneshotWorkspaceFn = func() (string, error) { return root, nil }
	t.Cleanup(func() { oneshotWorkspaceFn = prev })
}

func writeRecognizedMITLicense(t *testing.T, root string) {
	t.Helper()
	text := "MIT License\n\nCopyright (c) 2026 Buckley Contributors\n\n" + workspaceevidence.CanonicalMITBody
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(text), 0o644); err != nil {
		t.Fatalf("write LICENSE: %v", err)
	}
}

func TestGovernedOneshotClient_RecognizedOSSDispatchesNonZDRDeny(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	governed, ok := client.(*governedOpenRouterClient)
	if !ok {
		t.Fatalf("client type = %T, want *governedOpenRouterClient", client)
	}
	if governed.contract.Policy != "oss_non_zdr" || governed.contract.PolicyAction != "allow" || governed.contract.PolicyReasonCode != "oss_license_verified" {
		t.Fatalf("contract policy = %s/%s/%s, want oss_non_zdr/allow/oss_license_verified", governed.contract.Policy, governed.contract.PolicyAction, governed.contract.PolicyReasonCode)
	}
	if governed.contract.EffectiveRetentionMode() != goalloop.GoalRetentionNonZDR {
		t.Fatalf("retention mode = %q, want non_zdr", governed.contract.EffectiveRetentionMode())
	}
	if governed.contract.WorkspaceLicense.IsZero() || governed.contract.WorkspaceLicense.ID != workspaceevidence.LicenseIDMIT {
		t.Fatalf("workspace license evidence not bound: %+v", governed.contract.WorkspaceLicense)
	}

	originalProvider := map[string]any{
		"allow_fallbacks": true,
		"zdr":             true,
	}
	req := model.ChatRequest{Model: "stealth/ox-alpha", Provider: originalProvider}
	if _, err := client.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("dispatched requests = %d, want 1", len(fake.requests))
	}
	provider := fake.requests[0].Provider
	if provider["allow_fallbacks"] != false {
		t.Fatalf("allow_fallbacks = %#v, want false", provider["allow_fallbacks"])
	}
	if provider["data_collection"] != "deny" {
		t.Fatalf("data_collection = %#v, want deny", provider["data_collection"])
	}
	if _, has := provider["zdr"]; has {
		t.Fatal("non-ZDR dispatch must not set zdr")
	}
	if originalProvider["allow_fallbacks"] != true || originalProvider["zdr"] != true {
		t.Fatalf("caller provider map was mutated: %#v", originalProvider)
	}
	if fake.requests[0].Model != "stealth/ox-alpha" {
		t.Fatalf("model = %q, want exact configured model", fake.requests[0].Model)
	}
}

func TestGovernedOneshotClient_LicenseMutationFailsClosedBeforeDispatch(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}

	// Mutate the recognized license after binding but before dispatch.
	mutated := "MIT License\n\nCopyright (c) 2999 Mutated Contributors\n\n" + workspaceevidence.CanonicalMITBody
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(mutated), 0o644); err != nil {
		t.Fatalf("mutate LICENSE: %v", err)
	}

	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "stealth/ox-alpha"}); err == nil {
		t.Fatal("expected mutated license to block dispatch")
	} else if !strings.Contains(err.Error(), "license_changed") {
		t.Fatalf("error = %v, want license_changed block", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("mutated license dispatched %d requests, want 0", len(fake.requests))
	}
}

func TestGovernedOneshotClient_UnlicensedRequiresStrictZDR(t *testing.T) {
	root := t.TempDir()
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	governed := client.(*governedOpenRouterClient)
	if governed.contract.Policy != "strict_zdr" || governed.contract.PolicyAction != "allow" {
		t.Fatalf("contract policy = %s/%s, want strict_zdr/allow", governed.contract.Policy, governed.contract.PolicyAction)
	}
	if governed.contract.EffectiveRetentionMode() != goalloop.GoalRetentionZDR {
		t.Fatalf("retention mode = %q, want zdr", governed.contract.EffectiveRetentionMode())
	}
	if !governed.contract.WorkspaceLicense.IsZero() {
		t.Fatalf("strict ZDR must not bind license evidence: %+v", governed.contract.WorkspaceLicense)
	}

	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{
		Model:    "stealth/ox-alpha",
		Provider: map[string]any{"data_collection": "allow"},
	}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("dispatched requests = %d, want 1", len(fake.requests))
	}
	provider := fake.requests[0].Provider
	if provider["allow_fallbacks"] != false || provider["zdr"] != true {
		t.Fatalf("provider = %#v, want allow_fallbacks=false zdr=true", provider)
	}
	if _, has := provider["data_collection"]; has {
		t.Fatal("strict ZDR dispatch must not set data_collection")
	}
}

func TestGovernedOneshotClient_ExactModelNoFallback(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}

	// A caller-supplied fallback chain is overridden by the governed policy.
	req := model.ChatRequest{
		Model:    "stealth/ox-alpha",
		Provider: map[string]any{"allow_fallbacks": true},
	}
	if _, err := client.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(fake.requests) != 1 || fake.requests[0].Provider["allow_fallbacks"] != false {
		t.Fatalf("provider fallbacks not disabled: %#v", fake.requests)
	}

	// A drifted model ID never reaches the provider.
	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "other/model"}); err == nil {
		t.Fatal("expected exact-model mismatch to block")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model drift dispatched extra requests: %d", len(fake.requests))
	}
}

func TestOneshotClientForProvider_NonOpenRouterUnchanged(t *testing.T) {
	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "claude-x/some", "anthropic")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	if _, ok := client.(*capturingOneshotClient); !ok {
		t.Fatalf("non-OpenRouter client wrapped: %T", client)
	}
	provider := map[string]any{"allow_fallbacks": true}
	req := model.ChatRequest{Model: "claude-x/some", Provider: provider}
	if _, err := client.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(fake.requests) != 1 || len(fake.requests[0].Provider) != 1 || fake.requests[0].Provider["allow_fallbacks"] != true {
		t.Fatalf("non-OpenRouter request was modified: %#v", fake.requests)
	}
	if _, err := oneshotClientForProvider(nil, "m/x", "openrouter"); err == nil {
		t.Fatal("expected nil OpenRouter client to fail at construction")
	}
}

func TestGovernedOneshotClient_InvalidPolicyBlocksDispatch(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	// A non-canonical model ID cannot carry an OpenRouter privacy policy;
	// the contract must fail closed on every dispatch.
	client, err := oneshotClientForProvider(fake, "not-canonical", "openrouter")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "not-canonical"}); err == nil {
		t.Fatal("expected invalid policy contract to block dispatch")
	} else if !strings.Contains(err.Error(), "invalid_policy_contract") {
		t.Fatalf("error = %v, want invalid_policy_contract", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("invalid policy dispatched %d requests, want 0", len(fake.requests))
	}
}
