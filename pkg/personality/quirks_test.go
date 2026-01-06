package personality

import (
	"errors"
	"strings"
	"testing"
)

func TestNewManager(t *testing.T) {
	config := DefaultConfig()
	manager := NewManager(config)

	if manager == nil {
		t.Fatal("NewManager should return non-nil manager")
	}

	if manager.config.Enabled != config.Enabled {
		t.Error("Manager should use provided config")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if !config.Enabled {
		t.Error("Default config should have personality enabled")
	}

	if config.QuirkProbability <= 0 || config.QuirkProbability > 1 {
		t.Errorf("QuirkProbability should be 0-1, got %f", config.QuirkProbability)
	}

	if config.Tone != "friendly" {
		t.Errorf("Default tone should be 'friendly', got %q", config.Tone)
	}
}

func TestApplyQuirk_Disabled(t *testing.T) {
	config := Config{
		Enabled:          false,
		QuirkProbability: 1.0,
		Tone:             "friendly",
	}

	manager := NewManager(config)
	original := "Test message"
	result := manager.ApplyQuirk(original, ContextSuccess)

	if result != original {
		t.Error("ApplyQuirk should not modify message when disabled")
	}
}

func TestApplyQuirk_ContextExists(t *testing.T) {
	config := Config{
		Enabled:          true,
		QuirkProbability: 1.0, // Always apply
		Tone:             "friendly",
	}

	manager := NewManager(config)
	original := "Test message"
	result := manager.ApplyQuirk(original, ContextSuccess)

	// Should have quirk added
	if result == original {
		t.Error("ApplyQuirk should modify message when enabled with high probability")
	}

	// Should contain original message
	if !strings.Contains(result, original) {
		t.Error("ApplyQuirk should preserve original message")
	}
}

func TestApplyQuirk_InvalidContext(t *testing.T) {
	config := Config{
		Enabled:          true,
		QuirkProbability: 1.0,
		Tone:             "friendly",
	}

	manager := NewManager(config)
	original := "Test message"
	result := manager.ApplyQuirk(original, Context("nonexistent"))

	// Should return original for unknown context
	if result != original {
		t.Error("ApplyQuirk should return original for unknown context")
	}
}

func TestApplyQuirk_Tones(t *testing.T) {
	tones := []string{"professional", "friendly", "quirky"}

	for _, tone := range tones {
		config := Config{
			Enabled:          true,
			QuirkProbability: 1.0,
			Tone:             tone,
		}

		manager := NewManager(config)
		original := "Test message"
		result := manager.ApplyQuirk(original, ContextSuccess)

		// All tones should preserve original message
		if !strings.Contains(result, original) {
			t.Errorf("Tone %q should preserve original message", tone)
		}
	}
}

func TestGetTonePrefix(t *testing.T) {
	tests := []struct {
		tone    string
		context Context
	}{
		{"professional", ContextSuccess},
		{"friendly", ContextSuccess},
		{"quirky", ContextSuccess},
		{"professional", ContextError},
		{"friendly", ContextError},
		{"quirky", ContextError},
	}

	for _, tt := range tests {
		config := Config{
			Enabled: true,
			Tone:    tt.tone,
		}

		manager := NewManager(config)
		prefix := manager.GetTonePrefix(tt.context)

		// Prefix should be a string (may be empty for professional)
		if tt.tone == "professional" && prefix != "" {
			t.Errorf("Professional tone should have empty prefix, got %q", prefix)
		}

		// Non-professional tones should have prefixes for success/error
		if tt.tone != "professional" && (tt.context == ContextSuccess || tt.context == ContextError) {
			if prefix == "" {
				t.Errorf("Tone %q with context %v should have prefix", tt.tone, tt.context)
			}
		}
	}
}

func TestGetTonePrefix_Disabled(t *testing.T) {
	config := Config{
		Enabled: false,
		Tone:    "quirky",
	}

	manager := NewManager(config)
	prefix := manager.GetTonePrefix(ContextSuccess)

	if prefix != "" {
		t.Error("Disabled personality should return empty prefix")
	}
}

func TestWrapError(t *testing.T) {
	config := Config{
		Enabled:          true,
		QuirkProbability: 1.0,
		Tone:             "friendly",
	}

	manager := NewManager(config)

	// Test nil error
	result := manager.WrapError(nil)
	if result != "" {
		t.Error("WrapError should return empty string for nil error")
	}

	// Test actual error
	err := errors.New("test error")
	result = manager.WrapError(err)

	// Should contain error message
	if !strings.Contains(result, "test error") {
		t.Error("WrapError should preserve error message")
	}
}

func TestWrapError_Disabled(t *testing.T) {
	config := Config{
		Enabled: false,
		Tone:    "friendly",
	}

	manager := NewManager(config)
	err := errors.New("test error")
	result := manager.WrapError(err)

	if result != "test error" {
		t.Error("WrapError should not modify error when disabled")
	}
}

func TestGreetUser(t *testing.T) {
	tones := []string{"professional", "friendly", "quirky"}

	for _, tone := range tones {
		config := Config{
			Enabled: true,
			Tone:    tone,
		}

		manager := NewManager(config)
		greeting := manager.GreetUser()

		if greeting == "" {
			t.Errorf("GreetUser should return non-empty string for tone %q", tone)
		}

		// Professional should be simple
		if tone == "professional" && strings.Contains(greeting, "🐕") {
			t.Error("Professional tone should not have dog emojis in greeting")
		}

		// Quirky should have some personality (at least one marker)
		if tone == "quirky" {
			hasPersonality := strings.Contains(greeting, "🐕") ||
				strings.Contains(greeting, "*") ||
				strings.Contains(greeting, "!") ||
				strings.Contains(greeting, "Woof")
			if !hasPersonality {
				t.Error("Quirky tone should have personality markers in greeting")
			}
		}
	}
}

func TestGreetUser_Disabled(t *testing.T) {
	config := Config{
		Enabled: false,
		Tone:    "quirky",
	}

	manager := NewManager(config)
	greeting := manager.GreetUser()

	// Should be simple when disabled
	if greeting == "" {
		t.Error("GreetUser should return non-empty string even when disabled")
	}

	// Should not have quirky elements
	if strings.Contains(greeting, "🐕") || strings.Contains(greeting, "*") {
		t.Error("Disabled personality should not have quirky elements")
	}
}

func TestFarewellUser(t *testing.T) {
	tones := []string{"professional", "friendly", "quirky"}

	for _, tone := range tones {
		config := Config{
			Enabled: true,
			Tone:    tone,
		}

		manager := NewManager(config)
		farewell := manager.FarewellUser()

		if farewell == "" {
			t.Errorf("FarewellUser should return non-empty string for tone %q", tone)
		}

		// Professional should be simple
		if tone == "professional" && (strings.Contains(farewell, "🐕") || strings.Contains(farewell, "*")) {
			t.Error("Professional tone should not have personality markers in farewell")
		}
	}
}

func TestFarewellUser_Disabled(t *testing.T) {
	config := Config{
		Enabled: false,
		Tone:    "quirky",
	}

	manager := NewManager(config)
	farewell := manager.FarewellUser()

	if farewell == "" {
		t.Error("FarewellUser should return non-empty string even when disabled")
	}

	// Should not have quirky elements
	if strings.Contains(farewell, "🐕") || strings.Contains(farewell, "*") {
		t.Error("Disabled personality should not have quirky elements")
	}
}

func TestQuirkBank_Coverage(t *testing.T) {
	// Ensure all important contexts have quirks
	requiredContexts := []Context{
		ContextSuccess,
		ContextError,
		ContextComplete,
		ContextThinking,
		ContextGreeting,
	}

	for _, ctx := range requiredContexts {
		quirks, exists := QuirkBank[ctx]
		if !exists {
			t.Errorf("QuirkBank should have quirks for context %v", ctx)
			continue
		}

		if len(quirks) == 0 {
			t.Errorf("QuirkBank should have at least one quirk for context %v", ctx)
		}

		// Check quirk structure
		for i, quirk := range quirks {
			if quirk.Text == "" {
				t.Errorf("Quirk %d for context %v has empty text", i, ctx)
			}
			if quirk.Probability <= 0 || quirk.Probability > 1 {
				t.Errorf("Quirk %d for context %v has invalid probability %f", i, ctx, quirk.Probability)
			}
		}
	}
}

func TestFormatWithQuirk_PreservesMessage(t *testing.T) {
	config := Config{
		Enabled: true,
		Tone:    "friendly",
	}

	manager := NewManager(config)
	quirk := Quirk{Text: "*test quirk*", Probability: 1.0}
	original := "Original message"

	result := manager.formatWithQuirk(original, quirk)

	if !strings.Contains(result, original) {
		t.Error("formatWithQuirk should preserve original message")
	}

	if !strings.Contains(result, quirk.Text) {
		t.Error("formatWithQuirk should include quirk text")
	}
}

func TestApplyQuirk_MultipleContexts(t *testing.T) {
	config := Config{
		Enabled:          true,
		QuirkProbability: 1.0,
		Tone:             "friendly",
	}

	manager := NewManager(config)
	contexts := []Context{
		ContextSuccess,
		ContextError,
		ContextComplete,
		ContextThinking,
		ContextGreeting,
		ContextHelp,
		ContextInfo,
	}

	for _, ctx := range contexts {
		result := manager.ApplyQuirk("Test", ctx)
		// Should either add quirk or return original
		if result != "Test" && !strings.Contains(result, "Test") {
			t.Errorf("ApplyQuirk for context %v corrupted message", ctx)
		}
	}
}
