// Package filepicker provides fuzzy file search with @ trigger for inline file selection.
package filepicker

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// fuzzyMatch performs fuzzy matching with scoring.
// Returns score (0 = no match) and highlight positions.
func fuzzyMatch(path, query string) (int, []int) {
	if query == "" {
		return 1, nil // Empty query matches everything
	}

	ctx := newFuzzyContext(path, query)
	state := newFuzzyMatchState(query)

	for pathIdx := range ctx.pathRunes {
		if state.matchedAll(ctx) {
			break
		}
		if ctx.pathRunes[pathIdx] == ctx.queryRunes[state.queryIndex] {
			state.recordMatch(ctx, pathIdx)
		}
	}

	if !state.matchedAll(ctx) {
		return 0, nil
	}

	return state.finalScore(ctx), state.highlights
}

type fuzzyContext struct {
	path          string
	queryLower    string
	filename      string
	pathRunes     []rune
	queryRunes    []rune
	originalRunes []rune
	filenameStart int
}

func newFuzzyContext(path, query string) fuzzyContext {
	pathLower := strings.ToLower(path)
	queryLower := strings.ToLower(query)
	filename := strings.ToLower(filepath.Base(path))
	pathRunes := []rune(pathLower)

	return fuzzyContext{
		path:          path,
		queryLower:    queryLower,
		filename:      filename,
		pathRunes:     pathRunes,
		queryRunes:    []rune(queryLower),
		originalRunes: []rune(path),
		filenameStart: len(pathRunes) - len([]rune(filename)),
	}
}

type fuzzyMatchState struct {
	queryIndex  int
	lastMatch   int
	consecutive int
	score       int
	highlights  []int
}

func newFuzzyMatchState(query string) fuzzyMatchState {
	return fuzzyMatchState{
		lastMatch:  -1,
		highlights: make([]int, 0, len([]rune(query))),
	}
}

func (s *fuzzyMatchState) matchedAll(ctx fuzzyContext) bool {
	return s.queryIndex >= len(ctx.queryRunes)
}

func (s *fuzzyMatchState) recordMatch(ctx fuzzyContext, pathIdx int) {
	s.highlights = append(s.highlights, pathIdx)
	s.score += s.matchScore(ctx, pathIdx)
	s.lastMatch = pathIdx
	s.queryIndex++
}

func (s *fuzzyMatchState) matchScore(ctx fuzzyContext, pathIdx int) int {
	score := 10
	if s.lastMatch == pathIdx-1 {
		s.consecutive++
		score += s.consecutive * 5
	} else {
		s.consecutive = 0
	}
	if isWordStart(ctx.pathRunes, pathIdx) {
		score += 20
	}
	if isCamelBoundary(ctx.originalRunes, pathIdx) {
		score += 15
	}
	if pathIdx >= ctx.filenameStart {
		score += 25
	}
	if pathIdx == s.queryIndex {
		score += 30
	}
	return score
}

func (s fuzzyMatchState) finalScore(ctx fuzzyContext) int {
	score := s.score
	score -= strings.Count(ctx.path, "/") * 2
	score -= len(ctx.pathRunes) / 5
	if strings.HasPrefix(ctx.filename, ctx.queryLower) {
		score += 50
	}
	return score
}

func isWordStart(pathRunes []rune, idx int) bool {
	return idx == 0 || pathRunes[idx-1] == '/' || pathRunes[idx-1] == '_' ||
		pathRunes[idx-1] == '-' || pathRunes[idx-1] == '.'
}

func isCamelBoundary(originalRunes []rune, idx int) bool {
	return idx > 0 && idx < len(originalRunes) &&
		unicode.IsLower(originalRunes[idx-1]) && unicode.IsUpper(originalRunes[idx])
}

// MultiPatternMatch supports space-separated patterns (AND logic).
// Example: "api hand" matches "pkg/api/handler.go"
func MultiPatternMatch(path string, patterns []string) (int, []int) {
	if len(patterns) == 0 {
		return 1, nil
	}

	totalScore := 0
	allHighlights := make([]int, 0)

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		score, highlights := fuzzyMatch(path, pattern)
		if score == 0 {
			return 0, nil // All patterns must match
		}
		totalScore += score
		allHighlights = append(allHighlights, highlights...)
	}

	return totalScore, uniqueSortedHighlights(allHighlights)
}

func uniqueSortedHighlights(highlights []int) []int {
	seen := make(map[int]bool, len(highlights))
	unique := make([]int, 0, len(highlights))
	for _, highlight := range highlights {
		if seen[highlight] {
			continue
		}
		seen[highlight] = true
		unique = append(unique, highlight)
	}
	slices.Sort(unique)
	return unique
}
