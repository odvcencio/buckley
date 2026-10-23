package main

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
)

// recordingResolver stands in for the model manager: it records every model it
// is asked to route and answers with the provider a config would choose.
type recordingResolver struct {
	provider string
	asked    []string
	fail     map[string]bool
}

func (r *recordingResolver) ResolveModelRoute(modelID string) (model.ModelRoute, error) {
	r.asked = append(r.asked, modelID)
	if r.fail[modelID] {
		return model.ModelRoute{}, fmt.Errorf("no provider configured for model %s", modelID)
	}
	return model.ModelRoute{RequestedModel: modelID, SelectedModel: modelID, ProviderID: r.provider}, nil
}

func TestReviewRouteLines_EveryCallClassNamesProviderAndModel(t *testing.T) {
	r := &recordingResolver{provider: "openrouter"}
	lines := reviewRouteLines(r, "openai/gpt-6-luna-pro", "")
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Route: primary review: openai/gpt-6-luna-pro via openrouter",
		"Route: PR shards: openai/gpt-6-luna-pro via openrouter",
		"Route: approval critic: openai/gpt-6-luna-pro via openrouter (same as primary)",
		"Route: other model calls: none",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "google") || strings.Contains(joined, "gemini") {
		t.Errorf("routes mention Google:\n%s", joined)
	}
	if len(r.asked) != 1 || r.asked[0] != "openai/gpt-6-luna-pro" {
		t.Errorf("resolver asked for %v, want only the primary model", r.asked)
	}
}

func TestReviewRouteLines_DedicatedCriticAndUnresolvedRoute(t *testing.T) {
	r := &recordingResolver{provider: "openrouter", fail: map[string]bool{"mystery/model": true}}
	joined := strings.Join(reviewRouteLines(r, "openai/gpt-6-luna-pro", "mystery/model"), "\n")
	if !strings.Contains(joined, "approval critic: mystery/model (unresolved: no provider configured for model mystery/model)") {
		t.Errorf("an unroutable critic must be visible:\n%s", joined)
	}
}

// Buckbot reaches Google only through the review-ledger archive, which needs an
// explicit bucket. Nothing may turn it on by default.
func TestReviewLedgerArchiveIsOffByDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Review.Ledger.GCS.Bucket != "" {
		t.Fatalf("default GCS bucket = %q, want none", cfg.Review.Ledger.GCS.Bucket)
	}
	built := false
	prev := newReviewLedger
	newReviewLedger = func(config.ReviewLedgerGCSConfig, string) orchestrator.ReviewLedger {
		built = true
		return nil
	}
	t.Cleanup(func() { newReviewLedger = prev })
	if ledger := configuredReviewLedger(cfg); ledger != nil || built {
		t.Fatalf("a default config built a ledger (ledger=%v built=%v)", ledger, built)
	}
	if ledger := configuredReviewLedger(nil); ledger != nil || built {
		t.Fatal("a nil config built a ledger")
	}
}
