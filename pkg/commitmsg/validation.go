package commitmsg

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HeaderLimit is the maximum visible length of a conventional commit header.
const HeaderLimit = 72

// AllowedActions is the shared action vocabulary used by commit and PR
// generation. Keeping it here prevents one structured path from accepting a
// verb that another path silently rewrites.
var AllowedActions = []string{
	"add", "fix", "update", "refactor", "remove", "improve",
	"rename", "move", "revert", "merge", "bump", "release",
	"format", "optimize", "simplify", "extract", "inline",
	"document", "test", "build", "ci",
}

// structuredActionAliases contains legacy/conventional verbs that can still
// appear in a generated header even though the current structured tool emits
// the canonical actions above. They must be treated as prefixes when checking
// that a model did not repeat the header inside the subject.
var structuredActionAliases = []string{
	"feat", "feature", "docs", "doc", "style", "perf", "performance",
	"chore", "tests",
}

// NormalizeAction trims and lowercases an action while preserving unknown
// values for validation diagnostics.
func NormalizeAction(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}

// IsAllowedAction reports whether action belongs to the shared vocabulary.
func IsAllowedAction(action string) bool {
	action = NormalizeAction(action)
	for _, allowed := range AllowedActions {
		if action == allowed {
			return true
		}
	}
	return false
}

// NormalizeBullet converts one model-supplied bullet into a single safe line.
// Renderers add the canonical marker, so duplicate markers are removed.
func NormalizeBullet(bullet string) string {
	value := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(bullet, "\r", " "), "\n", " "))
	for {
		trimmed := strings.TrimSpace(value)
		if trimmed == "-" || trimmed == "*" || trimmed == "•" {
			return ""
		}
		changed := false
		for _, marker := range []string{"- ", "* ", "• "} {
			if strings.HasPrefix(trimmed, marker) {
				value = strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
				changed = true
				break
			}
		}
		if !changed {
			value = trimmed
			break
		}
	}
	return strings.Join(strings.Fields(value), " ")
}

// NormalizeIssueRef returns a bare numeric issue reference or an empty string
// when the model supplied a value that cannot be rendered safely.
func NormalizeIssueRef(issue string) string {
	issue = strings.TrimSpace(issue)
	issue = strings.TrimLeft(issue, "#")
	if issue == "" {
		return ""
	}
	for _, r := range issue {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return issue
}

// ValidateCommitFields validates the model-controlled fields that affect the
// commit header, body shape, and issue footers.
func ValidateCommitFields(action, scope, subject string, body, issues []string) error {
	action = NormalizeAction(action)
	if action == "" {
		return fmt.Errorf("action is required")
	}
	if !IsAllowedAction(action) {
		return fmt.Errorf("action %q is not an allowed verb", action)
	}
	if hasCommitControl(scope) {
		return fmt.Errorf("scope contains control characters")
	}
	if hasCommitControl(subject) {
		return fmt.Errorf("subject contains control characters")
	}
	scope = strings.TrimSpace(scope)
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("subject is required")
	}
	header := action + ": " + subject
	if scope != "" {
		header = action + "(" + scope + "): " + subject
	}
	if utf8.RuneCountInString(header) > HeaderLimit {
		return fmt.Errorf("header exceeds %d characters", HeaderLimit)
	}
	if hasRepeatedActionPrefix(subject) {
		return fmt.Errorf("subject repeats an action/scope prefix; action and scope belong only in the structured header and the subject must not repeat them")
	}

	nonEmptyBody := 0
	for _, bullet := range body {
		if hasCommitControl(bullet) {
			return fmt.Errorf("body contains control characters")
		}
		if NormalizeBullet(bullet) != "" {
			nonEmptyBody++
		}
	}
	if nonEmptyBody == 0 {
		return fmt.Errorf("body requires at least one bullet")
	}
	for _, issue := range issues {
		if strings.TrimSpace(issue) != "" && NormalizeIssueRef(issue) == "" {
			return fmt.Errorf("issue reference %q is not numeric", issue)
		}
	}
	return nil
}

func hasCommitControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// hasRepeatedActionPrefix reports whether subject starts with a structured
// action header prefix. The prefix check is deliberately vocabulary-bound so
// ordinary colon prose such as "http:" and "10:30" remains valid.
func hasRepeatedActionPrefix(subject string) bool {
	subject = strings.TrimSpace(subject)
	for _, action := range AllowedActions {
		if hasActionHeaderPrefix(subject, action) {
			return true
		}
	}
	for _, action := range structuredActionAliases {
		if hasActionHeaderPrefix(subject, action) {
			return true
		}
	}
	return false
}

func hasActionHeaderPrefix(subject, action string) bool {
	if len(subject) <= len(action) || !strings.EqualFold(subject[:len(action)], action) {
		return false
	}

	remainder := subject[len(action):]
	if remainder[0] == '(' {
		close := strings.IndexByte(remainder, ')')
		if close < 2 || !validHeaderScope(remainder[1:close]) {
			return false
		}
		remainder = remainder[close+1:]
	}
	if strings.HasPrefix(remainder, "!") {
		remainder = remainder[1:]
	}
	return strings.HasPrefix(remainder, ":")
}

func validHeaderScope(scope string) bool {
	if scope == "" || strings.TrimSpace(scope) != scope {
		return false
	}
	for _, r := range scope {
		if unicode.IsControl(r) || r == '(' || r == ')' || r == ':' {
			return false
		}
	}
	return true
}
