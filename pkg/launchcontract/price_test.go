package launchcontract

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFreePriceEvidence_CanonicalAndStable(t *testing.T) {
	first, err := NormalizeZeroPrices(map[string]string{
		"prompt": "0", "completion": "0.0", "request": "0e+9", "image": "-0",
		"web_search": "0", "internal_reasoning": "0", "input_cache_read": "0", "input_cache_write": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeZeroPrices(map[string]string{
		"input_cache_write": "0", "input_cache_read": "0", "internal_reasoning": "0", "web_search": "0",
		"image": "-0", "request": "0e+9", "completion": "0.0", "prompt": "0",
	})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("canonical prices = %+v / %+v, %v", first, second, err)
	}
	now := time.Date(2026, 8, 21, 18, 0, 0, 123_000_000, time.UTC)
	receipt, err := newCatalogReceipt(now, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	left, err := newFreePriceEvidence(ModelOxAlpha, now, MaxFreePriceEvidenceTTL, first, receipt)
	if err != nil {
		t.Fatal(err)
	}
	right, err := newFreePriceEvidence(ModelOxAlpha, now, MaxFreePriceEvidenceTTL, second, receipt)
	if err != nil {
		t.Fatal(err)
	}
	leftBytes, _ := left.CanonicalBytes()
	rightBytes, _ := right.CanonicalBytes()
	if left.Digest != right.Digest || !bytes.Equal(leftBytes, rightBytes) {
		t.Fatalf("evidence changed across map order: %s / %s", left.Digest, right.Digest)
	}
	if err := left.ValidateAt(now); err != nil {
		t.Fatal(err)
	}
}

func TestFreePriceEvidence_RejectsEveryPotentialCharge(t *testing.T) {
	for _, test := range []struct {
		name   string
		prices map[string]string
	}{
		{name: "missing prompt", prices: map[string]string{"completion": "0", "request": "0"}},
		{name: "missing completion", prices: map[string]string{"prompt": "0", "request": "0"}},
		{name: "unknown", prices: map[string]string{"prompt": "0", "completion": "0", "future_charge": "0"}},
		{name: "positive exponent", prices: map[string]string{"prompt": "0", "completion": "1e+1"}},
		{name: "negative exponent", prices: map[string]string{"prompt": "0", "completion": "1e-999"}},
		{name: "negative underflow", prices: map[string]string{"prompt": "0", "completion": "-1e-999"}},
		{name: "nan", prices: map[string]string{"prompt": "0", "completion": "NaN"}},
		{name: "infinity", prices: map[string]string{"prompt": "0", "completion": "Inf"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeZeroPrices(test.prices); err == nil {
				t.Fatal("charge-bearing pricing accepted")
			}
		})
	}
}

func TestFreePriceEvidence_TimeAndIdentityFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 21, 18, 0, 0, 0, time.UTC)
	prices, err := NormalizeZeroPrices(allZeroPrices())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newCatalogReceipt(now, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := newFreePriceEvidence(ModelOxAlpha, now, time.Minute, prices, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.ValidateAt(now.Add(time.Minute)); err == nil {
		t.Fatal("expired evidence accepted")
	}
	if err := evidence.ValidateAt(now.Add(-time.Nanosecond)); err == nil {
		t.Fatal("future observation accepted")
	}
	if err := evidence.ValidateAt(now.In(time.FixedZone("offset", 3600))); err == nil {
		t.Fatal("non-UTC observation time accepted")
	}
	changed := evidence
	changed.SourceURL = "https://example.invalid/models"
	if err := changed.ValidateAt(now); err == nil {
		t.Fatal("custom catalog source accepted")
	}
	changed = evidence
	changed.CanonicalSlug = "ox-alpha"
	if err := changed.ValidateAt(now); err == nil {
		t.Fatal("model alias accepted")
	}
}

func allZeroPrices() map[string]string {
	return map[string]string{
		"prompt": "0", "completion": "0", "request": "0", "image": "0",
		"web_search": "0", "internal_reasoning": "0", "input_cache_read": "0", "input_cache_write": "0",
	}
}
