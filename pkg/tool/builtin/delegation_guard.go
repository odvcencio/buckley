package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Delegation guardrails to prevent runaway recursive loops

const (
	// Environment variable used to track delegation depth across process boundaries
	DelegationDepthEnvVar = "BUCKLEY_DELEGATION_DEPTH"

	// Maximum allowed delegation depth (prevents infinite recursion)
	MaxDelegationDepth = 3

	// Rate limiting: max delegations per time window
	MaxDelegationsPerWindow = 10
	DelegationRateWindow    = 60 * time.Second

	// Cooldown between consecutive delegations to the same tool
	DelegationCooldown = 2 * time.Second
)

// DelegationGuard enforces safety limits on delegation operations
type DelegationGuard struct {
	mu sync.Mutex

	// Rate limiting
	delegationTimes []time.Time

	// Per-tool cooldown tracking
	lastDelegation map[string]time.Time
}

// Global guard instance
var globalDelegationGuard = &DelegationGuard{
	delegationTimes: make([]time.Time, 0),
	lastDelegation:  make(map[string]time.Time),
}

// GetDelegationGuard returns the singleton guard instance
func GetDelegationGuard() *DelegationGuard {
	return globalDelegationGuard
}

// GetCurrentDepth returns the current delegation depth from environment
func (g *DelegationGuard) GetCurrentDepth() int {
	depthStr := os.Getenv(DelegationDepthEnvVar)
	if depthStr == "" {
		return 0
	}
	depth, err := strconv.Atoi(depthStr)
	if err != nil {
		return 0
	}
	return depth
}

// CanDelegate checks if a delegation is allowed and returns an error if not
func (g *DelegationGuard) CanDelegate(toolName string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check depth limit
	currentDepth := g.GetCurrentDepth()
	if currentDepth >= MaxDelegationDepth {
		return fmt.Errorf("delegation depth limit exceeded (current: %d, max: %d). "+
			"This prevents infinite recursion. Consider breaking the task into smaller pieces "+
			"or handling it directly instead of delegating", currentDepth, MaxDelegationDepth)
	}

	// Check rate limit
	now := time.Now()
	cutoff := now.Add(-DelegationRateWindow)

	// Clean old entries
	validTimes := make([]time.Time, 0)
	for _, t := range g.delegationTimes {
		if t.After(cutoff) {
			validTimes = append(validTimes, t)
		}
	}
	g.delegationTimes = validTimes

	if len(g.delegationTimes) >= MaxDelegationsPerWindow {
		return fmt.Errorf("delegation rate limit exceeded (%d delegations in %v). "+
			"Wait before delegating again to prevent resource exhaustion",
			MaxDelegationsPerWindow, DelegationRateWindow)
	}

	// Check per-tool cooldown
	if lastTime, ok := g.lastDelegation[toolName]; ok {
		elapsed := now.Sub(lastTime)
		if elapsed < DelegationCooldown {
			remaining := DelegationCooldown - elapsed
			return fmt.Errorf("cooldown active for %s (%.1fs remaining). "+
				"This prevents rapid-fire delegations to the same tool",
				toolName, remaining.Seconds())
		}
	}

	return nil
}

// RecordDelegation records that a delegation occurred
func (g *DelegationGuard) RecordDelegation(toolName string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	g.delegationTimes = append(g.delegationTimes, now)
	g.lastDelegation[toolName] = now
}

// PrepareChildEnv creates environment variables for a child process with incremented depth
func (g *DelegationGuard) PrepareChildEnv() []string {
	currentDepth := g.GetCurrentDepth()
	newDepth := currentDepth + 1

	// Start with current environment
	env := os.Environ()

	// Replace or add the delegation depth
	found := false
	for i, e := range env {
		if len(e) > len(DelegationDepthEnvVar) && e[:len(DelegationDepthEnvVar)+1] == DelegationDepthEnvVar+"=" {
			env[i] = fmt.Sprintf("%s=%d", DelegationDepthEnvVar, newDepth)
			found = true
			break
		}
	}
	if !found {
		env = append(env, fmt.Sprintf("%s=%d", DelegationDepthEnvVar, newDepth))
	}

	return env
}

// IsSelfDelegation checks if the tool would delegate to itself (Buckley -> Buckley)
func (g *DelegationGuard) IsSelfDelegation(toolName string) bool {
	// Check if we're already in a delegated context
	return toolName == "invoke_buckley" && g.GetCurrentDepth() > 0
}

// DelegationStats returns current delegation statistics for debugging
func (g *DelegationGuard) DelegationStats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-DelegationRateWindow)

	activeCount := 0
	for _, t := range g.delegationTimes {
		if t.After(cutoff) {
			activeCount++
		}
	}

	currentDepth := g.GetCurrentDepth()

	// Check if delegation would be allowed (without calling CanDelegate to avoid deadlock)
	canDelegate := currentDepth < MaxDelegationDepth && activeCount < MaxDelegationsPerWindow

	return map[string]any{
		"current_depth":         currentDepth,
		"max_depth":             MaxDelegationDepth,
		"delegations_in_window": activeCount,
		"max_delegations":       MaxDelegationsPerWindow,
		"rate_window_seconds":   DelegationRateWindow.Seconds(),
		"cooldown_seconds":      DelegationCooldown.Seconds(),
		"can_delegate":          canDelegate,
		"is_delegated_context":  currentDepth > 0,
	}
}

// ConfigureCommand sets up a command with proper environment for delegation
func (g *DelegationGuard) ConfigureCommand(cmd *exec.Cmd) {
	cmd.Env = g.PrepareChildEnv()
}

// CheckAndRecord performs the full delegation check and records it if allowed
func (g *DelegationGuard) CheckAndRecord(toolName string) error {
	if err := g.CanDelegate(toolName); err != nil {
		return err
	}
	g.RecordDelegation(toolName)
	return nil
}
