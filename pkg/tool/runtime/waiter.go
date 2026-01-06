package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ConditionWaiter manages waiting for conditions before tool execution
type ConditionWaiter struct {
	containerID string
	serviceName string
	composeFile string
}

// NewConditionWaiter creates a new condition waiter
func NewConditionWaiter(serviceName, composeFile, containerID string) *ConditionWaiter {
	return &ConditionWaiter{
		containerID: containerID,
		serviceName: serviceName,
		composeFile: composeFile,
	}
}

// WaitForReady waits for service to be ready based on conditions
func (cw *ConditionWaiter) WaitForReady(conditions []Condition, logs io.Reader) error {
	if len(conditions) == 0 {
		// No conditions to wait for
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), getMaxTimeout(conditions))
	defer cancel()

	// Create combined condition (all must be true)
	combined := NewAndCondition(conditions)

	// Start log capture in background
	logCh := make(chan string, 100)
	go captureLogs(logs, logCh)

	// Start waiting
	result := WaitForWithResult(ctx, combined, 3, 2*time.Second)

	if !result.Success {
		// Collect diagnostic information
		diagnostics := cw.collectDiagnostics(result.Error, conditions, logCh)
		return fmt.Errorf("service %s failed to become ready: %s\n\nDiagnostics:\n%s",
			cw.serviceName, result.Error, diagnostics)
	}

	fmt.Printf("Service %s is ready after %v\n", cw.serviceName, result.Duration)
	return nil
}

// WaitForServiceHealth waits for containerized service healthy state
func (cw *ConditionWaiter) WaitForServiceHealth() error {
	// Try docker-compose healthcheck if available
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Use docker-compose ps to check service health
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for service %s to be healthy", cw.serviceName)
		case <-ticker.C:
			// Check if service is healthy
			cmd := runCommand(ctx, "docker", "compose", "-f", cw.composeFile, "ps", cw.serviceName, "--format", "json")
			output, err := cmd.Output()
			if err != nil {
				continue // Service might not be ready yet
			}

			// Parse JSON output for health status
			if strings.Contains(string(output), `"Health":"healthy"`) {
				return nil
			}
		}
	}
}

// WaitForContainerStart waits for container to be running
func (cw *ConditionWaiter) WaitForContainerStart() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for container %s to start", cw.containerID)
		case <-ticker.C:
			// Check if container is running
			cmd := runCommand(ctx, "docker", "ps", "--filter", "id="+cw.containerID, "--format", "{{.Status}}")
			output, err := cmd.Output()
			if err != nil {
				continue
			}

			status := strings.TrimSpace(string(output))
			if strings.Contains(status, "Up") {
				return nil
			}
		}
	}
}

// collectDiagnostics collects diagnostic information when waiting fails
func (cw *ConditionWaiter) collectDiagnostics(waitError error, conditions []Condition, logCh chan string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Error: %v\n\n", waitError))

	// Check each condition status
	sb.WriteString("Condition Status:\n")
	for i, condition := range conditions {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		satisfied, err := condition.Check(ctx)
		cancel()

		status := "✓ SATISFIED"
		if !satisfied {
			status = "✗ NOT SATISFIED"
		}
		if err != nil {
			status = fmt.Sprintf("✗ ERROR: %v", err)
		}

		sb.WriteString(fmt.Sprintf("  [%d] %s - %s\n", i+1, condition.String(), status))
	}

	// Collect recent logs
	sb.WriteString("\nRecent Container Logs:\n")
	logCount := 0
	maxLogs := 50
	timeout := time.After(2 * time.Second)

collectLogs:
	for logCount < maxLogs {
		select {
		case logLine := <-logCh:
			sb.WriteString(fmt.Sprintf("  %s\n", strings.TrimSpace(logLine)))
			logCount++
		case <-timeout:
			break collectLogs
		}
	}

	if logCount == 0 {
		sb.WriteString("  (No logs available)\n")
	}

	// Check container status
	sb.WriteString("\nContainer Status:\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get container logs
	cmd := runCommand(ctx, "docker", "logs", "--tail", "20", cw.containerID)
	logs, err := cmd.Output()
	if err == nil && len(logs) > 0 {
		sb.WriteString("Recent container logs:\n")
		for _, line := range strings.Split(string(logs), "\n") {
			if strings.TrimSpace(line) != "" {
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}

	// Get container status
	cmd = runCommand(ctx, "docker", "inspect", "--format", `{{.State.Status}}: {{.State.Error}}`, cw.containerID)
	status, err := cmd.Output()
	if err == nil {
		sb.WriteString(fmt.Sprintf("\nContainer State: %s\n", strings.TrimSpace(string(status))))
	}

	// Get service status from docker-compose
	cmd = runCommand(ctx, "docker", "compose", "-f", cw.composeFile, "ps", cw.serviceName)
	serviceStatus, err := cmd.Output()
	if err == nil && len(serviceStatus) > 0 {
		sb.WriteString(fmt.Sprintf("\nService Status:\n%s\n", string(serviceStatus)))
	}

	return sb.String()
}

// captureLogs captures logs from a reader and sends them to a channel
func captureLogs(reader io.Reader, logCh chan<- string) {
	if reader == nil {
		return
	}

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case logCh <- scanner.Text():
		case <-time.After(100 * time.Millisecond):
			// Channel full, drop log to avoid blocking
		}
	}
}

// getMaxTimeout gets the maximum timeout from a list of conditions
func getMaxTimeout(conditions []Condition) time.Duration {
	maxTimeout := 30 * time.Second
	for _, condition := range conditions {
		if condition.Timeout() > maxTimeout {
			maxTimeout = condition.Timeout()
		}
	}
	return maxTimeout + 10*time.Second // Add buffer
}

// runCommand is a helper to create exec commands
func runCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{} // Inherit environment
	return cmd
}

// ExecuteWithConditions executes a tool with precondition waiting
type ExecuteConfig struct {
	Tool       tool.Tool
	Params     map[string]any
	Conditions []Condition
	Service    string
	Compose    string
	Container  string
	LogSource  io.Reader
}

// ExecuteToolWithConditions executes a tool, waiting for conditions first
func ExecuteToolWithConditions(config ExecuteConfig) (*builtin.Result, error) {
	// Create waiter if we have conditions
	if len(config.Conditions) > 0 {
		waiter := NewConditionWaiter(config.Service, config.Compose, config.Container)

		fmt.Printf("Waiting for conditions before executing %s...\n", config.Tool.Name())
		if err := waiter.WaitForReady(config.Conditions, config.LogSource); err != nil {
			return nil, fmt.Errorf("conditions not met: %w", err)
		}
	}

	// Execute the tool
	return config.Tool.Execute(config.Params)
}

// EnrichResultWithWaitInfo adds wait information to tool execution result
func EnrichResultWithWaitInfo(result *builtin.Result, startTime time.Time, conditions []Condition) *builtin.Result {
	if result == nil {
		result = &builtin.Result{Data: make(map[string]any)}
	}

	if result.Data == nil {
		result.Data = make(map[string]any)
	}

	result.Data["wait_duration"] = time.Since(startTime).Milliseconds()
	result.Data["conditions_waited"] = len(conditions)
	result.Data["waited_for"] = formatConditions(conditions)

	return result
}

func formatConditions(conditions []Condition) string {
	var parts []string
	for _, condition := range conditions {
		parts = append(parts, condition.String())
	}
	return strings.Join(parts, "; ")
}
