package tool

import "strings"

// IsToolAllowed returns true if the tool name is allowed by the filter.
// A nil allowed list means all tools are allowed. An explicitly empty list
// means no tools are allowed.
func IsToolAllowed(name string, allowed []string) bool {
	if allowed == nil {
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
