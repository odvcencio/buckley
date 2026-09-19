package agent

import (
	"testing"
)

// Subscribe to status updates

// Start the agent

// Should be active now

// Wait for status publish

// Cancel

// Register handler before starting

// Send a message to the agent

// Agent1 sends to Agent2

// Subscribe to task events

// Publish a task event

// Subscribe to task events for resolve

func TestRoleConstants(t *testing.T) {
	roles := []Role{RoleResearcher, RoleCoder, RoleReviewer, RolePlanner, RoleExecutor}
	expected := []string{"researcher", "coder", "reviewer", "planner", "executor"}

	for i, role := range roles {
		if string(role) != expected[i] {
			t.Errorf("Expected role %q, got %q", expected[i], role)
		}
	}
}
