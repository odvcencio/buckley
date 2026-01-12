package rlm

import "strings"

// ExecutionModelProvider returns the default execution model.
type ExecutionModelProvider interface {
	GetExecutionModel() string
}

// ModelSelector provides sub-agent model selection.
// Simplified: no tiers, just a single configured model or fallback to execution model.
type ModelSelector struct {
	model            string // Configured sub-agent model
	executionModel   ExecutionModelProvider
}

// NewModelSelector creates a selector from configuration.
func NewModelSelector(cfg Config, execModelProvider ExecutionModelProvider) *ModelSelector {
	cfg.Normalize()
	return &ModelSelector{
		model:          strings.TrimSpace(cfg.SubAgent.Model),
		executionModel: execModelProvider,
	}
}

// Select returns the model to use for sub-agents.
// If no model is configured, falls back to the execution model.
func (s *ModelSelector) Select() string {
	if s == nil {
		return ""
	}
	if s.model != "" {
		return s.model
	}
	if s.executionModel != nil {
		return s.executionModel.GetExecutionModel()
	}
	return ""
}

// SetModel overrides the sub-agent model.
func (s *ModelSelector) SetModel(modelID string) {
	if s == nil {
		return
	}
	s.model = strings.TrimSpace(modelID)
}
