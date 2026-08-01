package conversation

import "testing"

func TestNextModelVariant_CyclesInOrder(t *testing.T) {
	variants := DefaultModelVariants

	got := NextModelVariant(variants, "")
	if got.Name != "fast" {
		t.Fatalf("NextModelVariant(empty) = %q, want fast", got.Name)
	}

	got = NextModelVariant(variants, "fast")
	if got.Name != "balanced" {
		t.Fatalf("NextModelVariant(fast) = %q, want balanced", got.Name)
	}

	got = NextModelVariant(variants, "balanced")
	if got.Name != "deep" {
		t.Fatalf("NextModelVariant(balanced) = %q, want deep", got.Name)
	}

	got = NextModelVariant(variants, "deep")
	if got.Name != "deep+retained" {
		t.Fatalf("NextModelVariant(deep) = %q, want deep+retained", got.Name)
	}

	got = NextModelVariant(variants, "deep+retained")
	if got.Name != "fast" {
		t.Fatalf("NextModelVariant(deep+retained) = %q, want fast (wrap)", got.Name)
	}
}

func TestNextModelVariant_UnknownNameStartsAtFirst(t *testing.T) {
	got := NextModelVariant(DefaultModelVariants, "not-a-variant")
	if got.Name != "fast" {
		t.Fatalf("NextModelVariant(unknown) = %q, want fast", got.Name)
	}
}

func TestNextModelVariant_EmptyVariantsReturnsZeroValue(t *testing.T) {
	got := NextModelVariant(nil, "fast")
	if got.Name != "" {
		t.Fatalf("NextModelVariant(nil variants) = %+v, want zero value", got)
	}
}

func TestBuiltinVariants_CombineEffortAndContinuation(t *testing.T) {
	tests := []struct {
		variant    ModelVariant
		effort     string
		continuing bool
	}{
		{VariantFast, "minimal", false},
		{VariantBalanced, "medium", false},
		{VariantDeep, "high", false},
		{VariantDeepRetained, "high", true},
	}
	for _, tt := range tests {
		if tt.variant.ReasoningEffort != tt.effort {
			t.Errorf("%s effort = %q, want %q", tt.variant.Name, tt.variant.ReasoningEffort, tt.effort)
		}
		if tt.variant.ProviderContinuation != tt.continuing {
			t.Errorf("%s continuation = %v, want %v", tt.variant.Name, tt.variant.ProviderContinuation, tt.continuing)
		}
	}
}
