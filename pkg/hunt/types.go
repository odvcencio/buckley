package hunt

// ImprovementSuggestion represents a suggested code improvement
type ImprovementSuggestion struct {
	ID          string
	Category    string // "bug-risk", "readability", "dependency", "docs", "tech-debt"
	Severity    int    // 1-10
	File        string
	LineStart   int
	LineEnd     int
	Snippet     string
	Rationale   string
	Effort      string // "trivial", "small", "medium", "large"
	AutoFixable bool
}

// Analyzer interface for pluggable analyzers
type Analyzer interface {
	Name() string
	Analyze(rootPath string) ([]ImprovementSuggestion, error)
}

// AnalyzerResult holds the result from an analyzer
type AnalyzerResult struct {
	AnalyzerName string
	Suggestions  []ImprovementSuggestion
	Error        error
}
