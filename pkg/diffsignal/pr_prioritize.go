package diffsignal

import (
	"path"
	"sort"
	"strings"
)

// prCategory ranks a high-signal file for PrioritizeForPR's assembly order.
// Lower ranks are assembled (and therefore funded) first.
type prCategory int

const (
	prCategorySource prCategory = iota
	prCategoryTest
	prCategoryDocConfig
	prCategoryDotOrScratch
)

// docConfigSuffixes are file extensions treated as documentation or
// configuration: relevant for review context, but not the primary evidence
// of what a PR changes.
var docConfigSuffixes = []string{
	".md", ".mdx", ".txt", ".rst", ".adoc",
	".yml", ".yaml", ".json", ".toml", ".ini", ".cfg", ".lock",
}

// docConfigBasenames are well-known repo-root files classified as
// docs/config regardless of extension.
var docConfigBasenames = map[string]bool{
	"changelog":       true,
	"license":         true,
	"notice":          true,
	"contributing.md": true,
	"makefile":        true,
	"dockerfile":      true,
}

// rankFilesForPR stable-sorts normal (indices into files, already in git's
// emission order) by prCategory: source first, tests next, docs/config
// after, dotdirs/scratch/handoff notes last. Ties preserve emission order,
// so within a category the result matches Prioritize's existing behavior.
func rankFilesForPR(files []FileDiff, normal []int) []int {
	ranked := make([]int, len(normal))
	copy(ranked, normal)
	sort.SliceStable(ranked, func(a, b int) bool {
		return classifyPRCategory(files[ranked[a]].Path) < classifyPRCategory(files[ranked[b]].Path)
	})
	return ranked
}

// classifyPRCategory buckets a changed file's path for PR ranking purposes.
// It never affects low-signal classification (binary/generated/minified);
// it only orders the high-signal set that classify() left untouched.
func classifyPRCategory(filePath string) prCategory {
	lower := strings.ToLower(filePath)

	// Any dot-prefixed path segment (".github/", ".tiller/", ".buckley/",
	// and so on) or an explicit scratch/handoff marker sorts last: these are
	// process/tooling artifacts, not the change itself.
	segments := strings.Split(lower, "/")
	for _, seg := range segments[:len(segments)-1] {
		if strings.HasPrefix(seg, ".") {
			return prCategoryDotOrScratch
		}
	}
	if strings.Contains(lower, "scratch") || strings.Contains(lower, "handoff") {
		return prCategoryDotOrScratch
	}

	base := path.Base(lower)
	if isTestPath(lower, base) {
		return prCategoryTest
	}
	if docConfigBasenames[base] {
		return prCategoryDocConfig
	}
	for _, suffix := range docConfigSuffixes {
		if strings.HasSuffix(base, suffix) {
			return prCategoryDocConfig
		}
	}

	return prCategorySource
}

// isTestPath reports whether a lower-cased path looks like a test file
// across the languages GoSX-adjacent repos commonly mix: Go (_test.go),
// JS/TS (.test.ts, .spec.ts), and a conventional test/ or tests/ directory.
func isTestPath(lowerPath, base string) bool {
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, seg := range strings.Split(lowerPath, "/") {
		if seg == "test" || seg == "tests" || seg == "__tests__" {
			return true
		}
	}
	return false
}
