package rules

import "testing"

func TestModelDataPolicy_AllContractOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		facts      map[string]any
		wantAction string
		wantReason string
		wantPolicy string
	}{
		{
			name:       "strict zdr",
			facts:      modelDataPolicyFacts("zdr", true, "", "recognized_oss", false),
			wantAction: "allow", wantReason: "zdr_enforced", wantPolicy: "strict_zdr",
		},
		{
			name:       "strict zdr unenforceable",
			facts:      modelDataPolicyFacts("zdr", false, "", "recognized_oss", true),
			wantAction: "block", wantReason: "zdr_unenforceable", wantPolicy: "strict_zdr",
		},
		{
			name:       "strict zdr missing capability",
			facts:      modelDataPolicyFacts("zdr", false, "", "missing", false),
			wantAction: "block", wantReason: "zdr_unenforceable", wantPolicy: "strict_zdr",
		},
		{
			name:       "non zdr missing data policy",
			facts:      modelDataPolicyFacts("non_zdr", false, "", "recognized_oss", true),
			wantAction: "block", wantReason: "data_collection_policy_missing", wantPolicy: "oss_non_zdr",
		},
		{
			name:       "non zdr data policy not deny",
			facts:      modelDataPolicyFacts("non_zdr", false, "allow", "recognized_oss", true),
			wantAction: "block", wantReason: "data_collection_policy_missing", wantPolicy: "oss_non_zdr",
		},
		{
			name:       "non zdr unsupported provider",
			facts:      modelDataPolicyFactsWithProvider("non_zdr", false, "deny", "recognized_oss", true, "anthropic"),
			wantAction: "block", wantReason: "provider_privacy_unsupported", wantPolicy: "oss_non_zdr",
		},
		{
			name:       "non zdr verified oss",
			facts:      modelDataPolicyFacts("non_zdr", false, "deny", "recognized_oss", true),
			wantAction: "allow", wantReason: "oss_license_verified", wantPolicy: "oss_non_zdr",
		},
		{
			name:       "legacy verified oss",
			facts:      modelDataPolicyFacts("legacy", false, "", "recognized_oss", true),
			wantAction: "allow", wantReason: "oss_license_verified", wantPolicy: "oss_legacy",
		},
		{
			name:       "non zdr digest mismatch",
			facts:      modelDataPolicyFacts("non_zdr", false, "deny", "recognized_oss", false),
			wantAction: "block", wantReason: "license_digest_mismatch", wantPolicy: "oss_non_zdr",
		},
		{
			name:       "legacy digest mismatch",
			facts:      modelDataPolicyFacts("legacy", false, "", "recognized_oss", false),
			wantAction: "block", wantReason: "license_digest_mismatch", wantPolicy: "oss_legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mustNewTestEngine(t).EvalStrategy("runtime/model_data_policy", "model_data_policy", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			assertModelDataPolicyResult(t, result.Params, tt.wantAction, tt.wantReason, tt.wantPolicy)
		})
	}
}

func TestModelDataPolicy_LicenseFailureReasons(t *testing.T) {
	for _, status := range []string{"missing", "ambiguous", "proprietary", "unreadable", "changed", "unsupported"} {
		t.Run(status, func(t *testing.T) {
			result, err := mustNewTestEngine(t).EvalStrategy("runtime/model_data_policy", "model_data_policy", modelDataPolicyFacts("non_zdr", false, "deny", status, true))
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			assertModelDataPolicyResult(t, result.Params, "block", "license_"+status, "oss_non_zdr")
		})
	}
	for _, status := range []string{"missing", "ambiguous", "proprietary", "unreadable", "changed", "unsupported"} {
		t.Run("legacy/"+status, func(t *testing.T) {
			result, err := mustNewTestEngine(t).EvalStrategy("runtime/model_data_policy", "model_data_policy", modelDataPolicyFacts("legacy", false, "", status, true))
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			assertModelDataPolicyResult(t, result.Params, "block", "license_"+status, "oss_legacy")
		})
	}
}

func TestModelDataPolicy_InvalidContractsBlock(t *testing.T) {
	tests := []struct {
		name  string
		facts map[string]any
	}{
		{
			name:  "unknown retention",
			facts: modelDataPolicyFacts("future", false, "deny", "recognized_oss", true),
		},
		{
			name:  "missing retention",
			facts: map[string]any{"provider": map[string]any{"name": "openrouter"}},
		},
		{
			name:  "non zdr unknown license status",
			facts: modelDataPolicyFacts("non_zdr", false, "deny", "future", true),
		},
		{
			name:  "legacy unknown license status",
			facts: modelDataPolicyFacts("legacy", false, "", "future", true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mustNewTestEngine(t).EvalStrategy("runtime/model_data_policy", "model_data_policy", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			if got := result.Params["action"]; got != "block" {
				t.Fatalf("action = %q, want block", got)
			}
		})
	}
}

func TestModelDataPolicyCatalog(t *testing.T) {
	contracts := FactContractsForDomain("runtime/model_data_policy")
	if len(contracts) != 1 {
		t.Fatalf("got %d contracts, want 1", len(contracts))
	}
	want := []string{
		"privacy.data_collection",
		"privacy.retention_mode",
		"privacy.zdr_enforceable",
		"provider.name",
		"workspace.license_digest_match",
		"workspace.license_id",
		"workspace.license_status",
	}
	for _, key := range want {
		assertFact(t, contracts[0], key)
	}
}

func modelDataPolicyFacts(retention string, zdrEnforceable bool, dataCollection, status string, digestMatch bool) map[string]any {
	return modelDataPolicyFactsWithProvider(retention, zdrEnforceable, dataCollection, status, digestMatch, "openrouter")
}

func modelDataPolicyFactsWithProvider(retention string, zdrEnforceable bool, dataCollection, status string, digestMatch bool, provider string) map[string]any {
	return map[string]any{
		"provider": map[string]any{"name": provider},
		"privacy": map[string]any{
			"retention_mode":  retention,
			"zdr_enforceable": zdrEnforceable,
			"data_collection": dataCollection,
		},
		"workspace": map[string]any{
			"license_status":       status,
			"license_id":           "MIT",
			"license_digest_match": digestMatch,
		},
	}
}

func assertModelDataPolicyResult(t *testing.T, params map[string]any, wantAction, wantReason, wantPolicy string) {
	t.Helper()
	if got := params["action"]; got != wantAction {
		t.Errorf("action = %#v, want %q", got, wantAction)
	}
	if got := params["reason_code"]; got != wantReason {
		t.Errorf("reason_code = %#v, want %q", got, wantReason)
	}
	if got := params["policy"]; got != wantPolicy {
		t.Errorf("policy = %#v, want %q", got, wantPolicy)
	}
}
