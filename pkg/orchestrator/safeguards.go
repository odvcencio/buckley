package orchestrator

import (
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/rules"
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
	engine *rules.Engine // Optional arbiter rules engine
}

// RiskDetectorOption configures the risk detector.
type RiskDetectorOption func(*RiskDetector)

// WithRiskRulesEngine sets the arbiter rules engine for risk evaluation.
func WithRiskRulesEngine(e *rules.Engine) RiskDetectorOption {
	return func(d *RiskDetector) {
		d.engine = e
	}
}

// NewRiskDetector creates a risk detector
func NewRiskDetector(opts ...RiskDetectorOption) *RiskDetector {
	d := &RiskDetector{}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Analyze examines text for potential risks
func (d *RiskDetector) Analyze(text string) *RiskAssessment {
	// Use arbiter rules engine if available
	if d.engine != nil {
		if result := d.evalArbiterRisk(text); result != nil {
			return result
		}
	}

	// No engine or arbiter returned no matches — conservative default
	return &RiskAssessment{
		Level:   RiskNone,
		Reasons: []string{},
	}
}

// evalArbiterRisk attempts risk evaluation via the arbiter rules engine.
// Returns nil if evaluation fails or yields no matches, signalling the caller
// to fall through to pattern-based analysis.
func (d *RiskDetector) evalArbiterRisk(text string) *RiskAssessment {
	// Build CommandFacts with pre-processed boolean flags
	facts := rules.CommandFacts{
		Command:       text,
		IsGitOp:       strings.HasPrefix(text, "git "),
		IsForceOp:     strings.Contains(text, "--force") || strings.Contains(text, "-f"),
		IsRmRecursive: strings.Contains(text, "rm -r"),
	}

	matches, err := rules.Eval(d.engine, "risk", facts)
	if err != nil || len(matches) == 0 {
		return nil
	}

	// The first match is highest priority (arbiter sorts by priority desc)
	top := matches[0]

	// Map action label to RiskLevel
	var level RiskLevel
	switch top.Action {
	case "Block":
		level = RiskCritical
	case "Pause":
		level = RiskHigh
	case "Allow":
		level = RiskNone
	default:
		// Unknown action — fall through to pattern-based logic
		return nil
	}

	assessment := &RiskAssessment{
		Level:         level,
		Reasons:       []string{"arbiter:" + top.Name},
		RequiresPause: level >= RiskHigh,
	}

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
