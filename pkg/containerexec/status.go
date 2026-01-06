package containerexec

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ServiceStatus represents docker compose service status.
type ServiceStatus struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// GetStatus runs `docker compose ps` and returns service statuses.
func GetStatus(composePath string) ([]ServiceStatus, error) {
	if composePath == "" {
		return nil, fmt.Errorf("compose file not provided")
	}

	cmd := exec.Command("docker", "compose", "-f", composePath, "ps", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps: %w", err)
	}

	return parseStatus(output)
}

func parseStatus(output []byte) ([]ServiceStatus, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	statuses := make([]ServiceStatus, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var status ServiceStatus
		if err := json.Unmarshal([]byte(line), &status); err != nil {
			return nil, fmt.Errorf("parse compose status: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}
