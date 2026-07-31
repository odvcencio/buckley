package reviewsandbox

import (
	"fmt"
	goversion "go/version"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// trustedExecutableDirectories is deliberately independent of ambient PATH.
// Every verification argv uses an absolute executable selected from this
// fixed system/toolchain allowlist (or an explicitly configured absolute Codex
// executable).
func trustedExecutableDirectories() []string {
	candidates := make([]string, 0, 16)
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(home) != "" {
		// golang.org/dl installs complete toolchains below ~/sdk. Prefer the
		// newest installed SDK over an older system Go so immutable reviews can
		// verify modules that have already raised their go directive.
		candidates = appendGlobDirectoriesDescending(candidates, filepath.Join(home, "sdk", "go*", "bin"))
	}
	candidates = append(candidates,
		"/usr/local/go/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/opt/homebrew/bin",
		"/opt/local/bin",
	)
	if strings.TrimSpace(home) != "" {
		candidates = append(candidates, filepath.Join(home, ".cargo", "bin"))
		candidates = appendGlobDirectories(candidates, filepath.Join(home, ".rustup", "toolchains", "*", "bin"))
		candidates = appendGlobDirectories(candidates, filepath.Join(home, ".nvm", "versions", "node", "*", "bin"))
		candidates = appendGlobDirectories(candidates, filepath.Join(home, ".codex", "bin", "wsl", "*"))
	}
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		candidates = appendGlobDirectories(candidates, filepath.Join(codexHome, "bin", "wsl", "*"))
		candidates = appendGlobDirectories(candidates, filepath.Join(codexHome, "bin", "*"))
	}
	return canonicalTrustedExecutableDirectories(candidates)
}

// canonicalTrustedExecutableDirectories preserves the explicit candidate
// priority while removing missing directories and canonical duplicates. The
// order is security-relevant: both trustedLookPath and the review sandbox PATH
// must prefer the configured toolchain roots over generic system directories.
func canonicalTrustedExecutableDirectories(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			continue
		}
		canonical = filepath.Clean(canonical)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result
}

func appendGlobDirectories(candidates []string, pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return candidates
	}
	sort.Strings(matches)
	return append(candidates, matches...)
}

func appendGlobDirectoriesDescending(candidates []string, pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return candidates
	}
	sort.SliceStable(matches, func(i, j int) bool {
		iVersion := filepath.Base(filepath.Dir(matches[i]))
		jVersion := filepath.Base(filepath.Dir(matches[j]))
		if comparison := goversion.Compare(iVersion, jVersion); comparison != 0 {
			return comparison > 0
		}
		return matches[i] > matches[j]
	})
	return append(candidates, matches...)
}

func trustedLookPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("executable name must not contain a path")
	}
	names := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		names = append(names, name+".exe")
	}
	for _, dir := range trustedExecutableDirectories() {
		for _, candidateName := range names {
			if executable, err := resolveTrustedCandidate(filepath.Join(dir, candidateName)); err == nil {
				return executable, nil
			}
		}
	}
	return "", fmt.Errorf("trusted executable %q was not found", name)
}

// resolveTrustedCandidate preserves the logical executable path because
// rustup-style proxy symlinks select their tool from argv[0]. Both the logical
// parent and resolved target are already beneath deterministic trusted
// installation roots selected by trustedExecutableDirectories.
func resolveTrustedCandidate(command string) (string, error) {
	logical, err := filepath.Abs(filepath.Clean(command))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(logical)
	if err != nil {
		return "", err
	}
	if info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", fmt.Errorf("path is not executable")
	}
	if _, err := filepath.EvalSymlinks(logical); err != nil {
		return "", err
	}
	return logical, nil
}

func resolveExplicitExecutable(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" || !filepath.IsAbs(command) {
		return "", fmt.Errorf("executable must be an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(command))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", fmt.Errorf("path is not executable")
	}
	return filepath.Clean(canonical), nil
}
