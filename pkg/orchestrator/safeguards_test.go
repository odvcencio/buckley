package orchestrator

import (
	"strings"
	"testing"
	"time"
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

func TestNewLongRunGuard(t *testing.T) {
	// Test default values
	guard := NewLongRunGuard(0, 0, true)
	if guard.maxDuration != 30*time.Minute {
		t.Errorf("Expected default maxDuration 30m, got %v", guard.maxDuration)
	}
	if guard.checkInInterval != 10*time.Minute {
		t.Errorf("Expected default checkInInterval 10m, got %v", guard.checkInInterval)
	}

	// Test custom values
	guard = NewLongRunGuard(60, 15, false)
	if guard.maxDuration != 60*time.Minute {
		t.Errorf("Expected maxDuration 60m, got %v", guard.maxDuration)
	}
	if guard.checkInInterval != 15*time.Minute {
		t.Errorf("Expected checkInInterval 15m, got %v", guard.checkInInterval)
	}
	if guard.pauseOnRisk {
		t.Error("Expected pauseOnRisk to be false")
	}
}

func TestLongRunGuard_Start(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	if guard.isPaused {
		t.Error("Expected isPaused to be false after Start()")
	}
	if guard.totalOperations != 0 {
		t.Errorf("Expected totalOperations to be 0, got %d", guard.totalOperations)
	}
	if len(guard.riskEvents) != 0 {
		t.Errorf("Expected riskEvents to be empty, got %d", len(guard.riskEvents))
	}
}

func TestLongRunGuard_RecordOperation(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	guard.RecordOperation()
	guard.RecordOperation()
	guard.RecordOperation()

	if guard.totalOperations != 3 {
		t.Errorf("Expected totalOperations to be 3, got %d", guard.totalOperations)
	}
}

func TestLongRunGuard_RecordRisk(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	assessment := &RiskAssessment{
		Level:   RiskHigh,
		Reasons: []string{"test risk"},
	}

	guard.RecordRisk(assessment, "test context", true)
	guard.RecordRisk(assessment, "another context", false)

	events := guard.GetRiskEvents()
	if len(events) != 2 {
		t.Fatalf("Expected 2 risk events, got %d", len(events))
	}

	if events[0].Context != "test context" {
		t.Errorf("Expected context 'test context', got %q", events[0].Context)
	}
	if !events[0].Proceeded {
		t.Error("Expected first event to have proceeded=true")
	}
	if events[1].Proceeded {
		t.Error("Expected second event to have proceeded=false")
	}
}

func TestLongRunGuard_CheckIn(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	// Should continue normally
	shouldContinue, reason := guard.CheckIn()
	if !shouldContinue {
		t.Errorf("Expected to continue, but got reason: %s", reason)
	}

	// Pause the guard
	guard.Pause("manual pause")
	shouldContinue, reason = guard.CheckIn()
	if shouldContinue {
		t.Error("Expected to not continue after pause")
	}
	if reason != "manual pause" {
		t.Errorf("Expected reason 'manual pause', got %q", reason)
	}
}

func TestLongRunGuard_PauseResume(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	if guard.IsPaused() {
		t.Error("Expected not paused initially")
	}

	guard.Pause("test reason")

	if !guard.IsPaused() {
		t.Error("Expected to be paused")
	}
	if guard.PauseReason() != "test reason" {
		t.Errorf("Expected reason 'test reason', got %q", guard.PauseReason())
	}

	guard.Resume()

	if guard.IsPaused() {
		t.Error("Expected not paused after resume")
	}
	if guard.PauseReason() != "" {
		t.Errorf("Expected empty reason after resume, got %q", guard.PauseReason())
	}
}

func TestLongRunGuard_ShouldPauseForRisk(t *testing.T) {
	// With pauseOnRisk enabled
	guard := NewLongRunGuard(30, 10, true)

	lowRisk := &RiskAssessment{Level: RiskLow, RequiresPause: false}
	highRisk := &RiskAssessment{Level: RiskHigh, RequiresPause: true}

	if guard.ShouldPauseForRisk(lowRisk) {
		t.Error("Expected not to pause for low risk")
	}
	if !guard.ShouldPauseForRisk(highRisk) {
		t.Error("Expected to pause for high risk")
	}

	// With pauseOnRisk disabled
	guard = NewLongRunGuard(30, 10, false)
	if guard.ShouldPauseForRisk(highRisk) {
		t.Error("Expected not to pause when pauseOnRisk is disabled")
	}
}

func TestLongRunGuard_Status(t *testing.T) {
	guard := NewLongRunGuard(30, 10, true)
	guard.Start()

	guard.RecordOperation()
	guard.RecordOperation()

	assessment := &RiskAssessment{Level: RiskMedium}
	guard.RecordRisk(assessment, "test", true)

	status := guard.Status()

	if status.TotalOperations != 2 {
		t.Errorf("Expected 2 operations, got %d", status.TotalOperations)
	}
	if status.RiskEvents != 1 {
		t.Errorf("Expected 1 risk event, got %d", status.RiskEvents)
	}
	if status.IsPaused {
		t.Error("Expected not paused")
	}
}

func TestLongRunGuard_NeedsCheckIn(t *testing.T) {
	// Use very short interval for testing
	guard := &LongRunGuard{
		checkInInterval: 1 * time.Millisecond,
		lastCheckIn:     time.Now().Add(-10 * time.Millisecond),
	}

	if !guard.NeedsCheckIn() {
		t.Error("Expected to need check-in after interval elapsed")
	}

	guard.lastCheckIn = time.Now()
	if guard.NeedsCheckIn() {
		t.Error("Expected not to need check-in immediately after update")
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
