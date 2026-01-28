// Package main provides semantic widget matching for self-healing tests.
//
// The matcher uses multiple strategies to find widgets even when UI changes:
// 1. Exact role + label match
// 2. Fuzzy label similarity (Levenshtein distance)
// 3. Keyword matching against labels, descriptions, and values
// 4. Context-aware matching using neighbor widgets
// 5. Spatial matching (location-based heuristics)
package main

import (
	"math"
	"strings"
	"unicode"
)

// MatchStrategy defines how to match widgets.
type MatchStrategy int

const (
	// MatchExact requires exact match
	MatchExact MatchStrategy = iota
	// MatchFuzzy allows fuzzy string matching
	MatchFuzzy
	// MatchKeyword matches if keywords are present
	MatchKeyword
	// MatchSpatial uses location heuristics
	MatchSpatial
)

// MatchCriteria defines what to look for in a widget.
type MatchCriteria struct {
	Role           string
	Label          string
	Description    string
	Keywords       []string
	Strategy       MatchStrategy
	MinConfidence  float64 // 0.0 to 1.0
	NearWidgetID   string  // For spatial matching
	PreferredArea  string  // "top", "bottom", "left", "right", "center"
}

// MatchResult represents a successful match with confidence score.
type MatchResult struct {
	Widget     *WidgetInfo
	Confidence float64
	Strategy   string
}

// FindWidgetSemantic finds a widget using self-healing semantic matching.
// It tries multiple strategies and returns the best match.
func FindWidgetSemantic(snap *Snapshot, criteria MatchCriteria) *MatchResult {
	candidates := collectWidgets(snap.Widgets)
	
	var results []MatchResult

	// Strategy 1: Exact match
	if exact := matchExact(candidates, criteria); exact != nil {
		results = append(results, *exact)
	}

	// Strategy 2: Fuzzy label matching
	if criteria.Label != "" {
		if fuzzy := matchFuzzy(candidates, criteria); fuzzy != nil && fuzzy.Confidence >= criteria.MinConfidence {
			results = append(results, *fuzzy)
		}
	}

	// Strategy 3: Keyword matching
	if len(criteria.Keywords) > 0 {
		if keyword := matchKeywords(candidates, criteria); keyword != nil && keyword.Confidence >= criteria.MinConfidence {
			results = append(results, *keyword)
		}
	}

	// Strategy 4: Role-only with spatial preference
	if criteria.Role != "" && criteria.PreferredArea != "" {
		if spatial := matchSpatial(candidates, criteria, snap.Width, snap.Height); spatial != nil {
			results = append(results, *spatial)
		}
	}

	if len(results) == 0 {
		return nil
	}

	// Return best match
	best := results[0]
	for _, r := range results[1:] {
		if r.Confidence > best.Confidence {
			best = r
		}
	}

	return &best
}

// collectWidgets flattens the widget tree into a slice.
func collectWidgets(widgets []WidgetInfo) []WidgetInfo {
	var result []WidgetInfo
	for i := range widgets {
		result = append(result, widgets[i])
		result = append(result, collectWidgets(widgets[i].Children)...)
	}
	return result
}

// matchExact finds exact role/label matches.
func matchExact(widgets []WidgetInfo, criteria MatchCriteria) *MatchResult {
	for i := range widgets {
		w := &widgets[i]
		roleMatch := criteria.Role == "" || strings.EqualFold(w.Role, criteria.Role)
		labelMatch := criteria.Label == "" || strings.EqualFold(w.Label, criteria.Label)
		
		if roleMatch && labelMatch {
			return &MatchResult{
				Widget:     w,
				Confidence: 1.0,
				Strategy:   "exact",
			}
		}
	}
	return nil
}

// matchFuzzy uses fuzzy string similarity for labels.
func matchFuzzy(widgets []WidgetInfo, criteria MatchCriteria) *MatchResult {
	if criteria.Label == "" {
		return nil
	}

	targetLower := strings.ToLower(criteria.Label)
	var best *WidgetInfo
	var bestScore float64

	for i := range widgets {
		w := &widgets[i]
		if criteria.Role != "" && !strings.EqualFold(w.Role, criteria.Role) {
			continue
		}

		// Check label similarity
		if w.Label != "" {
			sim := similarity(targetLower, strings.ToLower(w.Label))
			if sim > bestScore {
				bestScore = sim
				best = w
			}
		}

		// Check description similarity
		if w.Description != "" {
			sim := similarity(targetLower, strings.ToLower(w.Description))
			if sim > bestScore {
				bestScore = sim
				best = w
			}
		}

		// Check value similarity for inputs
		if w.Value != "" {
			sim := similarity(targetLower, strings.ToLower(w.Value))
			if sim > bestScore {
				bestScore = sim
				best = w
			}
		}
	}

	if best == nil || bestScore < 0.5 {
		return nil
	}

	return &MatchResult{
		Widget:     best,
		Confidence: bestScore,
		Strategy:   "fuzzy",
	}
}

// matchKeywords looks for keyword presence in label/description.
func matchKeywords(widgets []WidgetInfo, criteria MatchCriteria) *MatchResult {
	if len(criteria.Keywords) == 0 {
		return nil
	}

	var best *WidgetInfo
	var bestScore float64

	for i := range widgets {
		w := &widgets[i]
		if criteria.Role != "" && !strings.EqualFold(w.Role, criteria.Role) {
			continue
		}

		score := keywordScore(w, criteria.Keywords)
		if score > bestScore {
			bestScore = score
			best = w
		}
	}

	if best == nil || bestScore < 0.3 {
		return nil
	}

	return &MatchResult{
		Widget:     best,
		Confidence: bestScore,
		Strategy:   "keyword",
	}
}

// keywordScore calculates how many keywords match the widget.
func keywordScore(w *WidgetInfo, keywords []string) float64 {
	text := strings.ToLower(w.Label + " " + w.Description + " " + w.Value)
	matches := 0
	
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if strings.Contains(text, kwLower) {
			matches++
		}
	}

	return float64(matches) / float64(len(keywords))
}

// matchSpatial uses location preferences to select widgets.
func matchSpatial(widgets []WidgetInfo, criteria MatchCriteria, screenW, screenH int) *MatchResult {
	if criteria.Role == "" || criteria.PreferredArea == "" {
		return nil
	}

	var candidates []*WidgetInfo
	for i := range widgets {
		if strings.EqualFold(widgets[i].Role, criteria.Role) {
			candidates = append(candidates, &widgets[i])
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick widget based on preferred area
	var best *WidgetInfo
	var bestScore float64

	for _, w := range candidates {
		score := spatialScore(w, criteria.PreferredArea, screenW, screenH)
		if score > bestScore {
			bestScore = score
			best = w
		}
	}

	if best == nil {
		return nil
	}

	return &MatchResult{
		Widget:     best,
		Confidence: 0.6 + (bestScore * 0.3), // Base 0.6 + up to 0.3 from spatial score
		Strategy:   "spatial",
	}
}

// spatialScore calculates how well a widget fits the preferred area.
func spatialScore(w *WidgetInfo, area string, screenW, screenH int) float64 {
	centerX := w.Bounds.X + w.Bounds.Width/2
	centerY := w.Bounds.Y + w.Bounds.Height/2

	// Normalize to 0-1
	normX := float64(centerX) / float64(screenW)
	normY := float64(centerY) / float64(screenH)

	switch area {
	case "top":
		return 1.0 - normY
	case "bottom":
		return normY
	case "left":
		return 1.0 - normX
	case "right":
		return normX
	case "center":
		dx := normX - 0.5
		dy := normY - 0.5
		return 1.0 - math.Sqrt(dx*dx+dy*dy)
	default:
		return 0.5
	}
}

// similarity calculates string similarity using Jaro-Winkler-like approach.
func similarity(a, b string) float64 {
	if a == b {
		return 1.0
	}

	// Simple substring matching first
	if strings.Contains(b, a) {
		return 0.9 + (0.1 * float64(len(a)) / float64(len(b)))
	}

	// Calculate Levenshtein distance
	dist := levenshteinDistance(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	if maxLen == 0 {
		return 1.0
	}

	return 1.0 - float64(dist)/float64(maxLen)
}

// levenshteinDistance calculates edit distance between two strings.
func levenshteinDistance(a, b string) int {
	a = normalizeString(a)
	b = normalizeString(b)

	// Early termination
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// Use two rows for space efficiency
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 0; i < len(a); i++ {
		curr[0] = i + 1

		for j := 0; j < len(b); j++ {
			cost := 0
			if a[i] != b[j] {
				cost = 1
			}

			curr[j+1] = min(
				prev[j+1]+1,    // deletion
				curr[j]+1,      // insertion
				prev[j]+cost,   // substitution
			)
		}

		prev, curr = curr, prev
	}

	return prev[len(b)]
}

// normalizeString prepares string for comparison.
func normalizeString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	
	return b.String()
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// SelfHealingFind is the main entry point for finding widgets.
// It tries multiple strategies and logs what it found for debugging.
func SelfHealingFind(snap *Snapshot, goal string) (*MatchResult, error) {
	// Parse goal into criteria
	criteria := parseGoal(goal)
	
	// Try to find with increasing tolerance
	criteria.MinConfidence = 0.8
	if result := FindWidgetSemantic(snap, criteria); result != nil {
		return result, nil
	}
	
	criteria.MinConfidence = 0.6
	if result := FindWidgetSemantic(snap, criteria); result != nil {
		return result, nil
	}
	
	criteria.MinConfidence = 0.4
	if result := FindWidgetSemantic(snap, criteria); result != nil {
		return result, nil
	}
	
	return nil, nil
}

// parseGoal converts a human-readable goal into match criteria.
func parseGoal(goal string) MatchCriteria {
	goalLower := strings.ToLower(goal)
	c := MatchCriteria{
		MinConfidence: 0.5,
	}

	// Extract role hints
	if strings.Contains(goalLower, "input") || strings.Contains(goalLower, "textbox") {
		c.Role = "textbox"
		c.Keywords = []string{"input", "text", "message", "chat"}
		c.PreferredArea = "bottom"
	} else if strings.Contains(goalLower, "button") {
		c.Role = "button"
		c.Keywords = []string{"button", "click", "submit"}
	} else if strings.Contains(goalLower, "list") || strings.Contains(goalLower, "session") {
		c.Role = "list"
		c.Keywords = []string{"session", "list", "chat"}
		c.PreferredArea = "left"
	}

	// Extract label hints
	words := strings.Fields(goalLower)
	for _, w := range words {
		if len(w) > 3 && !isCommonWord(w) {
			c.Keywords = append(c.Keywords, w)
		}
	}

	// Look for specific patterns
	switch {
	case strings.Contains(goalLower, "send"):
		c.Label = "send"
		c.Keywords = append(c.Keywords, "send", "submit")
	case strings.Contains(goalLower, "new") && strings.Contains(goalLower, "session"):
		c.Keywords = append(c.Keywords, "new", "create", "+")
		c.PreferredArea = "top"
	case strings.Contains(goalLower, "settings") || strings.Contains(goalLower, "config"):
		c.Keywords = append(c.Keywords, "settings", "config", "options")
	}

	return c
}

var commonWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"to": true, "of": true, "in": true, "on": true, "at": true,
	"for": true, "with": true, "from": true, "by": true, "this": true,
	"that": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "can": true, "may": true, "might": true,
	"focus": true, "click": true, "select": true, "find": true, "get": true,
}

func isCommonWord(w string) bool {
	return commonWords[w]
}
