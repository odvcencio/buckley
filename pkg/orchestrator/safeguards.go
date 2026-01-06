package orchestrator

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// RiskLevel represents the severity of a detected risk
type RiskLevel int

const (
	RiskNone RiskLevel = iota
	RiskLow
	RiskMedium
	RiskHigh
	RiskCritical
)

// String returns a human-readable risk level
func (r RiskLevel) String() string {
	switch r {
	case RiskNone:
		return "none"
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// RiskAssessment contains the result of risk analysis
type RiskAssessment struct {
	Level         RiskLevel
	Reasons       []string
	RequiresPause bool // True if long-run mode should pause for confirmation
	Suggestions   []string
}

// RiskDetector analyzes operations for potential risks
type RiskDetector struct {
	patterns []riskPattern
}

type riskPattern struct {
	pattern     *regexp.Regexp
	level       RiskLevel
	description string
	suggestion  string
}

// NewRiskDetector creates a risk detector with default patterns
func NewRiskDetector() *RiskDetector {
	return &RiskDetector{
		patterns: []riskPattern{
			// Critical: Irreversible destructive operations
			{
				pattern:     regexp.MustCompile(`(?i)\b(rm\s+-rf|drop\s+database|truncate\s+table|delete\s+from\s+\w+\s+where\s+1=1)\b`),
				level:       RiskCritical,
				description: "destructive command detected",
				suggestion:  "Review carefully before executing",
			},
			{
				pattern:     regexp.MustCompile(`(?i)\bgit\s+push\s+.*--force\b`),
				level:       RiskCritical,
				description: "force push detected",
				suggestion:  "Force push can overwrite remote history",
			},
			{
				pattern:     regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`),
				level:       RiskHigh,
				description: "hard reset detected",
				suggestion:  "This will discard uncommitted changes",
			},
			// High: Operations affecting production or credentials
			{
				pattern:     regexp.MustCompile(`(?i)\b(production|prod)\b.*\b(deploy|push|update|delete)\b`),
				level:       RiskHigh,
				description: "production operation detected",
				suggestion:  "Verify you're targeting the correct environment",
			},
			{
				pattern:     regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|token|credential)\b.*\b(commit|push|write|save)\b`),
				level:       RiskHigh,
				description: "potential credential exposure",
				suggestion:  "Ensure secrets are not being committed",
			},
			// Medium: Bulk operations
			{
				pattern:     regexp.MustCompile(`(?i)\b(all|every|each)\s+(file|record|row|document)s?\b`),
				level:       RiskMedium,
				description: "bulk operation detected",
				suggestion:  "Consider processing in batches",
			},
			{
				pattern:     regexp.MustCompile(`(?i)\bdelete\b.*\bwhere\b`),
				level:       RiskMedium,
				description: "delete with condition detected",
				suggestion:  "Verify the WHERE clause is correct",
			},
			// Low: Operations that might need review
			{
				pattern:     regexp.MustCompile(`(?i)\b(refactor|rewrite|replace)\s+(all|every|entire)\b`),
				level:       RiskLow,
				description: "large-scale refactoring detected",
				suggestion:  "Consider incremental changes",
			},
		},
	}
}

// Analyze examines text for potential risks
func (d *RiskDetector) Analyze(text string) *RiskAssessment {
	assessment := &RiskAssessment{
		Level:   RiskNone,
		Reasons: []string{},
	}

	for _, p := range d.patterns {
		if p.pattern.MatchString(text) {
			if p.level > assessment.Level {
				assessment.Level = p.level
			}
			assessment.Reasons = append(assessment.Reasons, p.description)
			if p.suggestion != "" {
				assessment.Suggestions = append(assessment.Suggestions, p.suggestion)
			}
		}
	}

	// Require pause for high/critical risks
	assessment.RequiresPause = assessment.Level >= RiskHigh

	return assessment
}

// AnalyzeApproach examines a planning approach for risks
func (d *RiskDetector) AnalyzeApproach(name, description string, tradeoffs []string) *RiskAssessment {
	// Combine all text for analysis
	combined := name + " " + description + " " + strings.Join(tradeoffs, " ")
	assessment := d.Analyze(combined)

	// Check for risk indicators in approach metadata
	lowerDesc := strings.ToLower(description)
	if strings.Contains(lowerDesc, "irreversible") || strings.Contains(lowerDesc, "cannot be undone") {
		if assessment.Level < RiskHigh {
			assessment.Level = RiskHigh
		}
		assessment.Reasons = append(assessment.Reasons, "irreversible operation")
		assessment.RequiresPause = true
	}

	if strings.Contains(lowerDesc, "data loss") || strings.Contains(lowerDesc, "delete") {
		if assessment.Level < RiskMedium {
			assessment.Level = RiskMedium
		}
		assessment.Reasons = append(assessment.Reasons, "potential data loss")
	}

	return assessment
}

// LongRunGuard manages safeguards for autonomous operation
type LongRunGuard struct {
	mu sync.Mutex

	// Configuration
	maxDuration     time.Duration
	checkInInterval time.Duration
	pauseOnRisk     bool

	// State
	startTime       time.Time
	lastCheckIn     time.Time
	isPaused        bool
	pauseReason     string
	totalOperations int
	riskEvents      []RiskEvent
}

// RiskEvent records a risk detection during long-run mode
type RiskEvent struct {
	Timestamp  time.Time
	Assessment *RiskAssessment
	Context    string
	Proceeded  bool
}

// NewLongRunGuard creates a new long-run guard with the given settings
func NewLongRunGuard(maxMinutes, checkInMinutes int, pauseOnRisk bool) *LongRunGuard {
	if maxMinutes <= 0 {
		maxMinutes = 30
	}
	if checkInMinutes <= 0 {
		checkInMinutes = 10
	}

	return &LongRunGuard{
		maxDuration:     time.Duration(maxMinutes) * time.Minute,
		checkInInterval: time.Duration(checkInMinutes) * time.Minute,
		pauseOnRisk:     pauseOnRisk,
		startTime:       time.Now(),
		lastCheckIn:     time.Now(),
	}
}

// Start begins the long-run session
func (g *LongRunGuard) Start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.startTime = time.Now()
	g.lastCheckIn = time.Now()
	g.isPaused = false
	g.pauseReason = ""
	g.totalOperations = 0
	g.riskEvents = nil
}

// RecordOperation records that an operation was performed
func (g *LongRunGuard) RecordOperation() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.totalOperations++
}

// RecordRisk records a risk event
func (g *LongRunGuard) RecordRisk(assessment *RiskAssessment, context string, proceeded bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.riskEvents = append(g.riskEvents, RiskEvent{
		Timestamp:  time.Now(),
		Assessment: assessment,
		Context:    context,
		Proceeded:  proceeded,
	})
}

// CheckIn performs a check-in, returns true if should continue
func (g *LongRunGuard) CheckIn() (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()

	// Check if paused
	if g.isPaused {
		return false, g.pauseReason
	}

	// Check max duration
	elapsed := now.Sub(g.startTime)
	if elapsed >= g.maxDuration {
		g.isPaused = true
		g.pauseReason = "maximum duration reached"
		return false, g.pauseReason
	}

	// Update last check-in
	g.lastCheckIn = now

	return true, ""
}

// NeedsCheckIn returns true if a check-in is due
func (g *LongRunGuard) NeedsCheckIn() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Since(g.lastCheckIn) >= g.checkInInterval
}

// Pause pauses the long-run session
func (g *LongRunGuard) Pause(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.isPaused = true
	g.pauseReason = reason
}

// Resume resumes the long-run session
func (g *LongRunGuard) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.isPaused = false
	g.pauseReason = ""
	g.lastCheckIn = time.Now()
}

// IsPaused returns whether the session is paused
func (g *LongRunGuard) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.isPaused
}

// PauseReason returns the reason for pause
func (g *LongRunGuard) PauseReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pauseReason
}

// ShouldPauseForRisk checks if a risk should cause a pause
func (g *LongRunGuard) ShouldPauseForRisk(assessment *RiskAssessment) bool {
	if !g.pauseOnRisk {
		return false
	}
	return assessment.RequiresPause
}

// Status returns the current guard status
func (g *LongRunGuard) Status() LongRunStatus {
	g.mu.Lock()
	defer g.mu.Unlock()

	return LongRunStatus{
		StartTime:       g.startTime,
		Elapsed:         time.Since(g.startTime),
		Remaining:       g.maxDuration - time.Since(g.startTime),
		IsPaused:        g.isPaused,
		PauseReason:     g.pauseReason,
		TotalOperations: g.totalOperations,
		RiskEvents:      len(g.riskEvents),
		LastCheckIn:     g.lastCheckIn,
	}
}

// LongRunStatus contains the current status of a long-run session
type LongRunStatus struct {
	StartTime       time.Time
	Elapsed         time.Duration
	Remaining       time.Duration
	IsPaused        bool
	PauseReason     string
	TotalOperations int
	RiskEvents      int
	LastCheckIn     time.Time
}

// GetRiskEvents returns all recorded risk events
func (g *LongRunGuard) GetRiskEvents() []RiskEvent {
	g.mu.Lock()
	defer g.mu.Unlock()

	events := make([]RiskEvent, len(g.riskEvents))
	copy(events, g.riskEvents)
	return events
}
