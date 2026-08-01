package tui

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/v2/pkg/conversation"
)

// maxRecentModels caps the recent-models cycle list.
const maxRecentModels = 3

// cycleModelVariant advances to the next built-in reasoning preset (see
// conversation.DefaultModelVariants), applying its effort tier and
// provider-continuation flag to the active config and updating the header.
func (c *Controller) cycleModelVariant() {
	c.mu.Lock()
	variant := conversation.NextModelVariant(conversation.DefaultModelVariants, c.modelVariant)
	if c.cfg != nil {
		c.cfg.Models.Reasoning = variant.ReasoningEffort
		c.cfg.Models.ProviderContinuation = variant.ProviderContinuation
	}
	c.modelVariant = variant.Name
	c.mu.Unlock()

	c.app.SetModelVariant(variant.Name)
	c.app.AddMessage(fmt.Sprintf("Model variant: %s (reasoning=%s, provider_continuation=%v)", variant.Name, variant.ReasoningEffort, variant.ProviderContinuation), "system")
}

// cycleRecentModel switches the execution model to the next entry in the
// recent-models list (the last three distinct models used this session,
// most recent first).
func (c *Controller) cycleRecentModel() {
	c.mu.Lock()
	recents := append([]string(nil), c.recentModels...)
	current := c.modelOverride
	c.mu.Unlock()

	if len(recents) == 0 {
		c.app.AddMessage("No recent models yet this session. Use /model to pick one.", "system")
		return
	}

	c.setExecutionModel(nextRecentModel(recents, current))
}

// nextRecentModel returns the entry in recents that follows current,
// wrapping to the first entry. If current is not in recents, it returns
// the most recent entry.
func nextRecentModel(recents []string, current string) string {
	for i, id := range recents {
		if id == current {
			return recents[(i+1)%len(recents)]
		}
	}
	return recents[0]
}

// rememberRecentModel records modelID as the most-recently-used model for
// this session, most recent first, capped at maxRecentModels. Callers must
// already hold c.mu.
func (c *Controller) rememberRecentModel(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	filtered := make([]string, 0, len(c.recentModels)+1)
	filtered = append(filtered, modelID)
	for _, id := range c.recentModels {
		if id != modelID {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) > maxRecentModels {
		filtered = filtered[:maxRecentModels]
	}
	c.recentModels = filtered
}
