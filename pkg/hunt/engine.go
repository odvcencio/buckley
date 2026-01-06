package hunt

import (
	"fmt"
	"path/filepath"
	"sort"
)

// Engine manages the hunt process
type Engine struct {
	rootPath  string
	analyzers []Analyzer
	cache     *Cache
}

// NewEngine creates a new hunt engine
func NewEngine(rootPath string) *Engine {
	return &Engine{
		rootPath:  rootPath,
		analyzers: []Analyzer{},
		cache:     NewCache(filepath.Join(rootPath, ".buckley", "cache")),
	}
}

// AddAnalyzer registers an analyzer
func (e *Engine) AddAnalyzer(analyzer Analyzer) {
	e.analyzers = append(e.analyzers, analyzer)
}

// Scan runs all analyzers and returns suggestions
func (e *Engine) Scan() ([]ImprovementSuggestion, error) {
	// Check cache first
	commitSHA, err := getCurrentCommit(e.rootPath)
	if err == nil {
		if cached, ok := e.cache.Get(commitSHA); ok {
			return cached, nil
		}
	}

	suggestions := []ImprovementSuggestion{}

	// Run analyzers
	for _, analyzer := range e.analyzers {
		results, err := analyzer.Analyze(e.rootPath)
		if err != nil {
			// Log error but continue with other analyzers
			fmt.Printf("Warning: analyzer %s failed: %v\n", analyzer.Name(), err)
			continue
		}

		suggestions = append(suggestions, results...)
	}

	// Score and sort
	suggestions = e.scoreAndSort(suggestions)

	// Cache results
	if commitSHA != "" {
		_ = e.cache.Set(commitSHA, suggestions) // Best-effort caching
	}

	return suggestions, nil
}

// scoreAndSort sorts suggestions by severity and effort
func (e *Engine) scoreAndSort(suggestions []ImprovementSuggestion) []ImprovementSuggestion {
	// Calculate composite score: higher severity + lower effort = higher priority
	type scoredSuggestion struct {
		suggestion ImprovementSuggestion
		score      float64
	}

	scored := make([]scoredSuggestion, len(suggestions))
	for i, sug := range suggestions {
		effortScore := 0.0
		switch sug.Effort {
		case "trivial":
			effortScore = 4.0
		case "small":
			effortScore = 3.0
		case "medium":
			effortScore = 2.0
		case "large":
			effortScore = 1.0
		}

		// Composite score: severity * effort multiplier
		score := float64(sug.Severity) * effortScore
		scored[i] = scoredSuggestion{suggestion: sug, score: score}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Extract sorted suggestions
	sorted := make([]ImprovementSuggestion, len(scored))
	for i, s := range scored {
		sorted[i] = s.suggestion
	}

	return sorted
}

// GetAnalyzers returns all registered analyzers
func (e *Engine) GetAnalyzers() []Analyzer {
	return e.analyzers
}
