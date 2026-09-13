package modelprofile

import (
	"reflect"
	"testing"
)

func TestReviewNormalizationRejectsAmbiguousAndEmptyBudgetKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   bool
		values map[string]int
	}{
		{"effort collision", false, map[string]int{"low": 100, " LOW ": 200}},
		{"size collision", true, map[string]int{"focused": 100, " Focused ": 200}},
		{"empty effort", false, map[string]int{" ": 100}},
		{"empty size", true, map[string]int{"": 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := validProfile()
			profile.Review = &ReviewBehavior{}
			if tc.size {
				profile.Review.ReasoningMaxTokensBySize = tc.values
			} else {
				profile.Review.ReasoningMaxTokensByEffort = tc.values
			}
			for i := 0; i < 30; i++ {
				normalized := profile.Normalize()
				if !reflect.DeepEqual(normalized, normalized.Normalize()) {
					t.Fatal("normalization is not idempotent")
				}
				if err := normalized.Validate(); err == nil {
					t.Fatal("normalization erased invalid budget-key evidence")
				}
				if _, err := normalized.Digest(); err == nil {
					t.Fatal("invalid profile received a digest")
				}
				if err := NewMemoryStore().Put(nil, normalized); err == nil {
					t.Fatal("invalid profile was stored")
				}
			}
		})
	}
}

func TestReviewNormalizationInvalidMapDoesNotAliasCaller(t *testing.T) {
	original := map[string]int{"low": 100, " LOW ": 200}
	normalized := normalizeReviewTokenMap(original)
	normalized["low"] = 999
	if original["low"] != 100 || original[" LOW "] != 200 {
		t.Fatal("normalization aliased caller-owned map")
	}
}
