package reviewsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// selfLocatingScriptRe finds a launcher script that looks for files next to
// itself (as the distribution launcher /usr/bin/google-chrome does with
// "$(dirname "$(readlink -f "$0")")"). Copied into the private runtime folder,
// such a script would look for its browser there and fail.
var selfLocatingScriptRe = regexp.MustCompile(`\$0\b|\$\{0[}:#%/]|BASH_SOURCE|\breadlink\b|\bdirname\b|\brealpath\b`)

// NodeVerificationPackagePath resolves only manifests inside the immutable
// snapshot, using the same package promotion as the actual verifier.
func NodeVerificationPackagePath(root, path string) (string, error) {
	root, dir, err := resolveSnapshotDirectory(root, path)
	if err != nil {
		return "", err
	}
	dir, err = resolveNodePackageDirectory(root, dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	return filepath.ToSlash(rel), err
}

// Only explicitly selected browser executables cross the environment boundary.
// Runtime libraries remain read-only. Small launcher scripts are copied into
// the private runtime so their parent directory never needs to be exposed.
func prepareNodeBrowserRuntime(runtimeDir string) ([]string, map[string]string, error) {
	env := map[string]string{}
	var roots []string
	for _, key := range []string{"SLIDES_BROWSER", "SLIDES_CHROME"} {
		selected := strings.TrimSpace(os.Getenv(key))
		if selected == "" {
			continue
		}
		if !filepath.IsAbs(selected) {
			return nil, nil, fmt.Errorf("%s must be an absolute executable path", key)
		}
		resolved, err := filepath.EvalSymlinks(selected)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve %s: %w", key, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return nil, nil, fmt.Errorf("%s must select a regular executable", key)
		}
		if info.Size() <= 64<<10 {
			content, err := os.ReadFile(resolved)
			if err != nil {
				return nil, nil, err
			}
			if strings.HasPrefix(string(content), "#!") {
				if selfLocatingScriptRe.Match(content) {
					return nil, nil, fmt.Errorf("%s selects %s, a launcher script that looks for the browser next to itself; it cannot be copied into the private runtime. Select the browser binary itself, or a wrapper script that runs it by absolute path", key, selected)
				}
				copied := filepath.Join(runtimeDir, strings.ToLower(key))
				if err := os.WriteFile(copied, content, 0o700); err != nil {
					return nil, nil, err
				}
				env[key] = copied
				continue
			}
		}
		env[key] = resolved
		roots = append(roots, filepath.Dir(resolved))
		if key == "SLIDES_BROWSER" {
			// A host-owned Chrome launcher directory may link distribution
			// resources to an installed cache. Expose only those named resources'
			// distribution directories, never the cache or home directory itself.
			for _, name := range []string{"icudtl.dat", "headless_command_resources.pak", "headless_lib_data.pak", "headless_lib_strings.pak", "libEGL.so", "libGLESv2.so", "libvk_swiftshader.so", "libvulkan.so.1", "v8_context_snapshot.bin", "vk_swiftshader_icd.json", "locales", "hyphen-data"} {
				resource := filepath.Join(filepath.Dir(resolved), name)
				info, err := os.Lstat(resource)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return nil, nil, err
				}
				if info.Mode()&os.ModeSymlink == 0 {
					continue
				}
				target, err := filepath.EvalSymlinks(resource)
				if err != nil || filepath.Base(target) != name {
					return nil, nil, fmt.Errorf("unsafe Chrome resource %s", name)
				}
				roots = append(roots, filepath.Dir(target))
			}
		}
	}
	return canonicalExistingDirectories(roots), env, nil
}
