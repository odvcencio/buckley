package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const EnvBuckleyLogDir = "BUCKLEY_LOG_DIR"

func BuckleyLogsBaseDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvBuckleyLogDir)); dir != "" {
		return filepath.Clean(expandHomePath(dir))
	}
	return filepath.Join(".buckley", "logs")
}

func expandHomePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func BuckleyLogsBaseDirForWorkdir(workdir string) string {
	base := BuckleyLogsBaseDir()
	if filepath.IsAbs(base) || strings.TrimSpace(workdir) == "" {
		return base
	}
	return filepath.Join(workdir, base)
}

func BuckleyLogsDir(identifier string) string {
	base := BuckleyLogsBaseDir()
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return base
	}
	return filepath.Join(base, identifier)
}
