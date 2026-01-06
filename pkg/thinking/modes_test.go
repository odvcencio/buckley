package thinking

import (
	"testing"
)

func TestModeString(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeNormal, "normal"},
		{ModeThink, "think"},
		{ModeThinkHard, "think-hard"},
		{ModeThinkHarder, "think-harder"},
		{ModeUltrathink, "ultrathink"},
		{Mode(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModeLabel(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeNormal, ""},
		{ModeThink, "Thinking"},
		{ModeThinkHard, "Thinking Hard"},
		{ModeThinkHarder, "Thinking Harder"},
		{ModeUltrathink, "Ultrathinking"},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			if got := tt.mode.Label(); got != tt.want {
				t.Errorf("Mode.Label() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectMode(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMode  Mode
		wantClean string
	}{
		// Ultrathink detection
		{
			name:      "ultrathink keyword",
			input:     "ultrathink about this problem",
			wantMode:  ModeUltrathink,
			wantClean: "about this problem",
		},
		{
			name:      "ULTRATHINK uppercase",
			input:     "ULTRATHINK analyze the code",
			wantMode:  ModeUltrathink,
			wantClean: "analyze the code",
		},

		// Think harder detection
		{
			name:      "think harder",
			input:     "think harder about the architecture",
			wantMode:  ModeThinkHarder,
			wantClean: "about the architecture",
		},
		{
			name:      "Think Harder mixed case",
			input:     "Think Harder please and solve this",
			wantMode:  ModeThinkHarder,
			wantClean: "please and solve this",
		},

		// Think hard detection
		{
			name:      "think hard",
			input:     "think hard and fix the bug",
			wantMode:  ModeThinkHard,
			wantClean: "and fix the bug",
		},
		{
			name:      "Think Hard at end",
			input:     "solve this problem, think hard",
			wantMode:  ModeThinkHard,
			wantClean: "solve this problem,",
		},

		// Think detection (directive patterns)
		{
			name:      "think at start",
			input:     "think, then implement the feature",
			wantMode:  ModeThink,
			wantClean: ", then implement the feature",
		},
		{
			name:      "please think",
			input:     "please think carefully and solve this",
			wantMode:  ModeThink,
			wantClean: "please  carefully and solve this", // Double space from regex replacement
		},

		// Natural language (should NOT trigger)
		{
			name:      "i think natural",
			input:     "I think we should use a database",
			wantMode:  ModeNormal,
			wantClean: "I think we should use a database",
		},
		{
			name:      "what do you think",
			input:     "what do you think about this?",
			wantMode:  ModeNormal,
			wantClean: "what do you think about this?",
		},
		{
			name:      "think about",
			input:     "let's think about the design",
			wantMode:  ModeNormal,
			wantClean: "let's think about the design",
		},
		{
			name:      "think that",
			input:     "I don't think that will work",
			wantMode:  ModeNormal,
			wantClean: "I don't think that will work",
		},

		// No thinking keyword
		{
			name:      "no keyword",
			input:     "implement the new feature",
			wantMode:  ModeNormal,
			wantClean: "implement the new feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotClean := DetectMode(tt.input)
			if gotMode != tt.wantMode {
				t.Errorf("DetectMode(%q) mode = %v, want %v", tt.input, gotMode, tt.wantMode)
			}
			// For mode detection, we only check that non-normal modes clean up
			if tt.wantMode != ModeNormal && gotClean != tt.wantClean {
				t.Errorf("DetectMode(%q) clean = %q, want %q", tt.input, gotClean, tt.wantClean)
			}
		})
	}
}

func TestParseModeCommand(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"/think", ModeThink},
		{"/think hard", ModeThinkHard},
		{"/think harder", ModeThinkHarder},
		{"/think ultra", ModeUltrathink},
		{"/THINK", ModeThink},
		{"/Think Hard", ModeThinkHard},
		{"/thinking", ModeNormal}, // Not a valid command
		{"/help", ModeNormal},
		{"think", ModeNormal}, // Missing slash
		{"", ModeNormal},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseModeCommand(tt.input)
			if got != tt.want {
				t.Errorf("ParseModeCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestModeGetConfig(t *testing.T) {
	tests := []struct {
		mode            Mode
		wantEffort      string
		wantMultiplier  float64
		wantBudget      int
		wantHasAddition bool
	}{
		{ModeNormal, "", 1.0, 0, false},
		{ModeThink, "medium", 1.5, 4000, true},
		{ModeThinkHard, "high", 2.0, 8000, true},
		{ModeThinkHarder, "high", 2.5, 12000, true},
		{ModeUltrathink, "high", 3.0, 16000, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			cfg := tt.mode.GetConfig()

			if cfg.ReasoningEffort != tt.wantEffort {
				t.Errorf("GetConfig() ReasoningEffort = %v, want %v", cfg.ReasoningEffort, tt.wantEffort)
			}
			if cfg.MaxTokensMultiplier != tt.wantMultiplier {
				t.Errorf("GetConfig() MaxTokensMultiplier = %v, want %v", cfg.MaxTokensMultiplier, tt.wantMultiplier)
			}
			if cfg.BudgetTokens != tt.wantBudget {
				t.Errorf("GetConfig() BudgetTokens = %v, want %v", cfg.BudgetTokens, tt.wantBudget)
			}
			hasAddition := cfg.SystemPromptAddition != ""
			if hasAddition != tt.wantHasAddition {
				t.Errorf("GetConfig() has SystemPromptAddition = %v, want %v", hasAddition, tt.wantHasAddition)
			}
		})
	}
}

func TestIsThinkingDirective(t *testing.T) {
	directives := []string{
		"think, then solve",
		"please think carefully",
		"now think deeply",
		"First think. Then act.",
		"and think carefully",
	}

	for _, d := range directives {
		if !isThinkingDirective(d) {
			t.Errorf("isThinkingDirective(%q) = false, want true", d)
		}
	}

	notDirectives := []string{
		"I think we should",
		"what do you think",
		"don't think so",
		"thinking about it",
		"think about options",
		"think of something",
		"now think about this", // "think about" is natural
	}

	for _, nd := range notDirectives {
		if isThinkingDirective(nd) {
			t.Errorf("isThinkingDirective(%q) = true, want false", nd)
		}
	}
}
