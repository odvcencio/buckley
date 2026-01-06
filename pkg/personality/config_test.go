package personality

import (
	"testing"
)

func TestConfigFromYAML(t *testing.T) {
	tests := []struct {
		name             string
		enabled          bool
		quirkProbability float64
		tone             string
		wantEnabled      bool
		wantProbability  float64
		wantTone         string
	}{
		{
			name:             "valid config",
			enabled:          true,
			quirkProbability: 0.5,
			tone:             "friendly",
			wantEnabled:      true,
			wantProbability:  0.5,
			wantTone:         "friendly",
		},
		{
			name:             "empty tone defaults to friendly",
			enabled:          true,
			quirkProbability: 0.3,
			tone:             "",
			wantEnabled:      true,
			wantProbability:  0.3,
			wantTone:         "friendly",
		},
		{
			name:             "invalid tone defaults to friendly",
			enabled:          true,
			quirkProbability: 0.2,
			tone:             "invalid",
			wantEnabled:      true,
			wantProbability:  0.2,
			wantTone:         "friendly",
		},
		{
			name:             "negative probability clamped to 0",
			enabled:          true,
			quirkProbability: -0.5,
			tone:             "friendly",
			wantEnabled:      true,
			wantProbability:  0,
			wantTone:         "friendly",
		},
		{
			name:             "probability > 1 clamped to 1",
			enabled:          true,
			quirkProbability: 1.5,
			tone:             "friendly",
			wantEnabled:      true,
			wantProbability:  1,
			wantTone:         "friendly",
		},
		{
			name:             "professional tone",
			enabled:          true,
			quirkProbability: 0.1,
			tone:             "professional",
			wantEnabled:      true,
			wantProbability:  0.1,
			wantTone:         "professional",
		},
		{
			name:             "quirky tone",
			enabled:          true,
			quirkProbability: 0.3,
			tone:             "quirky",
			wantEnabled:      true,
			wantProbability:  0.3,
			wantTone:         "quirky",
		},
		{
			name:             "disabled",
			enabled:          false,
			quirkProbability: 0.5,
			tone:             "friendly",
			wantEnabled:      false,
			wantProbability:  0.5,
			wantTone:         "friendly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ConfigFromYAML(tt.enabled, tt.quirkProbability, tt.tone)

			if config.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", config.Enabled, tt.wantEnabled)
			}

			if config.QuirkProbability != tt.wantProbability {
				t.Errorf("QuirkProbability = %v, want %v", config.QuirkProbability, tt.wantProbability)
			}

			if config.Tone != tt.wantTone {
				t.Errorf("Tone = %v, want %v", config.Tone, tt.wantTone)
			}
		})
	}
}
