package external

import (
	"os"
	"path/filepath"
)

// HookManifestRef points at a discovered plugin manifest that declares
// hooks, together with the resolved executable and working directory the
// hook process must run with.
type HookManifestRef struct {
	Manifest *ToolManifest
	ExecPath string
	WorkDir  string
	Env      map[string]string
}

// DiscoverHookManifests walks dirs the same way plugin discovery does and
// returns every manifest that declares a hooks section. Missing directories
// are skipped; malformed manifests are skipped (plugin loading already
// warns about them).
func DiscoverHookManifests(dirs []string) []HookManifestRef {
	var refs []HookManifestRef
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() || filepath.Base(path) != "tool.yaml" {
				return nil
			}
			manifest, err := LoadManifest(path)
			if err != nil || !manifest.HasHooks() {
				return nil
			}
			manifestDir := filepath.Dir(path)
			execPath := filepath.Join(manifestDir, manifest.Executable)
			if resolved, rerr := filepath.EvalSymlinks(execPath); rerr == nil {
				execPath = resolved
			}
			if err := checkExecutable(execPath); err != nil {
				return nil
			}
			refs = append(refs, HookManifestRef{
				Manifest: manifest,
				ExecPath: execPath,
				WorkDir:  manifestDir,
			})
			return nil
		})
	}
	return refs
}
