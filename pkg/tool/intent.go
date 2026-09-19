package tool

import (
	"time"
)

// Intent represents a statement of what the agent is about to do
type Intent struct {
	Phase        string    // "planning", "execution", "review"
	Activity     string    // High-level activity description
	Tools        []string  // Tools that will be used
	ExpectedTime string    // Estimated time (e.g., "~30 seconds")
	Timestamp    time.Time // When intent was declared
}

// IntentHistory tracks intent statements for a session
type IntentHistory struct {
	intents []Intent
}

// NewIntentHistory creates a new intent history
func NewIntentHistory() *IntentHistory {
	return &IntentHistory{
		intents: []Intent{},
	}
}

// Add adds an intent to the history
func (h *IntentHistory) Add(intent Intent) {
	h.intents = append(h.intents, intent)
}

// GetAll returns all intents
func (h *IntentHistory) GetAll() []Intent {
	return h.intents
}

// GetByPhase returns intents for a specific phase
func (h *IntentHistory) GetByPhase(phase string) []Intent {
	var filtered []Intent
	for _, intent := range h.intents {
		if intent.Phase == phase {
			filtered = append(filtered, intent)
		}
	}
	return filtered
}

// GetRecent returns the N most recent intents
func (h *IntentHistory) GetRecent(n int) []Intent {
	if n >= len(h.intents) {
		return h.intents
	}
	return h.intents[len(h.intents)-n:]
}

// GetLatest returns the most recent intent
func (h *IntentHistory) GetLatest() *Intent {
	if len(h.intents) == 0 {
		return nil
	}
	return &h.intents[len(h.intents)-1]
}

// Clear clears the intent history
func (h *IntentHistory) Clear() {
	h.intents = []Intent{}
}

// Count returns the number of intents
func (h *IntentHistory) Count() int {
	return len(h.intents)
}
