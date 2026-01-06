package containers

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ServiceCLI provides CLI operations for docker compose services
type ServiceCLI struct {
	composePath string
}

// NewServiceCLI creates a new service CLI instance
func NewServiceCLI(composePath string) *ServiceCLI {
	return &ServiceCLI{
		composePath: composePath,
	}
}

// ServiceStatus represents the status of a service
type ServiceStatus struct {
	Name    string
	Service string
	State   string
	Health  string
	Ports   string
}

// Status returns the status of all services
func (sc *ServiceCLI) Status() ([]ServiceStatus, error) {
	cmd := exec.Command("docker", "compose", "-f", sc.composePath, "ps", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get service status: %w", err)
	}

	var statuses []ServiceStatus
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var status ServiceStatus
		if err := json.Unmarshal([]byte(line), &status); err != nil {
			// Try manual parsing if JSON unmarshal fails
			status = parseServiceStatus(line)
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// parseServiceStatus manually parses service status from JSON line
func parseServiceStatus(line string) ServiceStatus {
	status := ServiceStatus{State: "unknown"}

	// Extract fields from JSON
	if idx := strings.Index(line, `"Name":`); idx != -1 {
		rest := line[idx+7:]
		if startIdx := strings.Index(rest, `"`); startIdx != -1 && startIdx+1 < len(rest) {
			if endIdx := strings.Index(rest[startIdx+1:], `"`); endIdx != -1 {
				status.Name = rest[startIdx+1 : startIdx+1+endIdx]
			}
		}
	}

	if idx := strings.Index(line, `"Service":`); idx != -1 {
		rest := line[idx+10:]
		if startIdx := strings.Index(rest, `"`); startIdx != -1 && startIdx+1 < len(rest) {
			if endIdx := strings.Index(rest[startIdx+1:], `"`); endIdx != -1 {
				status.Service = rest[startIdx+1 : startIdx+1+endIdx]
			}
		}
	}

	if idx := strings.Index(line, `"State":`); idx != -1 {
		rest := line[idx+8:]
		if startIdx := strings.Index(rest, `"`); startIdx != -1 && startIdx+1 < len(rest) {
			if endIdx := strings.Index(rest[startIdx+1:], `"`); endIdx != -1 {
				status.State = rest[startIdx+1 : startIdx+1+endIdx]
			}
		}
	}

	return status
}

// Logs retrieves logs for a service
func (sc *ServiceCLI) Logs(service string, follow bool, tail int) (*exec.Cmd, error) {
	args := []string{"compose", "-f", sc.composePath, "logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	}
	args = append(args, service)

	cmd := exec.Command("docker", args...)
	return cmd, nil
}

// Exec shells into a service
func (sc *ServiceCLI) Exec(service string, command []string) error {
	args := append([]string{"compose", "-f", sc.composePath, "exec", service}, command...)
	cmd := exec.Command("docker", args...)
	cmd.Stdin = nil  // Caller should set
	cmd.Stdout = nil // Caller should set
	cmd.Stderr = nil // Caller should set

	return cmd.Run()
}

// Restart restarts a service
func (sc *ServiceCLI) Restart(service string) error {
	cmd := exec.Command("docker", "compose", "-f", sc.composePath, "restart", service)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart service: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Stop stops a service
func (sc *ServiceCLI) Stop(service string) error {
	cmd := exec.Command("docker", "compose", "-f", sc.composePath, "stop", service)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop service: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Start starts a service
func (sc *ServiceCLI) Start(service string) error {
	cmd := exec.Command("docker", "compose", "-f", sc.composePath, "start", service)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Down stops and removes all services
func (sc *ServiceCLI) Down(removeVolumes bool) error {
	args := []string{"compose", "-f", sc.composePath, "down"}
	if removeVolumes {
		args = append(args, "-v")
	}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop services: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// Up starts all services
func (sc *ServiceCLI) Up(detach bool) error {
	args := []string{"compose", "-f", sc.composePath, "up"}
	if detach {
		args = append(args, "-d", "--wait")
	}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start services: %w\nOutput: %s", err, string(output))
	}

	return nil
}
