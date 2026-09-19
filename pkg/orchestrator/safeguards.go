package orchestrator

import (
	"strings"

	"m31labs.dev/buckley/pkg/rules"
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
