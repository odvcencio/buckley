package worktree

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindComposeFile locates a docker compose file for the current repository.
// It looks for .buckley/container.yaml overrides first, followed by common filenames.
func FindComposeFile(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("repo root not provided")
	}

	candidates := []string{}

	if spec, err := LoadContainerSpec(repoRoot); err == nil && spec != nil {
		if spec.ComposeFile != "" {
			candidates = append(candidates, filepath.Join(repoRoot, spec.ComposeFile))
		}
		// If spec doesn't provide a compose file, fallback to generated default.
		candidates = append(candidates, filepath.Join(repoRoot, "docker-compose.worktree.yml"))
	} else if err != nil {
		return "", fmt.Errorf("load container spec: %w", err)
	}

	// Standard filenames if no spec entry matched.
	candidates = append(candidates,
		filepath.Join(repoRoot, "docker-compose.worktree.yml"),
		filepath.Join(repoRoot, "docker-compose.yml"),
		filepath.Join(repoRoot, "compose.yaml"),
		filepath.Join(repoRoot, "compose.yml"),
	)

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no compose file found in %s (expected docker-compose.worktree.yml or docker-compose.yml)", repoRoot)
}
