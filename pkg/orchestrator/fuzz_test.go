package orchestrator

import (
	"testing"
)

// FuzzSanitizeIdentifier fuzzes the identifier sanitization function
func FuzzSanitizeIdentifier(f *testing.F) {
	// Seed corpus with interesting test cases
	f.Add("my-feature-name")
	f.Add("Feature With Spaces")
	f.Add("special@#$chars")
	f.Add("")
	f.Add("   ")
	f.Add("123-numeric-start")
	f.Add("UPPERCASE")
	f.Add("mixedCase")
	f.Add("unicode-café")
	f.Add("emoji-😀-test")

	f.Fuzz(func(t *testing.T, input string) {
		result := SanitizeIdentifier(input)

		// Invariants that must hold:
		// 1. Result should not contain spaces
		if len(result) > 0 {
			for _, ch := range result {
				if ch == ' ' {
					t.Errorf("SanitizeIdentifier(%q) returned result with spaces: %q", input, result)
				}
			}
		}

		// 2. Result should be deterministic
		result2 := SanitizeIdentifier(input)
		if result != result2 {
			t.Errorf("SanitizeIdentifier(%q) is not deterministic: %q != %q", input, result, result2)
		}

		// 3. Function should handle empty input gracefully (may return empty string)
		// This is acceptable behavior - the function doesn't guarantee non-empty output
	})
}

// FuzzPlanIDValidation fuzzes plan ID validation in LoadPlan
func FuzzPlanIDValidation(f *testing.F) {
	// Seed corpus
	f.Add("valid-plan-123")
	f.Add("")
	f.Add("   ")
	f.Add("../../../etc/passwd")
	f.Add("plan/../other")
	f.Add("plan\x00null")
	f.Add("plan\nwithnewline")

	f.Fuzz(func(t *testing.T, planID string) {
		tempDir := t.TempDir()
		store := NewFilePlanStore(tempDir)

		// Should not panic regardless of input
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("LoadPlan(%q) panicked: %v", planID, r)
			}
		}()

		_, err := store.LoadPlan(planID)

		// Empty or whitespace-only IDs should always return error
		trimmed := ""
		for _, ch := range planID {
			if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
				trimmed += string(ch)
			}
		}

		if trimmed == "" {
			if err == nil {
				t.Errorf("LoadPlan(%q) should return error for empty/whitespace-only ID", planID)
			}
		}
	})
}
