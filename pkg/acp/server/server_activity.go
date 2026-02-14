package server

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func (s *Server) recordActivity(agentID, sessionID, action, details, status string) {
	if s.store == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	mStore := mission.NewStore(s.store.DB())
	_ = mStore.RecordAgentActivity(&mission.AgentActivity{
		AgentID:   agentID,
		SessionID: sessionID,
		AgentType: "editor",
		Action:    action,
		Details:   details,
		Status:    status,
		Timestamp: time.Now(),
	})
}

// publishUsage emits telemetry for token/cost usage when available.
func (s *Server) publishUsage(modelID string, usage *model.Usage, sessionID, planID string) {
	if s.telemetryHub == nil || usage == nil {
		return
	}

	data := map[string]any{
		"model":             modelID,
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}

	if price, err := s.models.GetPricing(modelID); err == nil && price != nil {
		promptCost := price.Prompt * float64(usage.PromptTokens) / 1_000_000.0
		completionCost := price.Completion * float64(usage.CompletionTokens) / 1_000_000.0
		data["cost"] = promptCost + completionCost
	}

	s.telemetryHub.Publish(telemetry.Event{
		Type:      telemetry.EventTokenUsageUpdated,
		SessionID: sessionID,
		PlanID:    planID,
		Data:      data,
	})
}

func (s *Server) publishTelemetry(eventType telemetry.EventType, sessionID, planID string, data map[string]any) {
	if s.telemetryHub == nil {
		return
	}
	s.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: sessionID,
		PlanID:    planID,
		Data:      data,
	})
}
