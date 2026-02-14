package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/paths"
)

// GetArtifactChain returns the artifact chain for the current feature
func (w *WorkflowManager) GetArtifactChain() (*artifact.Chain, error) {
	if w.artifacts == nil {
		return nil, fmt.Errorf("artifact pipeline unavailable")
	}
	return w.artifacts.chainManager().FindChain(w.feature)
}

// UpdateArtifactLinks updates links in the artifact chain
func (w *WorkflowManager) UpdateArtifactLinks(chain *artifact.Chain) error {
	if w.artifacts == nil {
		return fmt.Errorf("artifact pipeline unavailable")
	}
	return w.artifacts.chainManager().UpdateLinks(chain)
}

// Pause exposes manual workflow pauses (CLI/API).
func (w *WorkflowManager) Pause(reason, question string) error {
	return w.pauseWorkflow(reason, question)
}

// GetResearchHighlights returns summary + top risks from the latest brief.
func (w *WorkflowManager) GetResearchHighlights() (string, []string) {
	if w == nil || w.latestResearchBrief == nil {
		return "", nil
	}
	summary := strings.TrimSpace(w.latestResearchBrief.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Research completed for %s", w.latestResearchBrief.Feature)
	}
	risks := w.latestResearchBrief.Risks
	if len(risks) > 3 {
		risks = risks[:3]
	}
	return summary, risks
}

// GetResearchBrief returns the last generated brief, if any.
func (w *WorkflowManager) GetResearchBrief() *artifact.ResearchBrief {
	return w.latestResearchBrief
}

// EnrichPlan copies workflow context (research highlights, logs) onto plan prior to saving.
func (w *WorkflowManager) EnrichPlan(plan *Plan) {
	if w == nil || plan == nil {
		return
	}

	if summary, risks := w.GetResearchHighlights(); summary != "" {
		plan.Context.ResearchSummary = summary
		plan.Context.ResearchRisks = append([]string{}, risks...)
		if brief := w.latestResearchBrief; brief != nil && !brief.Updated.IsZero() {
			plan.Context.ResearchLoggedAt = brief.Updated
		} else if plan.Context.ResearchLoggedAt.IsZero() {
			plan.Context.ResearchLoggedAt = time.Now()
		}
	}
}

func (w *WorkflowManager) steeringSettingKeys() (string, string) {
	if w.sessionID == "" {
		return "", ""
	}
	return fmt.Sprintf("session.%s.steering_notes", w.sessionID),
		fmt.Sprintf("session.%s.autonomy_level", w.sessionID)
}

func (w *WorkflowManager) loadSteeringSettings() {
	if w == nil || w.store == nil {
		return
	}
	steerKey, autoKey := w.steeringSettingKeys()
	if steerKey == "" {
		return
	}
	settings, err := w.store.GetSettings([]string{steerKey, autoKey})
	if err != nil {
		return
	}
	if val, ok := settings[steerKey]; ok {
		w.steeringNotes = strings.TrimSpace(val)
	}
	if val, ok := settings[autoKey]; ok {
		w.autonomyLevel = strings.TrimSpace(val)
	}
}

func (w *WorkflowManager) persistSteeringSettings() {
	if w == nil || w.store == nil {
		return
	}
	steerKey, autoKey := w.steeringSettingKeys()
	if steerKey == "" {
		return
	}
	_ = w.store.SetSetting(steerKey, w.steeringNotes)
	_ = w.store.SetSetting(autoKey, w.autonomyLevel)
}

func researchLogPath(feature string) string {
	identifier := SanitizeIdentifier(feature)
	if identifier == "" {
		identifier = "default"
	}
	return filepath.Join(paths.BuckleyLogsDir(identifier), "research.jsonl")
}
