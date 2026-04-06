// Package filepicker provides fuzzy file search with @ trigger for inline file selection.
package filepicker

import (
	"path/filepath"
	"strings"
	"unicode"
)

// fuzzyMatch performs fuzzy matching with scoring.
// Returns score (0 = no match) and highlight positions.
func fuzzyMatch(path, query string) (int, []int) {
	if query == "" {
		return 1, nil // Empty query matches everything
	}

	pathLower := strings.ToLower(path)
	queryLower := strings.ToLower(query)

	// Extract filename for bonus scoring
	filename := strings.ToLower(filepath.Base(path))
	filenameStart := len(pathLower) - len(filename)

	score := 0
	highlights := make([]int, 0, len(query))

	pathRunes := []rune(pathLower)
	queryRunes := []rune(queryLower)
	originalRunes := []rune(path)

	qi := 0         // Query index
	lastMatch := -1 // Last matched position
	consecutive := 0

	for pi := 0; pi < len(pathRunes) && qi < len(queryRunes); pi++ {
		if pathRunes[pi] == queryRunes[qi] {
			highlights = append(highlights, pi)

			// Scoring bonuses
			baseScore := 10

			// Consecutive match bonus
			if lastMatch == pi-1 {
				consecutive++
				baseScore += consecutive * 5
			} else {
				consecutive = 0
			}

			// Start of word bonus (after / or _ or - or .)
			if pi == 0 || pathRunes[pi-1] == '/' || pathRunes[pi-1] == '_' ||
				pathRunes[pi-1] == '-' || pathRunes[pi-1] == '.' {
				baseScore += 20
			}

			// CamelCase bonus
			if pi > 0 && pi < len(originalRunes) &&
				unicode.IsLower(rune(originalRunes[pi-1])) && unicode.IsUpper(originalRunes[pi]) {
				baseScore += 15
			}

			// Filename match bonus (matches in filename worth more)
			if pi >= filenameStart {
				baseScore += 25
			}

			// Exact prefix match bonus
			if pi == qi {
				baseScore += 30
			}

			score += baseScore
			lastMatch = pi
			qi++
		}
	}

	// All query chars must match
	if qi < len(queryRunes) {
		return 0, nil
	}

	// Bonus for shorter paths (prefer less nested)
	depthPenalty := strings.Count(path, "/") * 2
	score -= depthPenalty

	// Bonus for shorter overall length
	score -= len(path) / 5

	// Bonus for exact filename match
	if strings.HasPrefix(filename, queryLower) {
		score += 50
	}

	return score, highlights
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

	// Remove duplicates and sort
	seen := make(map[int]bool)
	unique := make([]int, 0, len(allHighlights))
	for _, h := range allHighlights {
		if !seen[h] {
			seen[h] = true
			unique = append(unique, h)
		}
	}

	// Sort highlights
	for i := 0; i < len(unique)-1; i++ {
		for j := i + 1; j < len(unique); j++ {
			if unique[j] < unique[i] {
				unique[i], unique[j] = unique[j], unique[i]
			}
		}
	}

	return totalScore, unique
}
