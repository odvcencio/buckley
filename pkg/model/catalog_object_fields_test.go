package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeCatalogObjectFields_PreservesLexemesAndNestedValues(t *testing.T) {
	body := []byte(`{
  "id": "vendor/model",
  "underflow": 1e-999,
  "escaped": "\u0041\\literal",
  "pricing": { "prompt": "0.00000100", "completion": "0" },
  "future": [null, {"nested":1,"nested":2}],
  "supported_parameters": null,
  "ID": "case-distinct"
}`)
	fields, err := decodeCatalogObjectFields(body)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]json.RawMessage{
		"id":                   json.RawMessage(`"vendor/model"`),
		"underflow":            json.RawMessage(`1e-999`),
		"escaped":              json.RawMessage(`"\u0041\\literal"`),
		"pricing":              json.RawMessage(`{ "prompt": "0.00000100", "completion": "0" }`),
		"future":               json.RawMessage(`[null, {"nested":1,"nested":2}]`),
		"supported_parameters": json.RawMessage(`null`),
		"ID":                   json.RawMessage(`"case-distinct"`),
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("raw catalog fields changed: got %q, want %q", fields, want)
	}
}

func TestModelInfoUnmarshal_PreservesCaseAliasSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, body, id string
		params         []string
		prompt         float64
	}{
		{"lowercase_wins_sorted_spelling", `{"id":"lower","ID":"upper"}`, "lower", nil, 0},
		{"same_precedence_in_reverse_order", `{"ID":"upper","id":"lower"}`, "lower", nil, 0},
		{"pricing_alias", `{"id":"alias","Pricing":{"prompt":"0.000001","completion":"0.000002"}}`, "alias", nil, 1},
		{"parameters_alias", `{"id":"alias","Supported_Parameters":["tools"]}`, "alias", []string{"tools"}, 0},
		{"unicode_case_alias", `{"id":"alias","ſupported_parameters":["tools"]}`, "alias", []string{"tools"}, 0},
		{"malformed_canonical_overrides_alias", `{"id":"mixed","Supported_Parameters":["tools"],"supported_parameters":["reasoning",null],"x_buckley_supported_parameters_complete":true}`, "mixed", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var info ModelInfo
			if err := json.Unmarshal([]byte(tc.body), &info); err != nil {
				t.Fatal(err)
			}
			if info.ID != tc.id || !reflect.DeepEqual(info.SupportedParameters, tc.params) || info.Pricing.Prompt != tc.prompt {
				t.Fatalf("case-alias catalog behavior changed: %+v", info)
			}
			if info.supportedParametersComplete || info.PricingKnown || len(info.RawPricingJSON) != 0 {
				t.Fatalf("case aliases or malformed metadata granted new authority: %+v", info)
			}
		})
	}
}

func TestModelInfoUnmarshal_PreservesLargeFutureCompatibilityFields(t *testing.T) {
	future := `{"future":"` + strings.Repeat("x", 1<<20) + `"}`
	body := `{"id":"future/model","pricing":` + future + `,"supported_parameters":` + future + `,"top_provider":{"max_completion_tokens":8192}}`
	var info ModelInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "future/model" || info.MaxCompletionTokens != 8192 || string(info.RawPricingJSON) != future {
		t.Fatal("ordinary fields or exact future pricing value were lost")
	}
	if info.PricingKnown || info.supportedParametersComplete || len(info.SupportedParameters) != 0 {
		t.Fatal("future metadata granted price or capability authority")
	}
}

func TestDecodeCatalogObjectFields_RejectsDuplicatesAndInvalidObjects(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"duplicate_id", `{"id":"a","id":"b"}`},
		{"escaped_duplicate", `{"id":"a","\u0069d":"b"}`},
		{"duplicate_null", `{"pricing":null,"pricing":{}}`},
		{"array", `[]`}, {"null", `null`}, {"string", `"object"`}, {"number", `1`},
		{"truncated", `{"id":"a"`}, {"malformed_value", `{"pricing":[}`},
		{"trailing_object", `{"id":"a"}{}`}, {"trailing_scalar", `{} true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := decodeCatalogObjectFields([]byte(tc.body))
			if err == nil || fields != nil {
				t.Fatalf("invalid catalog object accepted: fields=%q err=%v", fields, err)
			}
		})
	}
}
