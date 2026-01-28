// pkg/ralph/backend_registry.go
package ralph

import (
	"sync"
)

// BackendRegistry manages registered backends.
type BackendRegistry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NewBackendRegistry creates a new backend registry.
func NewBackendRegistry() *BackendRegistry {
	return &BackendRegistry{
		backends: make(map[string]Backend),
	}
}

// Register adds a backend to the registry.
// If a backend with the same name exists, it is replaced.
func (r *BackendRegistry) Register(b Backend) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.Name()] = b
}

// Get returns a backend by name.
func (r *BackendRegistry) Get(name string) (Backend, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[name]
	return b, ok
}

// List returns all registered backends.
func (r *BackendRegistry) List() []Backend {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		result = append(result, b)
	}
	return result
}

// Available returns only backends where Available() returns true.
func (r *BackendRegistry) Available() []Backend {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		if b.Available() {
			result = append(result, b)
		}
	}
	return result
}
