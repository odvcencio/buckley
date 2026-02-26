package model

import "sync"

// RoutingDecision captures model routing context.
type RoutingDecision struct {
	RequestedModel string
	SelectedModel  string
	Reason         string
	TaskWeight     string
	Context        map[string]any
}

// RoutingHook can adjust a routing decision.
type RoutingHook func(decision *RoutingDecision) *RoutingDecision

// RoutingHooks manages registered routing hooks.
type RoutingHooks struct {
	mu    sync.RWMutex
	hooks []RoutingHook
}

// NewRoutingHooks creates a routing hook registry.
func NewRoutingHooks() *RoutingHooks {
	return &RoutingHooks{}
}

// Register adds a routing hook.
func (rh *RoutingHooks) Register(hook RoutingHook) {
	if rh == nil || hook == nil {
		return
	}
	rh.mu.Lock()
	rh.hooks = append(rh.hooks, hook)
	rh.mu.Unlock()
}

// Apply runs hooks in order and returns the final decision.
func (rh *RoutingHooks) Apply(decision *RoutingDecision) *RoutingDecision {
	if rh == nil || decision == nil {
		return decision
	}
	rh.mu.RLock()
	hooks := append([]RoutingHook{}, rh.hooks...)
	rh.mu.RUnlock()

	out := decision
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if next := hook(out); next != nil {
			out = next
		}
	}
	return out
}
