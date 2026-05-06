package orchestrator

import (
	"regexp"
	"strings"

	"github.com/odvcencio/buckley/pkg/rules"
)

// defaultComplexitySignal returns a conservative default for when no arbiter
// engine is configured. Direct execution with a low confidence score.
func defaultComplexitySignal() *ComplexitySignal {
	return &ComplexitySignal{
		Score:       0.3,
		Reasons:     []string{"no arbiter engine configured"},
		Recommended: DirectExecution,
	}
}

// ComplexityMode represents whether to use direct execution or planning mode
type ComplexityMode int

const (
	// DirectExecution means the task is simple enough to execute directly
	DirectExecution ComplexityMode = iota
	// PlanningMode means the task is complex and warrants brainstorming
	PlanningMode
)

// ComplexitySignal contains the result of complexity analysis
type ComplexitySignal struct {
	Score       float64        // 0.0 - 1.0, higher = more complex
	Reasons     []string       // Human-readable reasons for the score
	Recommended ComplexityMode // DirectExecution or PlanningMode
}

// ComplexityDetector analyzes input to determine if planning mode is warranted
type ComplexityDetector struct {
	Threshold float64       // Score above this triggers planning mode (default: 0.6)
	engine    *rules.Engine // Optional arbiter rules engine
}

// ComplexityOption configures the complexity detector.
type ComplexityOption func(*ComplexityDetector)

// WithRulesEngine sets the arbiter rules engine for complexity evaluation.
func WithRulesEngine(e *rules.Engine) ComplexityOption {
	return func(d *ComplexityDetector) {
		d.engine = e
	}
}

// NewComplexityDetector creates a detector with default settings
func NewComplexityDetector(opts ...ComplexityOption) *ComplexityDetector {
	d := &ComplexityDetector{
		Threshold: 0.5, // Err on the side of planning when uncertain
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Analyze examines input text and context to determine complexity
func (d *ComplexityDetector) Analyze(input string, context *AnalysisContext) *ComplexitySignal {
	signal := &ComplexitySignal{
		Score:   0.0,
		Reasons: []string{},
	}

	input = strings.TrimSpace(input)
	if input == "" {
		signal.Recommended = DirectExecution
		return signal
	}

	// Use arbiter rules engine if available
	if d.engine != nil {
		if result := d.evalArbiter(input); result != nil {
			return result
		}
	}

	// No engine or arbiter returned no matches — conservative default
	return defaultComplexitySignal()
}

// evalArbiter attempts complexity evaluation via the arbiter rules engine.
// Returns nil if evaluation fails or yields no matches, signalling the caller
// to fall through to heuristic scoring.
func (d *ComplexityDetector) evalArbiter(input string) *ComplexitySignal {
	lowerInput := strings.ToLower(input)

	// Build TaskFacts from input text
	facts := rules.TaskFacts{
		WordCount:    len(strings.Fields(input)),
		HasFilePaths: regexp.MustCompile(`(?:^|[^/\w])(?:[\w-]+/)+[\w.-]+\.\w+`).MatchString(input),
		HasQuestions: strings.Contains(input, "?"),
		Ambiguity:    computeAmbiguity(lowerInput),
	}

	matches, err := rules.Eval(d.engine, "complexity", facts)
	if err != nil || len(matches) == 0 {
		return nil
	}

	// The first match is highest priority (arbiter sorts by priority desc)
	top := matches[0]

	signal := &ComplexitySignal{
		Reasons: []string{"arbiter:" + top.Name},
	}

	// Map action to mode
	switch top.Action {
	case "Plan":
		signal.Recommended = PlanningMode
	default:
		signal.Recommended = DirectExecution
	}

	// Extract confidence as score
	if conf, ok := top.Params["confidence"]; ok {
		switch v := conf.(type) {
		case float64:
			signal.Score = v
		case int:
			signal.Score = float64(v)
		}
	}

	return signal
}

// computeAmbiguity returns a 0.0-1.0 ambiguity score based on hedging language.
func computeAmbiguity(lower string) float64 {
	hedges := []string{
		"maybe", "possibly", "not sure", "should we", "should i",
		"what if", "which one", "either", "or we could",
		"alternatively", "what do you think", "how should",
	}
	count := 0
	for _, h := range hedges {
		if strings.Contains(lower, h) {
			count++
		}
	}
	// Normalize: 3+ hedges → 1.0
	score := float64(count) / 3.0
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// AnalysisContext provides additional context for complexity analysis
type AnalysisContext struct {
	HasImages      bool     // Whether visual content was provided
	RecentErrors   int      // Number of recent errors in conversation
	EstimatedFiles int      // Estimated number of files that may be affected
	ProjectType    string   // Type of project (go, node, python, etc.)
	RecentTools    []string // Recently used tools (may indicate ongoing work)
}

// IsComplex returns true if the signal recommends planning mode
func (s *ComplexitySignal) IsComplex() bool {
	return s.Recommended == PlanningMode
}

// Summary returns a human-readable summary of the complexity analysis
func (s *ComplexitySignal) Summary() string {
	if len(s.Reasons) == 0 {
		return "Simple task - no complexity signals detected"
	}

	mode := "direct execution"
	if s.Recommended == PlanningMode {
		mode = "planning mode"
	}

	return strings.Join(s.Reasons, ", ") + " → " + mode
}
