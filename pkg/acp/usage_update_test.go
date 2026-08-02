package acp

import (
	"encoding/json"
	"testing"
)

// TestNewUsageUpdate_WireShape locks the exact JSON shape of a usage_update
// session update (N1): "used"/"size" as numbers, "cost" present only when
// supplied.
func TestNewUsageUpdate_WireShape(t *testing.T) {
	t.Parallel()

	t.Run("without cost", func(t *testing.T) {
		t.Parallel()
		update := NewUsageUpdate(150, 128000, nil)
		data, err := json.Marshal(update)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if raw["sessionUpdate"] != "usage_update" {
			t.Fatalf("sessionUpdate = %v, want usage_update", raw["sessionUpdate"])
		}
		if raw["used"] != float64(150) {
			t.Fatalf("used = %v, want 150", raw["used"])
		}
		if raw["size"] != float64(128000) {
			t.Fatalf("size = %v, want 128000", raw["size"])
		}
		if _, present := raw["cost"]; present {
			t.Fatalf("cost must be omitted when nil, got %v", raw["cost"])
		}
	})

	t.Run("with cost", func(t *testing.T) {
		t.Parallel()
		update := NewUsageUpdate(150, 128000, &Cost{Amount: 0.045, Currency: "USD"})
		data, err := json.Marshal(update)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		cost, ok := raw["cost"].(map[string]any)
		if !ok {
			t.Fatalf("cost = %v, want an object", raw["cost"])
		}
		if cost["amount"] != 0.045 || cost["currency"] != "USD" {
			t.Fatalf("cost = %v, want {amount:0.045, currency:USD}", cost)
		}
	})
}
