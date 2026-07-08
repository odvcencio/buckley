package jsonrepair

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRepair_ExactObservedError reproduces the literal error text seen live
// from `buckley pr` under z-ai/glm-5.2: "unmarshal tool call: invalid
// character ' ' in numeric literal". encoding/json's scanner only emits the
// bare "invalid character %q in numeric literal" message (as opposed to the
// "after decimal point"/"in exponent" variants) when it sees a non-digit
// immediately after a numeric literal's leading '-' -- i.e. GLM's tool-call
// argument JSON emitted a negative number as "- 5" instead of "-5". This
// test proves Repair fixes exactly that payload, and that without a
// repair step encoding/json fails with the exact reported message.
func TestRepair_ExactObservedError(t *testing.T) {
	broken := []byte(`{"confidence": - 5, "label": "ok"}`)

	// Sanity check: confirm the unrepaired payload reproduces the exact
	// live error text from the bug report.
	var probe map[string]any
	err := json.Unmarshal(broken, &probe)
	if err == nil {
		t.Fatalf("sanity check failed: expected the unrepaired payload to fail unmarshal")
	}
	if !strings.Contains(err.Error(), "invalid character ' ' in numeric literal") {
		t.Fatalf("sanity check failed: expected exact reported error text, got: %v", err)
	}

	repaired := Repair(broken)
	if !json.Valid(repaired) {
		t.Fatalf("Repair(%q) = %q, not valid JSON", broken, repaired)
	}

	var out struct {
		Confidence float64 `json:"confidence"`
		Label      string  `json:"label"`
	}
	if err := json.Unmarshal(repaired, &out); err != nil {
		t.Fatalf("unmarshal after repair: %v (repaired=%q)", err, repaired)
	}
	if out.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", out.Confidence)
	}
	if out.Label != "ok" {
		t.Errorf("Label = %q, want %q", out.Label, "ok")
	}
}

func TestRepair_NumericSpacingVariants(t *testing.T) {
	tests := []struct {
		name   string
		broken string
		want   string
	}{
		{"decimal point split", `{"a": 0. 92}`, `{"a": 0.92}`},
		{"decimal point split with tab", "{\"a\": 0.\t92}", `{"a": 0.92}`},
		{"leading minus split", `{"a": - 5}`, `{"a": -5}`},
		{"exponent sign split", `{"a": 1e- 5}`, `{"a": 1e-5}`},
		{"exponent marker split", `{"a": 1e -5}`, `{"a": 1e-5}`},
		{"multiple spaces collapsed", `{"a": 0.   92}`, `{"a": 0.92}`},
		{"newline inside literal", "{\"a\": 0.\n92}", `{"a": 0.92}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if json.Valid([]byte(tt.broken)) {
				t.Fatalf("test setup bug: %q is already valid JSON", tt.broken)
			}
			got := Repair([]byte(tt.broken))
			if !json.Valid(got) {
				t.Fatalf("Repair(%q) = %q, not valid JSON", tt.broken, got)
			}

			var gotVal, wantVal any
			if err := json.Unmarshal(got, &gotVal); err != nil {
				t.Fatalf("unmarshal repaired: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantVal); err != nil {
				t.Fatalf("unmarshal want: %v", err)
			}
			gotJSON, _ := json.Marshal(gotVal)
			wantJSON, _ := json.Marshal(wantVal)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("Repair(%q) = %q (normalized %s), want normalized %s", tt.broken, got, gotJSON, wantJSON)
			}
		})
	}
}

func TestRepair_TrailingComma(t *testing.T) {
	tests := []struct {
		name   string
		broken string
	}{
		{"object trailing comma", `{"a": 1,}`},
		{"array trailing comma", `{"a": [1, 2,]}`},
		{"nested trailing commas", `{"a": [1, 2,], "b": {"c": 3,},}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if json.Valid([]byte(tt.broken)) {
				t.Fatalf("test setup bug: %q is already valid JSON", tt.broken)
			}
			got := Repair([]byte(tt.broken))
			if !json.Valid(got) {
				t.Fatalf("Repair(%q) = %q, not valid JSON", tt.broken, got)
			}
		})
	}
}

// TestRepair_DoesNotTouchStringContent proves the scanner is string-literal
// aware: whitespace, commas, and number-shaped substrings inside quoted JSON
// string values must survive untouched.
func TestRepair_DoesNotTouchStringContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"prose with spaced numbers", `{"note": "value is 0. 92 approx"}`},
		{"prose with trailing comma text", `{"note": "a, b, c,"}`},
		{"escaped quotes", `{"note": "she said \"0. 92\" exactly"}`},
		{"already valid compact", `{"a":1,"b":[1,2,3]}`},
		{"already valid spaced", `{"a": 1, "b": [1, 2, 3]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !json.Valid([]byte(tt.input)) {
				t.Fatalf("test setup bug: %q must be valid JSON", tt.input)
			}
			got := Repair([]byte(tt.input))
			if string(got) != tt.input {
				t.Errorf("Repair(%q) = %q, want unchanged (valid JSON must pass through as-is)", tt.input, got)
			}
		})
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"already valid", `{"a": 1}`, true},
		{"repairable", `{"a": - 5}`, true},
		{"truly broken", `{"a": {not json}`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Valid([]byte(tt.input)); got != tt.want {
				t.Errorf("Valid(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTryUnmarshal(t *testing.T) {
	t.Run("valid JSON unmarshals directly", func(t *testing.T) {
		var v struct {
			A int `json:"a"`
		}
		if err := TryUnmarshal([]byte(`{"a": 1}`), &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.A != 1 {
			t.Errorf("A = %d, want 1", v.A)
		}
	})

	t.Run("repairable JSON unmarshals after retry", func(t *testing.T) {
		var v struct {
			A float64 `json:"a"`
		}
		if err := TryUnmarshal([]byte(`{"a": - 5}`), &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.A != -5 {
			t.Errorf("A = %v, want -5", v.A)
		}
	})

	t.Run("truly broken JSON returns the original error", func(t *testing.T) {
		var v map[string]any
		broken := []byte(`{"a": {not json}`)
		firstErr := json.Unmarshal(broken, &v)
		if firstErr == nil {
			t.Fatalf("test setup bug: expected broken to fail unmarshal")
		}
		err := TryUnmarshal(broken, &v)
		if err == nil {
			t.Fatalf("expected an error for truly malformed JSON")
		}
		if err.Error() != firstErr.Error() {
			t.Errorf("TryUnmarshal error = %v, want original error %v", err, firstErr)
		}
	})
}

func TestFixArguments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"already valid unchanged", `{"a":1}`, `{"a":1}`},
		{"repaired", `{"a": - 5}`, `{"a": -5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FixArguments(tt.raw)
			if got != tt.want {
				t.Errorf("FixArguments(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	t.Run("unrepairable returns original unchanged", func(t *testing.T) {
		raw := `{not json at all`
		got := FixArguments(raw)
		if got != raw {
			t.Errorf("FixArguments(%q) = %q, want unchanged original", raw, got)
		}
	})
}
