package headless

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
)

// IsGitURL reports whether the input looks like a git clone URL.
//
// Supported formats:
// - https://host/org/repo(.git)
// - ssh://host/org/repo(.git)
// - git://host/org/repo(.git)
// - file:///path/to/repo
// - git@host:org/repo(.git) (scp-style)
func IsGitURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || strings.TrimSpace(u.Scheme) == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "http", "https", "ssh", "git", "file":
			return true
		default:
			return false
		}
	}

	if filepath.IsAbs(value) || isDriveLetterPath(value) {
		return false
	}

	colon := strings.Index(value, ":")
	if colon <= 0 || colon >= len(value)-1 {
		return false
	}

	host := value[:colon]
	path := value[colon+1:]
	if strings.ContainsAny(host, "/\\") {
		return false
	}

	if strings.ContainsAny(value, " \t\r\n") {
		return false
	}

	return strings.Contains(path, "/") || strings.HasSuffix(path, ".git")
}

func isDriveLetterPath(value string) bool {
	if len(value) < 2 {
		return false
	}
	if value[1] != ':' {
		return false
	}
	r := rune(value[0])
	return unicode.IsLetter(r)
}
