package model

import "sort"

// maxModelSuggestions bounds how many close matches an unresolved-model
// error names. More than a handful stops being a targeted suggestion and
// starts being a catalog dump.
const maxModelSuggestions = 5

// closestModelMatches ranks every catalog model ID by case-insensitive
// Levenshtein distance to requested and returns up to limit of the nearest
// ones. It exists so an unresolved model ID (H1) can point at what the
// caller probably meant -- e.g. "openai_compatible/glm-5.3-flash" (a
// previously registered, now-renamed alias) suggesting the live catalog's
// "openai_compatible/glm5.3flash" -- instead of only reporting failure.
// Matches farther than modelSuggestionMaxDistance(requested) are dropped:
// an unrelated catalog should suggest nothing rather than noise.
func (m *Manager) closestModelMatches(requested string, limit int) []string {
	if m == nil || limit <= 0 {
		return nil
	}
	requested = normalizeForModelSuggestion(requested)
	if requested == "" {
		return nil
	}

	m.catalogMu.RLock()
	type scored struct {
		id       string
		distance int
	}
	candidates := make([]scored, 0, len(m.catalog))
	for id := range m.catalog {
		distance := levenshteinDistance(requested, normalizeForModelSuggestion(id))
		candidates = append(candidates, scored{id: id, distance: distance})
	}
	m.catalogMu.RUnlock()

	maxDistance := modelSuggestionMaxDistance(requested)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].id < candidates[j].id
	})

	matches := make([]string, 0, limit)
	for _, candidate := range candidates {
		if candidate.distance > maxDistance {
			break
		}
		matches = append(matches, candidate.id)
		if len(matches) >= limit {
			break
		}
	}
	return matches
}

// modelSuggestionMaxDistance bounds how dissimilar a suggested model ID may
// be from what was requested, scaled to the requested ID's own length so a
// short ID does not accept wildly unrelated short catalog entries.
func modelSuggestionMaxDistance(requested string) int {
	quarter := len(requested) / 3
	if quarter < 4 {
		return 4
	}
	return quarter
}

func normalizeForModelSuggestion(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		out = append(out, r)
	}
	return string(out)
}

// levenshteinDistance returns the edit distance between two strings using
// the standard single-row dynamic-programming form.
func levenshteinDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			deletion := prev[j] + 1
			insertion := curr[j-1] + 1
			substitution := prev[j-1] + cost
			best := deletion
			if insertion < best {
				best = insertion
			}
			if substitution < best {
				best = substitution
			}
			curr[j] = best
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}
