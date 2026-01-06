// Package coordinator provides Buckley-internal coordination primitives (not ACP).
package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/coordination/events"
)

// Coordinator manages Buckley-internal agent coordination (distinct from the Zed ACP protocol).
type Coordinator struct {
	config     *Config
	eventStore events.EventStore

	mu        sync.RWMutex
	agents    map[string]*AgentInfo
	p2pTokens map[string]*P2PToken
}

// AgentInfo holds information about a registered agent
type AgentInfo struct {
	ID           string
	Type         string
	Endpoint     string
	Capabilities []string
	Metadata     map[string]string
}

// DiscoveryQuery specifies criteria for agent discovery
type DiscoveryQuery struct {
	Type         string
	Capabilities []string
	Tags         map[string]string
}

// P2PToken represents a temporary token for P2P connection establishment
type P2PToken struct {
	TokenID     string
	RequesterID string
	TargetID    string
	IssuedAt    time.Time
	ExpiresAt   time.Time
	Used        bool
}

// NewCoordinator creates a new coordinator instance
func NewCoordinator(config *Config, eventStore events.EventStore) (*Coordinator, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if eventStore == nil {
		return nil, fmt.Errorf("event store is required")
	}

	return &Coordinator{
		config:     config,
		eventStore: eventStore,
		agents:     make(map[string]*AgentInfo),
		p2pTokens:  make(map[string]*P2PToken),
	}, nil
}

// Config returns the coordinator configuration
func (c *Coordinator) Config() *Config {
	return c.config
}

// RegisterAgent registers a new agent with the coordinator
func (c *Coordinator) RegisterAgent(ctx context.Context, agent *AgentInfo) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if agent already registered
	if _, exists := c.agents[agent.ID]; exists {
		return "", fmt.Errorf("agent %s already registered", agent.ID)
	}

	// Check max agents limit
	if len(c.agents) >= c.config.MaxAgents {
		return "", fmt.Errorf("max agents limit reached (%d)", c.config.MaxAgents)
	}

	// Store agent
	c.agents[agent.ID] = agent

	// Emit event
	event := events.NewAgentRegisteredEvent(agent.ID, agent.Capabilities, agent.Endpoint)
	event.StreamID = "coordinator"
	event.Version = int64(len(c.agents))
	if err := c.eventStore.Append(ctx, "coordinator", []events.Event{event}); err != nil {
		// Rollback registration
		delete(c.agents, agent.ID)
		return "", fmt.Errorf("failed to emit event: %w", err)
	}

	// Generate session token
	token := generateToken()

	return token, nil
}

// GetAgent retrieves agent information
func (c *Coordinator) GetAgent(ctx context.Context, agentID string) (*AgentInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	agent, exists := c.agents[agentID]
	if !exists {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}

	return agent, nil
}

// UnregisterAgent removes an agent
func (c *Coordinator) UnregisterAgent(ctx context.Context, agentID string, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.agents[agentID]; !exists {
		return fmt.Errorf("agent %s not found", agentID)
	}

	delete(c.agents, agentID)

	// Emit event
	event := events.NewAgentUnregisteredEvent(agentID, reason)
	event.StreamID = "coordinator"
	event.Version = int64(len(c.agents) + 1)
	return c.eventStore.Append(ctx, "coordinator", []events.Event{event})
}

// DiscoverAgents finds agents matching the query
func (c *Coordinator) DiscoverAgents(ctx context.Context, query *DiscoveryQuery) ([]*AgentInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var results []*AgentInfo

	for _, agent := range c.agents {
		if matchesQuery(agent, query) {
			results = append(results, agent)
		}
	}

	return results, nil
}

// matchesQuery checks if an agent matches discovery criteria
func matchesQuery(agent *AgentInfo, query *DiscoveryQuery) bool {
	// Check type
	if query.Type != "" && agent.Type != query.Type {
		return false
	}

	// Check capabilities (agent must have ALL requested capabilities)
	for _, reqCap := range query.Capabilities {
		found := false
		for _, agentCap := range agent.Capabilities {
			if agentCap == reqCap {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check tags
	for key, value := range query.Tags {
		if agent.Metadata[key] != value {
			return false
		}
	}

	return true
}

// generateToken creates a random session token
func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GetP2PEndpoint returns the endpoint and temporary token for P2P connection
func (c *Coordinator) GetP2PEndpoint(ctx context.Context, requesterID, targetAgentID string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Validate requester exists
	requester, exists := c.agents[requesterID]
	if !exists {
		return "", "", fmt.Errorf("requester agent %s not found", requesterID)
	}

	// Validate target exists
	target, exists := c.agents[targetAgentID]
	if !exists {
		return "", "", fmt.Errorf("target agent %s not found", targetAgentID)
	}

	// Generate P2P token with 5-minute expiration
	token := &P2PToken{
		TokenID:     generateToken(),
		RequesterID: requester.ID,
		TargetID:    target.ID,
		IssuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(5 * time.Minute),
		Used:        false,
	}

	// Store token
	c.p2pTokens[token.TokenID] = token

	return target.Endpoint, token.TokenID, nil
}

// ValidateP2PToken validates a P2P token and returns requester/target info
func (c *Coordinator) ValidateP2PToken(ctx context.Context, tokenID string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if token exists
	token, exists := c.p2pTokens[tokenID]
	if !exists {
		return "", "", fmt.Errorf("invalid token")
	}

	// Check if token has already been used
	if token.Used {
		return "", "", fmt.Errorf("token already used")
	}

	// Check expiration
	if time.Now().After(token.ExpiresAt) {
		delete(c.p2pTokens, tokenID)
		return "", "", fmt.Errorf("token expired")
	}

	// Mark token as used
	token.Used = true

	return token.RequesterID, token.TargetID, nil
}
