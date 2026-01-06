package builtin

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

func sanitizeEnvMap(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if !isValidEnvKey(key) {
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isValidEnvKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if !(r == '_' || unicode.IsLetter(r)) {
				return false
			}
			continue
		}
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	overrides = sanitizeEnvMap(overrides)
	if len(overrides) == 0 {
		return base
	}
	if base == nil {
		base = os.Environ()
	}
	return append(base, envPairs(overrides)...)
}

func envPairs(env map[string]string) []string {
	env = sanitizeEnvMap(env)
	if len(env) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(pairs)
	return pairs
}
