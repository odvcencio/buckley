package coordinator

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/coordination/capabilities"
	"github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCoordinator(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	config := &Config{
		Address:       "localhost:50052",
		Features:      []capabilities.Feature{capabilities.FeatureStreamingTasks},
		MaxAgents:     10,
		SupportedAuth: []string{"bearer"},
	}

	coord, err := NewCoordinator(config, eventStore)
	require.NoError(t, err)
	require.NotNil(t, coord)

	assert.Equal(t, "localhost:50052", coord.config.Address)
	assert.Len(t, coord.config.Features, 1)
}

func TestRegisterAgent(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := NewCoordinator(DefaultConfig(), eventStore)
	ctx := context.Background()

	t.Run("successful registration", func(t *testing.T) {
		agent := &AgentInfo{
			ID:           "agent-1",
			Type:         "builder",
			Endpoint:     "localhost:50053",
			Capabilities: []string{"read_files", "write_files"},
			Metadata:     map[string]string{"version": "1.0"},
		}

		token, err := coord.RegisterAgent(ctx, agent)
		require.NoError(t, err)
		assert.NotEmpty(t, token)

		// Verify agent is registered
		registered, err := coord.GetAgent(ctx, "agent-1")
		require.NoError(t, err)
		assert.Equal(t, "agent-1", registered.ID)
		assert.Equal(t, "builder", registered.Type)
	})

	t.Run("duplicate registration fails", func(t *testing.T) {
		agent := &AgentInfo{
			ID:       "agent-2",
			Type:     "reviewer",
			Endpoint: "localhost:50054",
		}

		_, err := coord.RegisterAgent(ctx, agent)
		require.NoError(t, err)

		// Try to register again
		_, err = coord.RegisterAgent(ctx, agent)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("max agents limit", func(t *testing.T) {
		// Create coordinator with max 2 agents
		config := DefaultConfig()
		config.MaxAgents = 2
		coord2, _ := NewCoordinator(config, events.NewInMemoryStore())

		// Register 2 agents
		_, err := coord2.RegisterAgent(ctx, &AgentInfo{ID: "a1", Endpoint: "e1"})
		require.NoError(t, err)
		_, err = coord2.RegisterAgent(ctx, &AgentInfo{ID: "a2", Endpoint: "e2"})
		require.NoError(t, err)

		// Third should fail
		_, err = coord2.RegisterAgent(ctx, &AgentInfo{ID: "a3", Endpoint: "e3"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "max agents")
	})
}

func TestDiscoverAgents(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := NewCoordinator(DefaultConfig(), eventStore)
	ctx := context.Background()

	// Register test agents
	agents := []*AgentInfo{
		{ID: "builder-1", Type: "builder", Endpoint: "e1", Capabilities: []string{"write_files"}},
		{ID: "builder-2", Type: "builder", Endpoint: "e2", Capabilities: []string{"write_files", "execute_shell"}},
		{ID: "reviewer-1", Type: "reviewer", Endpoint: "e3", Capabilities: []string{"read_files"}},
	}
	for _, a := range agents {
		coord.RegisterAgent(ctx, a)
	}

	t.Run("discover by type", func(t *testing.T) {
		query := &DiscoveryQuery{Type: "builder"}
		results, err := coord.DiscoverAgents(ctx, query)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("discover by capability", func(t *testing.T) {
		query := &DiscoveryQuery{Capabilities: []string{"execute_shell"}}
		results, err := coord.DiscoverAgents(ctx, query)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, "builder-2", results[0].ID)
	})

	t.Run("discover by type and capability", func(t *testing.T) {
		query := &DiscoveryQuery{Type: "builder", Capabilities: []string{"write_files"}}
		results, err := coord.DiscoverAgents(ctx, query)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("no matches", func(t *testing.T) {
		query := &DiscoveryQuery{Type: "nonexistent"}
		results, err := coord.DiscoverAgents(ctx, query)
		require.NoError(t, err)
		assert.Len(t, results, 0)
	})
}

func TestGetP2PEndpoint(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := NewCoordinator(DefaultConfig(), eventStore)
	ctx := context.Background()

	// Register two agents
	agent1 := &AgentInfo{
		ID:           "agent-1",
		Type:         "builder",
		Endpoint:     "localhost:50053",
		Capabilities: []string{string(capabilities.CapP2PMesh)},
	}
	agent2 := &AgentInfo{
		ID:       "agent-2",
		Type:     "reviewer",
		Endpoint: "localhost:50054",
	}
	_, err := coord.RegisterAgent(ctx, agent1)
	require.NoError(t, err)
	_, err = coord.RegisterAgent(ctx, agent2)
	require.NoError(t, err)

	t.Run("successful endpoint request", func(t *testing.T) {
		endpoint, token, err := coord.GetP2PEndpoint(ctx, "agent-1", "agent-2")
		require.NoError(t, err)
		assert.NotEmpty(t, endpoint)
		assert.NotEmpty(t, token)
		assert.Equal(t, "localhost:50054", endpoint)
	})

	t.Run("target agent not found", func(t *testing.T) {
		_, _, err := coord.GetP2PEndpoint(ctx, "agent-1", "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("requester agent not found", func(t *testing.T) {
		_, _, err := coord.GetP2PEndpoint(ctx, "nonexistent", "agent-2")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestValidateP2PToken(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := NewCoordinator(DefaultConfig(), eventStore)
	ctx := context.Background()

	// Register agents
	agent1 := &AgentInfo{
		ID:           "agent-1",
		Endpoint:     "localhost:50053",
		Capabilities: []string{string(capabilities.CapP2PMesh)},
	}
	agent2 := &AgentInfo{
		ID:       "agent-2",
		Endpoint: "localhost:50054",
	}
	_, err := coord.RegisterAgent(ctx, agent1)
	require.NoError(t, err)
	_, err = coord.RegisterAgent(ctx, agent2)
	require.NoError(t, err)

	t.Run("valid token", func(t *testing.T) {
		// Get a valid token
		_, token, err := coord.GetP2PEndpoint(ctx, "agent-1", "agent-2")
		require.NoError(t, err)

		// Validate the token
		requesterID, targetID, err := coord.ValidateP2PToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, "agent-1", requesterID)
		assert.Equal(t, "agent-2", targetID)
	})

	t.Run("invalid token", func(t *testing.T) {
		_, _, err := coord.ValidateP2PToken(ctx, "invalid-token")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("expired token", func(t *testing.T) {
		// This test will need manual time manipulation
		// For now we'll skip the implementation as it requires time mocking
		t.Skip("Time mocking required for expiration test")
	})

	t.Run("token can only be used once", func(t *testing.T) {
		_, token, err := coord.GetP2PEndpoint(ctx, "agent-1", "agent-2")
		require.NoError(t, err)

		// First validation should succeed
		_, _, err = coord.ValidateP2PToken(ctx, token)
		require.NoError(t, err)

		// Second validation should fail
		_, _, err = coord.ValidateP2PToken(ctx, token)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already used")
	})
}
