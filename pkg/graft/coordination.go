package graft

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Agent represents a registered agent in graft coordination.
type Agent struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Host      string `json:"host"`
}

// Coordinator manages graft coordination for Buckley agents.
type Coordinator struct {
	runner *Runner
	agent  string
}

// NewCoordinator creates a Coordinator for the named agent.
func NewCoordinator(runner *Runner, agentName string) *Coordinator {
	return &Coordinator{
		runner: runner,
		agent:  agentName,
	}
}

// AgentName returns the coordinator's agent name.
func (c *Coordinator) AgentName() string {
	return c.agent
}

// Join registers this agent in the coordination session.
func (c *Coordinator) Join(ctx context.Context) error {
	_, err := c.runner.Run(ctx, "workon", "--as", c.agent)
	if err != nil {
		return fmt.Errorf("joining coordination as %s: %w", c.agent, err)
	}
	return nil
}

// Leave deregisters this agent from the coordination session.
func (c *Coordinator) Leave(ctx context.Context) error {
	_, err := c.runner.Run(ctx, "workon", "--done", "--as", c.agent)
	if err != nil {
		return fmt.Errorf("leaving coordination as %s: %w", c.agent, err)
	}
	return nil
}

// CheckConflicts checks for coordination conflicts before commit.
// Returns true if clear to commit, false if conflicts exist.
func (c *Coordinator) CheckConflicts(ctx context.Context) (bool, error) {
	out, err := c.runner.Run(ctx, "coord", "check")
	if err != nil {
		// Non-zero exit from coord check means conflicts detected.
		return false, nil
	}
	// If output contains conflict indicators, report false.
	output := strings.ToLower(string(out))
	if strings.Contains(output, "conflict") {
		return false, nil
	}
	return true, nil
}

// ListAgents returns currently registered agents.
func (c *Coordinator) ListAgents(ctx context.Context) ([]Agent, error) {
	out, err := c.runner.Run(ctx, "coord", "agents", "--json")
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	var agents []Agent
	if err := json.Unmarshal(out, &agents); err != nil {
		// If JSON parsing fails, return the raw output as a single agent entry
		// to avoid losing information. This handles non-JSON output gracefully.
		return nil, fmt.Errorf("parsing agent list: %w", err)
	}
	return agents, nil
}

// Status returns the full coordination status as a string.
func (c *Coordinator) Status(ctx context.Context) (string, error) {
	out, err := c.runner.Run(ctx, "coord", "status")
	if err != nil {
		return "", fmt.Errorf("getting coordination status: %w", err)
	}
	return string(out), nil
}
