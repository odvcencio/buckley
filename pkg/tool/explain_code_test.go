package tool

import (
	"strings"
	"testing"
)

func TestExplainCode_BudgetPreservesStructuredModelOutput(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		SetResultEncoding(useToon)
		t.Cleanup(func() { SetResultEncoding(true) })
		for _, code := range []string{strings.Repeat("数字\"\x00\n", 2000), strings.Repeat("\n", 10000)} {
			registry := NewRegistry()
			native, ok := registry.Get("explain_code")
			if !ok {
				t.Fatal("explain_code is not registered")
			}
			native.(interface{ SetMaxOutputBytes(int) }).SetMaxOutputBytes(1024)
			result, err := registry.Execute("explain_code", map[string]any{"code": code})
			if err != nil || !result.Success {
				t.Fatalf("explain: %+v %v", result, err)
			}
			output, err := ToModelOutputWithLimit(result, 1024)
			if err != nil || len(output) > 1024 || !strings.Contains(output, "code_truncated") || !strings.Contains(output, "keyword_heuristic") || strings.Contains(output, "output_head") {
				t.Fatalf("lost structured explanation: %s %v", output, err)
			}
		}
	}
}
