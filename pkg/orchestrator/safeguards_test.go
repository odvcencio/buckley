package orchestrator

import (
	"m31labs.dev/buckley/pkg/rules"
	"strings"
	"testing"
)

func TestRiskLevel_String(t *testing.T) {
	tests := []struct {
		level    RiskLevel
		expected string
	}{
		{RiskNone, "none"},
		{RiskLow, "low"},
		{RiskMedium, "medium"},
		{RiskHigh, "high"},
		{RiskCritical, "critical"},
		{RiskLevel(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.level.String(); got != tt.expected {
				t.Errorf("RiskLevel.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestNewRiskDetector(t *testing.T) {
	detector := NewRiskDetector()
	if detector == nil {
		t.Fatal("Expected non-nil detector")
	}
}

func TestRiskDetector_Analyze_Destructive(t *testing.T) {
	// With arbiter engine, destructive commands are detected by risk rules
	engine := mustNewRulesEngine(t)
	detector := NewRiskDetector(WithRiskRulesEngine(engine))

	tests := []struct {
		name          string
		text          string
		expectedLevel RiskLevel
		expectPause   bool
	}{
		{
			name:          "rm -rf",
			text:          "rm -rf /some/path",
			expectedLevel: RiskHigh,
			expectPause:   true,
		},
		{
			name:          "force push",
			text:          "git push --force",
			expectedLevel: RiskCritical,
			expectPause:   true,
		},
		{
			name:          "hard reset",
			text:          "git reset --hard",
			expectedLevel: RiskCritical,
			expectPause:   true,
		},
		{
			name:          "drop table",
			text:          "DROP TABLE users",
			expectedLevel: RiskHigh,
			expectPause:   true,
		},
		{
			name:          "safe operation",
			text:          "cat /etc/hosts",
			expectedLevel: RiskNone,
			expectPause:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assessment := detector.Analyze(tt.text)

			if assessment.Level != tt.expectedLevel {
				t.Errorf("Level = %v, want %v (reasons: %v)", assessment.Level, tt.expectedLevel, assessment.Reasons)
			}
			if assessment.RequiresPause != tt.expectPause {
				t.Errorf("RequiresPause = %v, want %v", assessment.RequiresPause, tt.expectPause)
			}
		})
	}
}

func TestRiskDetector_AnalyzeApproach(t *testing.T) {
	detector := NewRiskDetector()

	tests := []struct {
		name          string
		approachName  string
		description   string
		tradeoffs     []string
		expectedLevel RiskLevel
	}{
		{
			name:          "irreversible operation",
			approachName:  "Database Migration",
			description:   "This operation is irreversible and cannot be undone",
			tradeoffs:     []string{"permanent changes"},
			expectedLevel: RiskHigh,
		},
		{
			name:          "data loss warning",
			approachName:  "Cleanup",
			description:   "Delete old records to free space",
			tradeoffs:     []string{"potential data loss"},
			expectedLevel: RiskMedium,
		},
		{
			name:          "safe approach",
			approachName:  "Refactor",
			description:   "Update code style",
			tradeoffs:     []string{"takes time"},
			expectedLevel: RiskNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assessment := detector.AnalyzeApproach(tt.approachName, tt.description, tt.tradeoffs)

			if assessment.Level < tt.expectedLevel {
				t.Errorf("Level = %v, want at least %v", assessment.Level, tt.expectedLevel)
			}
		})
	}
}

// TestRiskDetector_Analyze_ViaArbiter tests the arbiter-backed risk detection path.
func TestRiskDetector_Analyze_ViaArbiter(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewRiskDetector(WithRiskRulesEngine(engine))

	// These commands match arbiter rules exactly (starts_with or in-list exact match).
	// Commands with trailing args (e.g. "git reset --hard HEAD~1") may fall through
	// to pattern-based analysis since the DestructiveGit rule uses exact `in` matching.
	tests := []struct {
		name      string
		command   string
		wantLevel RiskLevel
		wantPause bool
	}{
		{
			// DestructiveGit rule: is_git_op && command in [...] — exact match
			name:      "git reset --hard exact match is Block (critical)",
			command:   "git reset --hard",
			wantLevel: RiskCritical,
			wantPause: true,
		},
		{
			// DestructiveGit rule: is_git_op && command in [...] — exact match
			name:      "git push --force exact match is Block (critical)",
			command:   "git push --force",
			wantLevel: RiskCritical,
			wantPause: true,
		},
		{
			// RmRecursive rule: command starts_with "rm -r"
			name:      "rm -r is Pause (high)",
			command:   "rm -r ./old-dir",
			wantLevel: RiskHigh,
			wantPause: true,
		},
		{
			// DangerousOps rule: command starts_with "DROP "
			name:      "DROP is Pause (high)",
			command:   "DROP TABLE users",
			wantLevel: RiskHigh,
			wantPause: true,
		},
		{
			// SafeRead rule: command starts_with "git status"
			name:      "git status is Allow (none)",
			command:   "git status",
			wantLevel: RiskNone,
			wantPause: false,
		},
		{
			// SafeRead rule: command starts_with "go test"
			name:      "go test is Allow (none)",
			command:   "go test ./...",
			wantLevel: RiskNone,
			wantPause: false,
		},
		{
			// SafeRead rule: command starts_with "cat "
			name:      "cat file is Allow (none)",
			command:   "cat /etc/hosts",
			wantLevel: RiskNone,
			wantPause: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assessment := detector.Analyze(tt.command)

			if assessment.Level != tt.wantLevel {
				t.Errorf("Level = %v, want %v (reasons: %v)", assessment.Level, tt.wantLevel, assessment.Reasons)
			}
			if assessment.RequiresPause != tt.wantPause {
				t.Errorf("RequiresPause = %v, want %v", assessment.RequiresPause, tt.wantPause)
			}
			// Arbiter results should have "arbiter:" prefix in reasons
			foundArbiter := false
			for _, r := range assessment.Reasons {
				if strings.HasPrefix(r, "arbiter:") {
					foundArbiter = true
					break
				}
			}
			if !foundArbiter {
				t.Errorf("Expected arbiter-prefixed reason, got: %v", assessment.Reasons)
			}
		})
	}
}

// TestRiskDetector_Analyze_NilEngineFallback tests that nil engine returns RiskNone default.
func TestRiskDetector_Analyze_NilEngineFallback(t *testing.T) {
	// Without engine — returns conservative RiskNone default
	detector := NewRiskDetector()

	assessment := detector.Analyze("rm -rf /some/path")
	if assessment.Level != RiskNone {
		t.Errorf("Nil-engine fallback: Level = %v, want RiskNone", assessment.Level)
	}

	// Reasons should NOT have arbiter prefix
	for _, r := range assessment.Reasons {
		if strings.HasPrefix(r, "arbiter:") {
			t.Errorf("Nil-engine fallback should not produce arbiter reasons, got: %s", r)
		}
	}
}

// TestRiskDetector_WithRiskRulesEngineOption tests that the option sets the engine field.
func TestRiskDetector_WithRiskRulesEngineOption(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewRiskDetector(WithRiskRulesEngine(engine))

	if detector.engine == nil {
		t.Fatal("Expected engine to be set via WithRiskRulesEngine option")
	}
}

// TestRiskDetector_Analyze_ArbiterNoMatchReturnsDefault tests that when arbiter returns no matches
// the detector returns the conservative RiskNone default.
func TestRiskDetector_Analyze_ArbiterNoMatchReturnsDefault(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewRiskDetector(WithRiskRulesEngine(engine))

	// This text won't match any arbiter rule — returns default
	text := "api_key value to commit to repository"
	assessment := detector.Analyze(text)

	if assessment.Level != RiskNone {
		t.Errorf("Expected RiskNone default for unmatched text, got %v", assessment.Level)
	}
}

func mustNewRulesEngine(t *testing.T) *rules.Engine {
	t.Helper()
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("failed to create rules engine: %v", err)
	}
	return engine
}
