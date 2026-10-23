package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveReadRoots expands and validates read roots from config and the command
// line. Each entry has "~" and environment variables expanded and becomes an
// absolute path (relative entries are taken from baseDir). An entry that does
// not exist is an error, not a silent skip, so a typo cannot leave a task
// without the specs it was pointed at. Duplicates are dropped.
func ResolveReadRoots(baseDir string, lists ...[]string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, raw := range list {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				continue
			}
			var missing string
			entry = os.Expand(entry, func(name string) string {
				value, ok := os.LookupEnv(name)
				if !ok || value == "" {
					missing = name
				}
				return value
			})
			if missing != "" {
				return nil, fmt.Errorf("read root %q: environment variable %s is unset or empty", raw, missing)
			}
			if entry == "~" || strings.HasPrefix(entry, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					return nil, fmt.Errorf("read root %q: resolve home: %w", raw, err)
				}
				entry = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(entry, "~"), "/"))
			}
			if !filepath.IsAbs(entry) {
				entry = filepath.Join(baseDir, entry)
			}
			abs, err := filepath.Abs(entry)
			if err != nil {
				return nil, fmt.Errorf("read root %q: %w", raw, err)
			}
			abs = filepath.Clean(abs)
			info, err := os.Stat(abs)
			if err != nil {
				return nil, fmt.Errorf("read root %q does not exist (resolved to %s)", raw, abs)
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return nil, fmt.Errorf("read root %q must be a directory or regular file", raw)
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out, nil
}
