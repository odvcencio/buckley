package reviewsandbox

import (
	"os"
	"path/filepath"
	"strings"
)

func nodePackageManager(workDir string) (command, lockfile string) {
	if _, err := os.Stat(filepath.Join(workDir, "pnpm-lock.yaml")); err == nil {
		return "pnpm", "pnpm-lock.yaml"
	}
	return "npm", "package-lock.json"
}

func nodeInstallHint(workDir string) string {
	manager, _ := nodePackageManager(workDir)
	if manager == "pnpm" {
		return "pnpm install --frozen-lockfile"
	}
	return "npm ci"
}

func toolchainInstallHint(language Language, workDir string) string {
	switch language {
	case LanguageNode:
		hint := `curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.8/install.sh | bash; . "$HOME/.nvm/nvm.sh" && nvm install --lts`
		if manager, _ := nodePackageManager(workDir); manager == "pnpm" {
			hint += ` && npm install --global --prefix "$HOME/.local" pnpm`
		}
		return hint + "; then " + nodeInstallHint(workDir)
	case LanguagePython:
		const python = `"$HOME/.local/share/buckley/review-python/bin/python3"`
		hint := `python3 -m venv "$HOME/.local/share/buckley/review-python" && ` + python + " -m pip install pytest"
		hasRequirements := false
		for _, name := range []string{"requirements.txt", "requirements-dev.txt", "requirements-test.txt"} {
			if _, err := os.Stat(filepath.Join(workDir, name)); err == nil {
				hint += " -r " + name
				hasRequirements = true
			}
		}
		if _, err := os.Stat(filepath.Join(workDir, "pyproject.toml")); err == nil && !hasRequirements {
			hint += " && " + python + " -m pip install ."
		}
		return hint
	case LanguageGo:
		return "install the Go version in go.mod under /usr/local/go or $HOME/sdk"
	case LanguageRust:
		return "rustup toolchain install stable"
	default:
		return "install the project toolchain"
	}
}

func verificationToolchainMissing(language Language, output commandOutput) bool {
	if language == LanguageNode && strings.Contains(output.Stderr, "Network access disabled by the environment; can't reach npm repository") {
		return true
	}
	for _, line := range strings.Split(output.Stderr, "\n") {
		line = strings.TrimSpace(line)
		if language == LanguagePython && output.ExitCode == 1 && strings.HasSuffix(line, ": No module named pytest") {
			return true
		}
		if output.ExitCode == 127 {
			for _, name := range map[Language][]string{
				LanguageNode: {"node", "npm", "pnpm"}, LanguagePython: {"python3"}, LanguageGo: {"go"}, LanguageRust: {"cargo"},
			}[language] {
				if strings.HasSuffix(line, name+": not found") || strings.HasSuffix(line, name+": command not found") || strings.Contains(line, "'"+name+"': No such file or directory") {
					return true
				}
			}
		}
	}
	return false
}
