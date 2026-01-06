package discovery

import (
	"context"
	"sync"
	"time"
)

// ServiceType represents the type of service
type ServiceType string

const (
	ServiceTypeCoordinator ServiceType = "coordinator"
	ServiceTypeAgent       ServiceType = "agent"
	ServiceTypeLSPBridge   ServiceType = "lsp_bridge"
)

// HealthStatus represents the health state of a service
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusUnknown   HealthStatus = "unknown"
)

// ServiceInfo contains information about a registered service
type ServiceInfo struct {
	ID           string
	Type         ServiceType
	Endpoint     string
	Capabilities []string
	Metadata     map[string]string
	Health       HealthStatus
}

// DiscoveryQuery is used to filter services during discovery
type DiscoveryQuery struct {
	Type         ServiceType
	Capabilities []string          // Service must have ALL these capabilities
	Tags         map[string]string // Service metadata must match ALL these tags
}

// ServiceEventType represents the type of service event
type ServiceEventType string

const (
	ServiceEventRegistered   ServiceEventType = "registered"
	ServiceEventUnregistered ServiceEventType = "unregistered"
	ServiceEventUpdated      ServiceEventType = "updated"
)

// ServiceEvent represents a change in service registry
type ServiceEvent struct {
	Type    ServiceEventType
	Service ServiceInfo
}

// DiscoveryService defines the interface for service discovery
type DiscoveryService interface {
	// Register registers a service in the registry
	Register(ctx context.Context, service ServiceInfo) error

	// Unregister removes a service from the registry
	Unregister(ctx context.Context, serviceID string) error

	// Discover finds services matching the query
	Discover(ctx context.Context, query DiscoveryQuery) ([]ServiceInfo, error)

	// Watch returns a channel that receives service events matching the query
	Watch(ctx context.Context, query DiscoveryQuery) (<-chan ServiceEvent, error)
}

// InMemoryDiscovery is an in-memory implementation of DiscoveryService
type InMemoryDiscovery struct {
	mu       sync.RWMutex
	services map[string]ServiceInfo
	watchers []watcher
}

type watcher struct {
	query   DiscoveryQuery
	eventCh chan ServiceEvent
	ctx     context.Context
}

// NewInMemoryDiscovery creates a new in-memory discovery service
func NewInMemoryDiscovery() *InMemoryDiscovery {
	return &InMemoryDiscovery{
		services: make(map[string]ServiceInfo),
		watchers: make([]watcher, 0),
	}
}

// Register registers a service in the in-memory registry
func (d *InMemoryDiscovery) Register(ctx context.Context, service ServiceInfo) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if service already exists
	_, exists := d.services[service.ID]

	// Store service
	d.services[service.ID] = service

	// Notify watchers
	eventType := ServiceEventRegistered
	if exists {
		eventType = ServiceEventUpdated
	}

	event := ServiceEvent{
		Type:    eventType,
		Service: service,
	}

	d.notifyWatchers(event)

	return nil
}

// Unregister removes a service from the in-memory registry
func (d *InMemoryDiscovery) Unregister(ctx context.Context, serviceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	service, exists := d.services[serviceID]
	if !exists {
		return nil // Idempotent operation
	}

	delete(d.services, serviceID)

	// Notify watchers
	event := ServiceEvent{
		Type:    ServiceEventUnregistered,
		Service: service,
	}

	d.notifyWatchers(event)

	return nil
}

// Discover finds services matching the query
func (d *InMemoryDiscovery) Discover(ctx context.Context, query DiscoveryQuery) ([]ServiceInfo, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]ServiceInfo, 0)

	for _, service := range d.services {
		if d.matchesQuery(service, query) {
			result = append(result, service)
		}
	}

	return result, nil
}

// Watch returns a channel that receives service events matching the query
func (d *InMemoryDiscovery) Watch(ctx context.Context, query DiscoveryQuery) (<-chan ServiceEvent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	eventCh := make(chan ServiceEvent, 100)

	w := watcher{
		query:   query,
		eventCh: eventCh,
		ctx:     ctx,
	}

	d.watchers = append(d.watchers, w)

	// Start goroutine to clean up watcher when context is done
	go func() {
		<-ctx.Done()
		d.removeWatcher(eventCh)
		close(eventCh)
	}()

	return eventCh, nil
}

// matchesQuery checks if a service matches the discovery query
func (d *InMemoryDiscovery) matchesQuery(service ServiceInfo, query DiscoveryQuery) bool {
	// Check type if specified
	if query.Type != "" && service.Type != query.Type {
		return false
	}

	// Check capabilities - service must have ALL required capabilities
	if len(query.Capabilities) > 0 {
		serviceCapMap := make(map[string]bool)
		for _, cap := range service.Capabilities {
			serviceCapMap[cap] = true
		}

		for _, requiredCap := range query.Capabilities {
			if !serviceCapMap[requiredCap] {
				return false
			}
		}
	}

	// Check tags - service metadata must match ALL required tags
	if len(query.Tags) > 0 {
		for key, value := range query.Tags {
			if service.Metadata[key] != value {
				return false
			}
		}
	}

	return true
}

// notifyWatchers sends an event to all watchers that match the service
func (d *InMemoryDiscovery) notifyWatchers(event ServiceEvent) {
	for _, w := range d.watchers {
		// Check if watcher's context is still valid
		select {
		case <-w.ctx.Done():
			continue
		default:
		}

		// Check if event matches watcher's query
		if d.matchesQuery(event.Service, w.query) {
			select {
			case w.eventCh <- event:
			case <-w.ctx.Done():
			default:
				// Don't block if channel is full
			}
		}
	}
}

// removeWatcher removes a watcher from the list
func (d *InMemoryDiscovery) removeWatcher(eventCh chan ServiceEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, w := range d.watchers {
		if w.eventCh == eventCh {
			// Remove watcher by replacing with last element and truncating
			d.watchers[i] = d.watchers[len(d.watchers)-1]
			d.watchers = d.watchers[:len(d.watchers)-1]
			break
		}
	}
}

// HealthCheckFunc is a function that checks if a service is healthy
type HealthCheckFunc func(ServiceInfo) bool

// HealthChecker periodically checks service health and unregisters unhealthy services
type HealthChecker struct {
	discovery   DiscoveryService
	interval    time.Duration
	healthCheck HealthCheckFunc
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(discovery DiscoveryService, interval time.Duration, healthCheck HealthCheckFunc) *HealthChecker {
	return &HealthChecker{
		discovery:   discovery,
		interval:    interval,
		healthCheck: healthCheck,
	}
}

// Start begins the health checking loop
func (h *HealthChecker) Start(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.checkServices(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// checkServices checks all registered services and unregisters unhealthy ones
func (h *HealthChecker) checkServices(ctx context.Context) {
	// Discover all services
	services, err := h.discovery.Discover(ctx, DiscoveryQuery{})
	if err != nil {
		return
	}

	// Check health of each service
	for _, service := range services {
		if !h.healthCheck(service) {
			// Unregister unhealthy service
			h.discovery.Unregister(ctx, service.ID)
		}
	}
}
