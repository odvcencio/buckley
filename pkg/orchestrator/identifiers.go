package orchestrator

import "strings"

// SanitizeIdentifier normalizes plan/feature identifiers for filesystem usage.
func SanitizeIdentifier(value string) string {
	if value == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
