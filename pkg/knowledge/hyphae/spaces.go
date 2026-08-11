package hyphae

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const spacesTimeout = 750 * time.Millisecond

// Space identifies one locally installed Hyphae space.
type Space struct {
	URI  string `json:"URI"`
	Path string `json:"Path"`
}

type spacesListResponse struct {
	Data []Space `json:"data"`
}

// ProjectKnowledgeContext returns bounded prompt guidance when a local Hyphae
// installation contains a space that matches the active project. Discovery is
// optional and never blocks a Buckley session.
func ProjectKnowledgeContext(ctx context.Context, workDir, configuredSpace string) string {
	space, found, err := DiscoverProjectSpace(ctx, defaultBinary, workDir, configuredSpace, runCommand)
	if err != nil || !found {
		return ""
	}
	return formatProjectKnowledgeContext(space)
}

// DiscoverProjectSpace finds a configured space or a locally installed space
// whose final URI segment matches the project directory name.
func DiscoverProjectSpace(ctx context.Context, binary, workDir, configuredSpace string, run commandRunner) (Space, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = defaultBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return Space{}, false, nil
	}
	if run == nil {
		run = runCommand
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, spacesTimeout)
	defer cancel()
	output, err := run(discoveryCtx, binary, "spaces", "list", "--format", "json")
	if err != nil {
		return Space{}, false, fmt.Errorf("list Hyphae spaces: %w", err)
	}
	var response spacesListResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return Space{}, false, fmt.Errorf("decode Hyphae spaces: %w", err)
	}
	return selectProjectSpace(response.Data, workDir, configuredSpace)
}

func selectProjectSpace(spaces []Space, workDir, configuredSpace string) (Space, bool, error) {
	configuredSpace = normalizeSpaceURI(configuredSpace)
	if configuredSpace != "" {
		for _, space := range spaces {
			if normalizeSpaceURI(space.URI) == configuredSpace {
				return normalizedSpace(space), true, nil
			}
		}
		return Space{}, false, fmt.Errorf("configured Hyphae space %q is not installed", configuredSpace)
	}

	projectName := strings.ToLower(strings.TrimSpace(filepath.Base(filepath.Clean(workDir))))
	if projectName == "" || projectName == "." || projectName == string(filepath.Separator) {
		return Space{}, false, nil
	}
	var match Space
	for _, space := range spaces {
		candidate := strings.ToLower(spaceName(space.URI))
		if candidate != projectName {
			continue
		}
		if match.URI != "" {
			return Space{}, false, nil
		}
		match = normalizedSpace(space)
	}
	if match.URI == "" {
		return Space{}, false, nil
	}
	return match, true, nil
}

func formatProjectKnowledgeContext(space Space) string {
	return fmt.Sprintf(`Hyphae Project Knowledge:
- A local, durable project knowledge space is available: %s.
- Before planning, architecture, review, or behavior changes, recall relevant decisions, specs, lessons, and prior work with hypha recall "<focused question>" --shape summary+anchors --format text --max-tokens 600.
- Use hypha show <anchor> to inspect a cited object and hypha pulse --space %s --window 30d --format text for recent project activity.
- Treat recalled knowledge as evidence: honor the current user request, verify code-facing claims against the repository, and do not invent a decision or write directly into a space.`, space.URI, space.URI)
}

func normalizedSpace(space Space) Space {
	space.URI = normalizeSpaceURI(space.URI)
	space.Path = strings.TrimSpace(space.Path)
	return space
}

func normalizeSpaceURI(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "hypha://")
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	return "hypha://" + value
}

func spaceName(uri string) string {
	uri = strings.TrimPrefix(normalizeSpaceURI(uri), "hypha://")
	uri = strings.Trim(uri, "/")
	parts := strings.Split(uri, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
