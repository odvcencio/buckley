package ipc

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/mission"
)

func TestFilterAgentsBySession(t *testing.T) {
	agents := []*mission.AgentStatus{
		{AgentID: "a1", SessionID: "s1"},
		{AgentID: "a2", SessionID: "s2"},
		{AgentID: "a3", SessionID: "s1"},
		nil, // should be skipped
	}

	tests := []struct {
		name      string
		sessionID string
		wantCount int
	}{
		{"empty session ID returns all", "", 4}, // returns original slice including nil but all non-nil are returned
		{"filter by s1", "s1", 2},
		{"filter by s2", "s2", 1},
		{"filter by nonexistent", "s3", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterAgentsBySession(agents, tt.sessionID)
			if len(result) != tt.wantCount {
				t.Errorf("filterAgentsBySession() got %d agents, want %d", len(result), tt.wantCount)
			}
			// Verify filtered agents match the session ID
			if tt.sessionID != "" {
				for _, agent := range result {
					if agent.SessionID != tt.sessionID {
						t.Errorf("agent %s has sessionID %s, want %s", agent.AgentID, agent.SessionID, tt.sessionID)
					}
				}
			}
		})
	}
}

func TestFilterChangesBySession(t *testing.T) {
	changes := []*mission.PendingChange{
		{ID: "c1", SessionID: "s1"},
		{ID: "c2", SessionID: "s2"},
		{ID: "c3", SessionID: "s1"},
		nil, // should be skipped
	}

	tests := []struct {
		name      string
		sessionID string
		wantCount int
	}{
		{"empty session ID returns all", "", 4}, // returns original slice including nil
		{"filter by s1", "s1", 2},
		{"filter by s2", "s2", 1},
		{"filter by nonexistent", "s3", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterChangesBySession(changes, tt.sessionID)
			if len(result) != tt.wantCount {
				t.Errorf("filterChangesBySession() got %d changes, want %d", len(result), tt.wantCount)
			}
			// Verify filtered changes match the session ID
			if tt.sessionID != "" {
				for _, change := range result {
					if change.SessionID != tt.sessionID {
						t.Errorf("change %s has sessionID %s, want %s", change.ID, change.SessionID, tt.sessionID)
					}
				}
			}
		})
	}
}

func TestFilterAgentsBySessionNil(t *testing.T) {
	result := filterAgentsBySession(nil, "s1")
	if result != nil && len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(result))
	}
}

func TestFilterChangesBySessionNil(t *testing.T) {
	result := filterChangesBySession(nil, "s1")
	if result != nil && len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(result))
	}
}

func TestFilterAgentsBySessionEmpty(t *testing.T) {
	agents := []*mission.AgentStatus{}
	result := filterAgentsBySession(agents, "s1")
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(result))
	}
}

func TestFilterChangesBySessionEmpty(t *testing.T) {
	changes := []*mission.PendingChange{}
	result := filterChangesBySession(changes, "s1")
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(result))
	}
}
