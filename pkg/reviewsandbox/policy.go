package reviewsandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const PermissionProfileName = "buckley-review-snapshot"

// PermissionArgs returns the Codex CLI overrides for a review verification
// sandbox. The current Codex working directory is read-only, only its private
// TMPDIR is writable, and direct network access is disabled.
func PermissionArgs(command, runtimeDir string) []string {
	return PermissionArgsWithReadRoots(command, runtimeDir)
}

// PermissionArgsWithReadRoots is PermissionArgs plus narrowly-scoped,
// read-only toolchain or dependency roots required by the verification run.
func PermissionArgsWithReadRoots(command, runtimeDir string, additionalReadRoots ...string) []string {
	readRoots := append(reviewReadRoots(command), additionalReadRoots...)
	readRoots = canonicalExistingDirectories(readRoots)
	filesystem := []string{
		strconv.Quote(":minimal") + ` = "read"`,
		strconv.Quote(":workspace_roots") + ` = { "." = "read" }`,
		strconv.Quote(":tmpdir") + ` = "write"`,
	}
	for _, root := range readRoots {
		filesystem = append(filesystem, strconv.Quote(root)+` = "read"`)
	}

	env := ToolEnvironment(runtimeDir)
	envEntries := make([]string, 0, len(env))
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		envEntries = append(envEntries, strconv.Quote(key)+" = "+strconv.Quote(env[key]))
	}

	return []string{
		"-c", "default_permissions=" + strconv.Quote(PermissionProfileName),
		"-c", "permissions." + PermissionProfileName + ".filesystem={ " + strings.Join(filesystem, ", ") + " }",
		"-c", `permissions.` + PermissionProfileName + `.network={ enabled = false }`,
		"-c", `shell_environment_policy={ inherit = "none", set = { ` + strings.Join(envEntries, ", ") + ` } }`,
		"-c", "allow_login_shell=false",
	}
}

// ToolEnvironment is the complete environment visible to a verification
// process inside Codex's sandbox. Build and package managers are forced
// offline; all writable caches and temporary output live below runtimeDir.
func ToolEnvironment(runtimeDir string) map[string]string {
	runtimeDir = filepath.Clean(runtimeDir)
	env := map[string]string{
		"CARGO_NET_OFFLINE":             "true",
		"CARGO_TARGET_DIR":              filepath.Join(runtimeDir, "cargo-target"),
		"CI":                            "true",
		"GOCACHE":                       filepath.Join(runtimeDir, "go-build"),
		"GOENV":                         "off",
		"GOPROXY":                       "off",
		"GOSUMDB":                       "off",
		"GOTOOLCHAIN":                   "local",
		"GOTMPDIR":                      filepath.Join(runtimeDir, "go-tmp"),
		"HOME":                          filepath.Join(runtimeDir, "home"),
		"LANG":                          defaultIfBlank(os.Getenv("LANG"), "C.UTF-8"),
		"LC_ALL":                        defaultIfBlank(os.Getenv("LC_ALL"), "C.UTF-8"),
		"NPM_CONFIG_CACHE":              filepath.Join(runtimeDir, "npm-cache"),
		"NPM_CONFIG_OFFLINE":            "true",
		"PATH":                          safePath(),
		"PIP_CACHE_DIR":                 filepath.Join(runtimeDir, "pip-cache"),
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",
		"PIP_NO_INDEX":                  "1",
		"PYTHONDONTWRITEBYTECODE":       "1",
		"PYTHONPYCACHEPREFIX":           filepath.Join(runtimeDir, "pycache"),
		"TERM":                          "dumb",
		"TEMP":                          runtimeDir,
		"TMP":                           runtimeDir,
		"TMPDIR":                        runtimeDir,
		"XDG_CACHE_HOME":                filepath.Join(runtimeDir, "xdg-cache"),
		"YARN_CACHE_FOLDER":             filepath.Join(runtimeDir, "yarn-cache"),
	}

	home, _ := os.UserHomeDir()
	if cache := firstExistingDirectory(filepath.Join(home, "go", "pkg", "mod")); cache != "" {
		env["GOMODCACHE"] = cache
	}
	if rustup := firstExistingDirectory(filepath.Join(home, ".rustup")); rustup != "" {
		env["RUSTUP_HOME"] = rustup
	}
	// CARGO_HOME is private so ambient Cargo config cannot affect verification.
	// prepareRuntime links only the read-only registry/git stores into it.
	env["CARGO_HOME"] = filepath.Join(runtimeDir, "cargo-home")
	return env
}

// RestrictedCommandEnvironment is safe for `codex sandbox`: it does not
// inherit credentials, proxy variables, hooks, or user configuration.
func RestrictedCommandEnvironment(runtimeDir string) []string {
	env := ToolEnvironment(runtimeDir)
	env["CODEX_HOME"] = filepath.Join(runtimeDir, "codex-home")
	return sortedEnvironment(env)
}

// InheritedCommandEnvironment preserves the caller environment for Codex
// provider authentication while forcing all sandbox temp paths into runtimeDir.
// The child command still receives only ToolEnvironment through the permission
// profile's shell_environment_policy.
func InheritedCommandEnvironment(runtimeDir string) []string {
	overrides := map[string]string{"TMPDIR": runtimeDir, "TMP": runtimeDir, "TEMP": runtimeDir}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for _, entry := range sortedEnvironment(overrides) {
		result = append(result, entry)
	}
	return result
}

func reviewReadRoots(command string) []string {
	candidates := make([]string, 0, 24)
	if resolved, err := resolveExplicitExecutable(command); err == nil {
		candidates = appendExecutableReadRoots(candidates, resolved)
	} else if resolved, err := trustedLookPath(strings.TrimSpace(command)); err == nil {
		candidates = appendExecutableReadRoots(candidates, resolved)
	}
	for _, executable := range []string{"go", "cargo", "rustc", "python3", "node", "npm"} {
		if resolved, err := trustedLookPath(executable); err == nil {
			candidates = appendExecutableReadRoots(candidates, resolved)
		}
	}

	home, _ := os.UserHomeDir()
	for _, rel := range []string{"go/pkg/mod", ".cargo/registry", ".cargo/git", ".rustup", ".gradle/caches", ".m2/repository", ".nuget/packages", ".cache/pip"} {
		candidates = append(candidates, filepath.Join(home, filepath.FromSlash(rel)))
	}
	return canonicalExistingDirectories(candidates)
}

func appendExecutableReadRoots(candidates []string, executable string) []string {
	candidates = append(candidates, filepath.Dir(executable))
	if canonical, err := filepath.EvalSymlinks(executable); err == nil {
		candidates = append(candidates, filepath.Dir(canonical))
	}
	return candidates
}

func canonicalExistingDirectories(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(strings.TrimSpace(candidate))
		if err != nil || strings.TrimSpace(candidate) == "" {
			continue
		}
		if canonical, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
			absolute = canonical
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.IsDir() {
			continue
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		result = append(result, absolute)
	}
	sort.Strings(result)
	return result
}

func sortedEnvironment(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func safePath() string {
	return strings.Join(trustedExecutableDirectories(), string(os.PathListSeparator))
}

func firstExistingDirectory(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			candidate = absolute
		}
		if canonical, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = canonical
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

func defaultIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
