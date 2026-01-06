package tool

import "strings"

// IsToolAllowed returns true if the tool name is allowed by the filter.
// An empty allowed list means all tools are allowed.
func IsToolAllowed(name string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	name = strings.TrimSpace(name)
	for _, allowedName := range allowed {
		if name == strings.TrimSpace(allowedName) {
			return true
		}
	}
	return false
}
