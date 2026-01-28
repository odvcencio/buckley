// Package diagnostics collects backend telemetry for debugging.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// MaxEvents is the default maximum number of events to retain.
const MaxEvents = 200

// Collector aggregates telemetry events for diagnostic dumps.
type Collector struct {
	mu        sync.RWMutex
	events    []telemetry.Event
	maxEvents int

	// Aggregated stats
	apiCalls      int
	apiErrors     int
	totalTokens   int
	totalLatency  time.Duration
	modelCalls    map[string]int
	toolCalls     map[string]int
	circuitStates map[string]string
	recentErrors  []errorEntry

	// Subscription
	unsubscribe func()
	started     time.Time
}

type errorEntry struct {
	Time    time.Time
	Type    string
	Message string
}

// NewCollector creates a new diagnostic collector.
func NewCollector() *Collector {
	return &Collector{
		events:        make([]telemetry.Event, 0, MaxEvents),
		maxEvents:     MaxEvents,
		modelCalls:    make(map[string]int),
		toolCalls:     make(map[string]int),
		circuitStates: make(map[string]string),
		recentErrors:  make([]errorEntry, 0, 50),
		started:       time.Now(),
	}
}

// Subscribe starts collecting events from a telemetry hub.
func (c *Collector) Subscribe(hub *telemetry.Hub) {
	if hub == nil {
		return
	}
	ch, unsub := hub.Subscribe()
	c.unsubscribe = unsub

	go func() {
		for event := range ch {
			c.record(event)
		}
	}()
}

// Close stops collecting events.
func (c *Collector) Close() {
	if c.unsubscribe != nil {
		c.unsubscribe()
	}
}

func (c *Collector) record(event telemetry.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store event in ring buffer
	if len(c.events) >= c.maxEvents {
		c.events = c.events[1:]
	}
	c.events = append(c.events, event)

	// Update aggregated stats based on event type
	switch event.Type {
	case telemetry.EventModelStreamStarted:
		c.apiCalls++
		if model, ok := event.Data["model"].(string); ok {
			c.modelCalls[model]++
		}

	case telemetry.EventModelStreamEnded:
		if tokens, ok := event.Data["tokens"].(int); ok {
			c.totalTokens += tokens
		}
		if latency, ok := event.Data["latency"].(time.Duration); ok {
			c.totalLatency += latency
		}

	case telemetry.EventToolStarted:
		if name, ok := event.Data["tool"].(string); ok {
			c.toolCalls[name]++
		}

	case telemetry.EventToolFailed, telemetry.EventTaskFailed, telemetry.EventBuilderFailed:
		c.apiErrors++
		msg := ""
		if errStr, ok := event.Data["error"].(string); ok {
			msg = errStr
		}
		c.addError(string(event.Type), msg)

	case telemetry.EventCircuitStateChange:
		if name, ok := event.Data["name"].(string); ok {
			if state, ok := event.Data["state"].(string); ok {
				c.circuitStates[name] = state
			}
		}

	case telemetry.EventCircuitFailure:
		if name, ok := event.Data["name"].(string); ok {
			c.circuitStates[name] = "failing"
		}
		if errStr, ok := event.Data["error"].(string); ok {
			c.addError("circuit_failure", errStr)
		}
	}
}

func (c *Collector) addError(errType, msg string) {
	if len(c.recentErrors) >= 50 {
		c.recentErrors = c.recentErrors[1:]
	}
	c.recentErrors = append(c.recentErrors, errorEntry{
		Time:    time.Now(),
		Type:    errType,
		Message: msg,
	})
}

// Dump returns a formatted diagnostic report.
func (c *Collector) Dump() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("=== Backend Diagnostics ===\n")
	sb.WriteString(fmt.Sprintf("Collection Started: %s\n", c.started.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Collection Duration: %s\n", time.Since(c.started).Round(time.Second)))
	sb.WriteString("\n")

	// API Stats
	sb.WriteString("=== API Statistics ===\n")
	sb.WriteString(fmt.Sprintf("Total API Calls: %d\n", c.apiCalls))
	sb.WriteString(fmt.Sprintf("API Errors: %d\n", c.apiErrors))
	sb.WriteString(fmt.Sprintf("Total Tokens: %d\n", c.totalTokens))
	if c.apiCalls > 0 {
		avgLatency := c.totalLatency / time.Duration(c.apiCalls)
		sb.WriteString(fmt.Sprintf("Avg Latency: %s\n", avgLatency.Round(time.Millisecond)))
	}
	sb.WriteString("\n")

	// Model usage
	if len(c.modelCalls) > 0 {
		sb.WriteString("=== Model Usage ===\n")
		for model, count := range c.modelCalls {
			sb.WriteString(fmt.Sprintf("  %s: %d calls\n", model, count))
		}
		sb.WriteString("\n")
	}

	// Tool usage
	if len(c.toolCalls) > 0 {
		sb.WriteString("=== Tool Usage ===\n")
		for tool, count := range c.toolCalls {
			sb.WriteString(fmt.Sprintf("  %s: %d calls\n", tool, count))
		}
		sb.WriteString("\n")
	}

	// Circuit breakers
	if len(c.circuitStates) > 0 {
		sb.WriteString("=== Circuit Breakers ===\n")
		for name, state := range c.circuitStates {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", name, state))
		}
		sb.WriteString("\n")
	}

	// Recent errors
	if len(c.recentErrors) > 0 {
		sb.WriteString("=== Recent Errors ===\n")
		for _, err := range c.recentErrors {
			sb.WriteString(fmt.Sprintf("  [%s] %s: %s\n",
				err.Time.Format("15:04:05"), err.Type, err.Message))
		}
		sb.WriteString("\n")
	}

	// Recent events (last 20)
	sb.WriteString("=== Recent Events (last 20) ===\n")
	start := len(c.events) - 20
	if start < 0 {
		start = 0
	}
	for _, event := range c.events[start:] {
		data := ""
		if len(event.Data) > 0 {
			if b, err := json.Marshal(event.Data); err == nil {
				data = string(b)
				if len(data) > 80 {
					data = data[:77] + "..."
				}
			}
		}
		sb.WriteString(fmt.Sprintf("  [%s] %s %s\n",
			event.Timestamp.Format("15:04:05"), event.Type, data))
	}
	sb.WriteString("\n")

	sb.WriteString("=== End Backend Diagnostics ===\n")
	return sb.String()
}

// Stats returns a summary of collected statistics.
func (c *Collector) Stats() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]any{
		"uptime":         time.Since(c.started).String(),
		"api_calls":      c.apiCalls,
		"api_errors":     c.apiErrors,
		"total_tokens":   c.totalTokens,
		"event_count":    len(c.events),
		"model_calls":    c.modelCalls,
		"tool_calls":     c.toolCalls,
		"circuit_states": c.circuitStates,
		"error_count":    len(c.recentErrors),
	}
}
