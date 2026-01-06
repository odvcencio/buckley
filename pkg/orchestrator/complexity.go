package orchestrator

import (
	"regexp"
	"strings"
)

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
	Threshold float64 // Score above this triggers planning mode (default: 0.6)
}

// NewComplexityDetector creates a detector with default settings
func NewComplexityDetector() *ComplexityDetector {
	return &ComplexityDetector{
		Threshold: 0.5, // Err on the side of planning when uncertain
	}
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

	// Token/length analysis - longer inputs often indicate complex tasks
	wordCount := len(strings.Fields(input))
	if wordCount > 100 {
		signal.Score += 0.25
		signal.Reasons = append(signal.Reasons, "lengthy input (>100 words)")
	} else if wordCount > 50 {
		signal.Score += 0.15
		signal.Reasons = append(signal.Reasons, "moderate length input (>50 words)")
	}

	// Ambiguity signals - uncertainty often indicates need for clarification
	ambiguityPatterns := []struct {
		pattern string
		weight  float64
	}{
		{"maybe", 0.15},
		{"possibly", 0.15},
		{"not sure", 0.2},
		{"decide", 0.1},
		{"should we", 0.15},
		{"should i", 0.15},
		{"what if", 0.1},
		{"which one", 0.1},
		{"either", 0.1},
		{"or we could", 0.15},
		{"alternatively", 0.1},
		{"what do you think", 0.15},
		{"how should", 0.1},
	}

	lowerInput := strings.ToLower(input)
	for _, p := range ambiguityPatterns {
		if strings.Contains(lowerInput, p.pattern) {
			signal.Score += p.weight
			signal.Reasons = append(signal.Reasons, "ambiguity signal: "+p.pattern)
		}
	}

	// Multi-step language - suggests task has multiple phases
	// Using word boundaries to avoid false positives (e.g., "then" in "authentication")
	multiStepPatterns := []struct {
		pattern string
		weight  float64
	}{
		{"first,", 0.1},
		{"first ", 0.1},
		{" then ", 0.1},
		{". then", 0.1},
		{"after that", 0.15},
		{"finally", 0.1},
		{" next ", 0.05},
		{"next,", 0.05},
		{" steps", 0.15},
		{"phases", 0.15},
		{" stage", 0.1},
		{"before we", 0.1},
		{"once we", 0.1},
	}

	for _, p := range multiStepPatterns {
		if strings.Contains(lowerInput, p.pattern) {
			signal.Score += p.weight
			signal.Reasons = append(signal.Reasons, "multi-step language: "+p.pattern)
		}
	}

	// Question count - multiple questions suggest exploration needed
	questionCount := strings.Count(input, "?")
	if questionCount >= 4 {
		signal.Score += 0.35
		signal.Reasons = append(signal.Reasons, "many questions (4+)")
	} else if questionCount >= 3 {
		signal.Score += 0.25
		signal.Reasons = append(signal.Reasons, "multiple questions (3)")
	} else if questionCount >= 2 {
		signal.Score += 0.15
		signal.Reasons = append(signal.Reasons, "multiple questions (2)")
	}

	// Feature/task keywords that often indicate larger scope
	scopeKeywords := []struct {
		pattern string
		weight  float64
	}{
		{"implement", 0.15},
		{"refactor", 0.2},
		{"redesign", 0.25},
		{"migrate", 0.2},
		{"integrate", 0.15},
		{"add support for", 0.15},
		{"build a", 0.15},
		{"create a", 0.1},
		{"system", 0.1},
		{"architecture", 0.2},
		{"across", 0.1},
		{"multiple files", 0.15},
		{"codebase", 0.1},
	}

	for _, p := range scopeKeywords {
		if strings.Contains(lowerInput, p.pattern) {
			signal.Score += p.weight
			signal.Reasons = append(signal.Reasons, "scope keyword: "+p.pattern)
		}
	}

	// File path mentions - multiple files suggest coordinated changes
	filePathPattern := regexp.MustCompile(`(?:^|[^/\w])(?:[\w-]+/)+[\w.-]+\.\w+`)
	fileMatches := filePathPattern.FindAllString(input, -1)
	if len(fileMatches) >= 3 {
		signal.Score += 0.2
		signal.Reasons = append(signal.Reasons, "multiple file paths mentioned (3+)")
	} else if len(fileMatches) >= 2 {
		signal.Score += 0.1
		signal.Reasons = append(signal.Reasons, "multiple file paths mentioned (2)")
	}

	// Context-based signals
	if context != nil {
		// Images suggest visual context that may need exploration
		if context.HasImages {
			signal.Score += 0.1
			signal.Reasons = append(signal.Reasons, "visual context provided")
		}

		// Recent errors suggest debugging session
		if context.RecentErrors > 0 {
			signal.Score += 0.15
			signal.Reasons = append(signal.Reasons, "recent errors in context")
		}

		// Large affected scope
		if context.EstimatedFiles > 5 {
			signal.Score += 0.2
			signal.Reasons = append(signal.Reasons, "large estimated file scope (>5)")
		}
	}

	// Cap score at 1.0
	if signal.Score > 1.0 {
		signal.Score = 1.0
	}

	// Determine recommended mode
	threshold := d.Threshold
	if threshold <= 0 {
		threshold = 0.6
	}

	if signal.Score >= threshold {
		signal.Recommended = PlanningMode
	} else {
		signal.Recommended = DirectExecution
	}

	return signal
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
