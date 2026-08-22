package rules

import "testing"

func TestCompactionModelRoute_FailClosedCriteria(t *testing.T) {
	tests := []struct {
		name       string
		facts      map[string]any
		wantRoute  string
		wantReason string
	}{
		{
			name:       "verified free campaign model",
			facts:      compactionModelFacts(true, true, true, true, true, true),
			wantRoute:  "campaign",
			wantReason: "verified_free_and_policy_eligible",
		},
		{
			name:       "campaign not requested",
			facts:      compactionModelFacts(false, true, true, true, true, true),
			wantRoute:  "commit",
			wantReason: "campaign_not_requested",
		},
		{
			name:       "price not verified",
			facts:      compactionModelFacts(true, false, true, true, true, true),
			wantRoute:  "commit",
			wantReason: "free_price_unverified",
		},
		{
			name:       "price evidence stale",
			facts:      compactionModelFacts(true, true, false, true, true, true),
			wantRoute:  "commit",
			wantReason: "free_price_stale",
		},
		{
			name:       "workspace not verified oss",
			facts:      compactionModelFacts(true, true, true, false, true, true),
			wantRoute:  "commit",
			wantReason: "workspace_not_verified_oss",
		},
		{
			name:       "privacy policy ineligible",
			facts:      compactionModelFacts(true, true, true, true, false, true),
			wantRoute:  "commit",
			wantReason: "privacy_policy_ineligible",
		},
		{
			name:       "commit fallback missing",
			facts:      compactionModelFacts(true, true, true, true, true, false),
			wantRoute:  "block",
			wantReason: "commit_model_missing",
		},
		{
			name:       "missing facts",
			facts:      map[string]any{},
			wantRoute:  "block",
			wantReason: "commit_model_missing",
		},
	}

	engine := mustNewTestEngine(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.EvalStrategy("runtime/compaction_model", "compaction_model_route", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			if got := result.Params["route"]; got != tt.wantRoute {
				t.Errorf("route = %#v, want %q", got, tt.wantRoute)
			}
			if got := result.Params["reason_code"]; got != tt.wantReason {
				t.Errorf("reason_code = %#v, want %q", got, tt.wantReason)
			}
		})
	}
}

func compactionModelFacts(requested, priceVerified, priceFresh, ossVerified, privacyAllowed, fallbackConfigured bool) map[string]any {
	return map[string]any{
		"campaign": map[string]any{
			"requested":            requested,
			"price_verified":       priceVerified,
			"price_evidence_fresh": priceFresh,
		},
		"workspace": map[string]any{"oss_verified": ossVerified},
		"privacy":   map[string]any{"allowed": privacyAllowed},
		"fallback":  map[string]any{"configured": fallbackConfigured},
	}
}
