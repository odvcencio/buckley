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

// The tests below pass dataPolicy "zdr" explicitly: they lock the governed
// gate's behavior for callers who opt back into it. See
// TestOneshotClientForProvider_DefaultDataPolicySkipsGovernance and
// TestOneshotClientForProvider_DenyDataPolicyForcesNonZDR below for the
// default ("none") and "deny" modes.

func TestGovernedOneshotClient_RecognizedOSSDispatchesNonZDRDeny(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "zdr")
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
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "zdr")
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
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "zdr")
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
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "zdr")
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
	client, err := oneshotClientForProvider(fake, "claude-x/some", "anthropic", "zdr")
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
	if _, err := oneshotClientForProvider(nil, "m/x", "openrouter", "zdr"); err == nil {
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
	client, err := oneshotClientForProvider(fake, "not-canonical", "openrouter", "zdr")
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

// TestOneshotClientForProvider_DefaultDataPolicySkipsGovernance locks the
// 2026-09-04 owner decision: with the default ("none") data policy, an
// OpenRouter one-shot request is never wrapped in governedOpenRouterClient,
// so no provider.zdr/data_collection field is attached and the goal-engine
// model-data-policy contract is never bound or enforced. It uses the same
// "not-canonical" model ID that TestGovernedOneshotClient_InvalidPolicyBlocksDispatch
// proves fails contract validation under "zdr", to demonstrate the contract
// check itself is skipped, not merely passing.
func TestOneshotClientForProvider_DefaultDataPolicySkipsGovernance(t *testing.T) {
	root := t.TempDir()
	setOneshotTestWorkspace(t, root)

	for _, dataPolicy := range []string{"", "none", "NONE", "  "} {
		fake := &capturingOneshotClient{}
		client, err := oneshotClientForProvider(fake, "not-canonical", "openrouter", dataPolicy)
		if err != nil {
			t.Fatalf("oneshotClientForProvider(%q): %v", dataPolicy, err)
		}
		if _, ok := client.(*governedOpenRouterClient); ok {
			t.Fatalf("dataPolicy %q wrapped the client in governedOpenRouterClient, want passthrough", dataPolicy)
		}
		provider := map[string]any{"allow_fallbacks": true}
		req := model.ChatRequest{Model: "not-canonical", Provider: provider}
		if _, err := client.ChatCompletion(context.Background(), req); err != nil {
			t.Fatalf("dataPolicy %q: ChatCompletion: %v", dataPolicy, err)
		}
		if len(fake.requests) != 1 {
			t.Fatalf("dataPolicy %q: dispatched requests = %d, want 1", dataPolicy, len(fake.requests))
		}
		got := fake.requests[0].Provider
		if _, has := got["zdr"]; has {
			t.Fatalf("dataPolicy %q: request carries provider.zdr, want none: %#v", dataPolicy, got)
		}
		if _, has := got["data_collection"]; has {
			t.Fatalf("dataPolicy %q: request carries provider.data_collection, want none: %#v", dataPolicy, got)
		}
		if len(got) != 1 || got["allow_fallbacks"] != true {
			t.Fatalf("dataPolicy %q: request provider was modified: %#v", dataPolicy, got)
		}
	}
}

// TestOneshotClientForProvider_DenyDataPolicyForcesNonZDR locks the "deny"
// opt-in: its contract requests non-ZDR retention with
// provider.data_collection=deny as the baseline (unlike "zdr", which only
// reaches non-ZDR by relaxing from a strict-ZDR default). The shared
// runtime/model_data_policy Arbiter strategy still requires recognized,
// digest-matched OSS license evidence for any non-ZDR dispatch regardless of
// which opt-in produced the contract (see NonZDRLicenseMissing and
// NonZDROSSAllow in model_data_policy.arb) — "deny" does not bypass that
// evidence gate, only the default "none" mode skips contract enforcement
// entirely.
func TestOneshotClientForProvider_DenyDataPolicyForcesNonZDR(t *testing.T) {
	root := t.TempDir()
	writeRecognizedMITLicense(t, root)
	setOneshotTestWorkspace(t, root)

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "deny")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	governed, ok := client.(*governedOpenRouterClient)
	if !ok {
		t.Fatalf("client type = %T, want *governedOpenRouterClient", client)
	}
	if governed.contract.EffectiveRetentionMode() != goalloop.GoalRetentionNonZDR {
		t.Fatalf("retention mode = %q, want non_zdr", governed.contract.EffectiveRetentionMode())
	}

	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "stealth/ox-alpha"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("dispatched requests = %d, want 1", len(fake.requests))
	}
	provider := fake.requests[0].Provider
	if provider["data_collection"] != "deny" {
		t.Fatalf("data_collection = %#v, want deny", provider["data_collection"])
	}
	if _, has := provider["zdr"]; has {
		t.Fatal("deny dispatch must not set zdr")
	}
}

// TestOneshotClientForProvider_DenyDataPolicyBlocksWithoutLicenseEvidence
// confirms "deny" fails closed exactly like "zdr" does for an unlicensed
// workspace: it never dispatches, and reports the missing evidence.
func TestOneshotClientForProvider_DenyDataPolicyBlocksWithoutLicenseEvidence(t *testing.T) {
	root := t.TempDir()
	setOneshotTestWorkspace(t, root) // no LICENSE file written

	fake := &capturingOneshotClient{}
	client, err := oneshotClientForProvider(fake, "stealth/ox-alpha", "openrouter", "deny")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "stealth/ox-alpha"}); err == nil {
		t.Fatal("expected missing license evidence to block deny dispatch")
	} else if !strings.Contains(err.Error(), "license_missing") {
		t.Fatalf("error = %v, want license_missing block", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("blocked deny dispatched %d requests, want 0", len(fake.requests))
	}
}
