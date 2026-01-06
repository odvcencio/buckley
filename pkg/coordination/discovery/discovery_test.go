package discovery

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test basic service registration and retrieval
func TestInMemoryDiscovery_Register(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	service := ServiceInfo{
		ID:           "agent-1",
		Type:         ServiceTypeAgent,
		Endpoint:     "localhost:50051",
		Capabilities: []string{"execute_tools", "read_files"},
		Metadata: map[string]string{
			"version": "1.0",
			"region":  "us-west-2",
		},
		Health: HealthStatusHealthy,
	}

	err := disc.Register(ctx, service)
	require.NoError(t, err)

	// Discover all services
	query := DiscoveryQuery{}
	services, err := disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "agent-1", services[0].ID)
	assert.Equal(t, ServiceTypeAgent, services[0].Type)
	assert.Equal(t, "localhost:50051", services[0].Endpoint)
}

// Test discovering services by type
func TestInMemoryDiscovery_DiscoverByType(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	// Register multiple services of different types
	services := []ServiceInfo{
		{
			ID:       "coordinator-1",
			Type:     ServiceTypeCoordinator,
			Endpoint: "localhost:50052",
			Health:   HealthStatusHealthy,
		},
		{
			ID:       "agent-1",
			Type:     ServiceTypeAgent,
			Endpoint: "localhost:50051",
			Health:   HealthStatusHealthy,
		},
		{
			ID:       "agent-2",
			Type:     ServiceTypeAgent,
			Endpoint: "localhost:50053",
			Health:   HealthStatusHealthy,
		},
		{
			ID:       "lsp-bridge-1",
			Type:     ServiceTypeLSPBridge,
			Endpoint: "localhost:50054",
			Health:   HealthStatusHealthy,
		},
	}

	for _, svc := range services {
		require.NoError(t, disc.Register(ctx, svc))
	}

	// Query for agents only
	query := DiscoveryQuery{
		Type: ServiceTypeAgent,
	}
	agents, err := disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	assert.ElementsMatch(t, []string{"agent-1", "agent-2"}, []string{agents[0].ID, agents[1].ID})
}

// Test discovering services by capabilities
func TestInMemoryDiscovery_DiscoverByCapabilities(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	// Register services with different capabilities
	services := []ServiceInfo{
		{
			ID:           "agent-1",
			Type:         ServiceTypeAgent,
			Endpoint:     "localhost:50051",
			Capabilities: []string{"execute_tools", "read_files"},
			Health:       HealthStatusHealthy,
		},
		{
			ID:           "agent-2",
			Type:         ServiceTypeAgent,
			Endpoint:     "localhost:50052",
			Capabilities: []string{"execute_tools", "write_files"},
			Health:       HealthStatusHealthy,
		},
		{
			ID:           "agent-3",
			Type:         ServiceTypeAgent,
			Endpoint:     "localhost:50053",
			Capabilities: []string{"read_files", "write_files"},
			Health:       HealthStatusHealthy,
		},
	}

	for _, svc := range services {
		require.NoError(t, disc.Register(ctx, svc))
	}

	// Query for agents with both execute_tools and read_files
	query := DiscoveryQuery{
		Type:         ServiceTypeAgent,
		Capabilities: []string{"execute_tools", "read_files"},
	}
	agents, err := disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "agent-1", agents[0].ID)

	// Query for agents with write_files
	query = DiscoveryQuery{
		Capabilities: []string{"write_files"},
	}
	agents, err = disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	assert.ElementsMatch(t, []string{"agent-2", "agent-3"}, []string{agents[0].ID, agents[1].ID})
}

// Test service unregistration
func TestInMemoryDiscovery_Unregister(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	service := ServiceInfo{
		ID:       "agent-1",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50051",
		Health:   HealthStatusHealthy,
	}

	require.NoError(t, disc.Register(ctx, service))

	// Verify service is registered
	services, err := disc.Discover(ctx, DiscoveryQuery{})
	require.NoError(t, err)
	require.Len(t, services, 1)

	// Unregister service
	err = disc.Unregister(ctx, "agent-1")
	require.NoError(t, err)

	// Verify service is gone
	services, err = disc.Discover(ctx, DiscoveryQuery{})
	require.NoError(t, err)
	require.Len(t, services, 0)
}

// Test health checking removes unhealthy services
func TestHealthChecker_RemovesUnhealthyServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	disc := NewInMemoryDiscovery()

	// Register healthy and unhealthy services
	healthyService := ServiceInfo{
		ID:       "healthy-agent",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50051",
		Health:   HealthStatusHealthy,
	}
	unhealthyService := ServiceInfo{
		ID:       "unhealthy-agent",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50052",
		Health:   HealthStatusUnhealthy,
	}

	require.NoError(t, disc.Register(ctx, healthyService))
	require.NoError(t, disc.Register(ctx, unhealthyService))

	// Create health checker with short interval
	checker := NewHealthChecker(disc, 100*time.Millisecond, func(svc ServiceInfo) bool {
		// Mock health check: return false for unhealthy-agent
		return svc.ID != "unhealthy-agent"
	})

	// Start health checker in background
	go checker.Start(ctx)

	// Wait for health check to run
	time.Sleep(300 * time.Millisecond)

	// Verify unhealthy service was removed
	services, err := disc.Discover(ctx, DiscoveryQuery{})
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "healthy-agent", services[0].ID)
}

// Test watch notifications on service changes
func TestInMemoryDiscovery_Watch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	disc := NewInMemoryDiscovery()

	// Start watching for service changes
	query := DiscoveryQuery{
		Type: ServiceTypeAgent,
	}
	eventChan, err := disc.Watch(ctx, query)
	require.NoError(t, err)

	// Register a service
	service := ServiceInfo{
		ID:       "agent-1",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50051",
		Health:   HealthStatusHealthy,
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		disc.Register(context.Background(), service)
	}()

	// Wait for registration event
	select {
	case event := <-eventChan:
		assert.Equal(t, ServiceEventRegistered, event.Type)
		assert.Equal(t, "agent-1", event.Service.ID)
	case <-ctx.Done():
		t.Fatal("timeout waiting for registration event")
	}

	// Unregister the service
	go func() {
		time.Sleep(100 * time.Millisecond)
		disc.Unregister(context.Background(), "agent-1")
	}()

	// Wait for unregistration event
	select {
	case event := <-eventChan:
		assert.Equal(t, ServiceEventUnregistered, event.Type)
		assert.Equal(t, "agent-1", event.Service.ID)
	case <-ctx.Done():
		t.Fatal("timeout waiting for unregistration event")
	}
}

// Test concurrent registration and discovery
func TestInMemoryDiscovery_Concurrent(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	const numGoroutines = 10
	const servicesPerGoroutine = 10

	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < servicesPerGoroutine; j++ {
				service := ServiceInfo{
					ID:       fmt.Sprintf("agent-%d-%d", goroutineID, j),
					Type:     ServiceTypeAgent,
					Endpoint: fmt.Sprintf("localhost:5%04d", goroutineID*100+j),
					Health:   HealthStatusHealthy,
				}
				err := disc.Register(ctx, service)
				assert.NoError(t, err)
			}
		}(i)
	}

	// Concurrent discoveries
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < servicesPerGoroutine; j++ {
				query := DiscoveryQuery{
					Type: ServiceTypeAgent,
				}
				_, err := disc.Discover(ctx, query)
				assert.NoError(t, err)
			}
		}()
	}

	wg.Wait()

	// Verify all services were registered
	services, err := disc.Discover(ctx, DiscoveryQuery{})
	require.NoError(t, err)
	assert.Len(t, services, numGoroutines*servicesPerGoroutine)
}

// Test discovering services by tags (metadata)
func TestInMemoryDiscovery_DiscoverByTags(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	services := []ServiceInfo{
		{
			ID:       "agent-1",
			Type:     ServiceTypeAgent,
			Endpoint: "localhost:50051",
			Metadata: map[string]string{
				"region":  "us-west-2",
				"version": "1.0",
			},
			Health: HealthStatusHealthy,
		},
		{
			ID:       "agent-2",
			Type:     ServiceTypeAgent,
			Endpoint: "localhost:50052",
			Metadata: map[string]string{
				"region":  "us-east-1",
				"version": "1.0",
			},
			Health: HealthStatusHealthy,
		},
		{
			ID:       "agent-3",
			Type:     ServiceTypeAgent,
			Endpoint: "localhost:50053",
			Metadata: map[string]string{
				"region":  "us-west-2",
				"version": "2.0",
			},
			Health: HealthStatusHealthy,
		},
	}

	for _, svc := range services {
		require.NoError(t, disc.Register(ctx, svc))
	}

	// Query for services in us-west-2
	query := DiscoveryQuery{
		Tags: map[string]string{
			"region": "us-west-2",
		},
	}
	agents, err := disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	assert.ElementsMatch(t, []string{"agent-1", "agent-3"}, []string{agents[0].ID, agents[1].ID})

	// Query for services with version 1.0
	query = DiscoveryQuery{
		Tags: map[string]string{
			"version": "1.0",
		},
	}
	agents, err = disc.Discover(ctx, query)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	assert.ElementsMatch(t, []string{"agent-1", "agent-2"}, []string{agents[0].ID, agents[1].ID})
}

// Test duplicate registration updates existing service
func TestInMemoryDiscovery_DuplicateRegistrationUpdates(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	// Register initial service
	service := ServiceInfo{
		ID:       "agent-1",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50051",
		Health:   HealthStatusHealthy,
		Metadata: map[string]string{
			"version": "1.0",
		},
	}
	require.NoError(t, disc.Register(ctx, service))

	// Update service with new metadata
	updatedService := ServiceInfo{
		ID:       "agent-1",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50052", // Changed endpoint
		Health:   HealthStatusHealthy,
		Metadata: map[string]string{
			"version": "2.0", // Updated version
		},
	}
	require.NoError(t, disc.Register(ctx, updatedService))

	// Verify service was updated, not duplicated
	services, err := disc.Discover(ctx, DiscoveryQuery{})
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "localhost:50052", services[0].Endpoint)
	assert.Equal(t, "2.0", services[0].Metadata["version"])
}

// Test watch with filtering
func TestInMemoryDiscovery_WatchWithFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	disc := NewInMemoryDiscovery()

	// Watch only for coordinator services
	query := DiscoveryQuery{
		Type: ServiceTypeCoordinator,
	}
	eventChan, err := disc.Watch(ctx, query)
	require.NoError(t, err)

	// Register an agent (should not trigger event)
	agentService := ServiceInfo{
		ID:       "agent-1",
		Type:     ServiceTypeAgent,
		Endpoint: "localhost:50051",
		Health:   HealthStatusHealthy,
	}

	// Register a coordinator (should trigger event)
	coordinatorService := ServiceInfo{
		ID:       "coordinator-1",
		Type:     ServiceTypeCoordinator,
		Endpoint: "localhost:50052",
		Health:   HealthStatusHealthy,
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		disc.Register(context.Background(), agentService)
		time.Sleep(100 * time.Millisecond)
		disc.Register(context.Background(), coordinatorService)
	}()

	// Should only receive coordinator event
	select {
	case event := <-eventChan:
		assert.Equal(t, ServiceEventRegistered, event.Type)
		assert.Equal(t, "coordinator-1", event.Service.ID)
		assert.Equal(t, ServiceTypeCoordinator, event.Service.Type)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for coordinator registration event")
	}
}
