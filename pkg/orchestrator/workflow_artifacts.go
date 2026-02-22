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
	w.stateMu.RLock()
	feat := w.feature
	w.stateMu.RUnlock()
	return w.artifacts.chainManager().FindChain(feat)
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
	if w == nil {
		return "", nil
	}
	w.stateMu.RLock()
	brief := w.latestResearchBrief
	w.stateMu.RUnlock()
	if brief == nil {
		return "", nil
	}
	summary := strings.TrimSpace(brief.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Research completed for %s", brief.Feature)
	}
	risks := brief.Risks
	if len(risks) > 3 {
		risks = risks[:3]
	}
	return summary, risks
}

// GetResearchBrief returns the last generated brief, if any.
func (w *WorkflowManager) GetResearchBrief() *artifact.ResearchBrief {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
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
		w.stateMu.RLock()
		brief := w.latestResearchBrief
		w.stateMu.RUnlock()
		if brief != nil && !brief.Updated.IsZero() {
			plan.Context.ResearchLoggedAt = brief.Updated
		} else if plan.Context.ResearchLoggedAt.IsZero() {
			plan.Context.ResearchLoggedAt = time.Now()
		}
	}
}

func (w *WorkflowManager) steeringSettingKeys() (string, string) {
	w.stateMu.RLock()
	sessionID := w.sessionID
	w.stateMu.RUnlock()
	if sessionID == "" {
		return "", ""
	}
	return fmt.Sprintf("session.%s.steering_notes", sessionID),
		fmt.Sprintf("session.%s.autonomy_level", sessionID)
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
	w.stateMu.Lock()
	if val, ok := settings[steerKey]; ok {
		w.steeringNotes = strings.TrimSpace(val)
	}
	if val, ok := settings[autoKey]; ok {
		w.autonomyLevel = strings.TrimSpace(val)
	}
	w.stateMu.Unlock()
}

func (w *WorkflowManager) persistSteeringSettings() {
	if w == nil || w.store == nil {
		return
	}
	steerKey, autoKey := w.steeringSettingKeys()
	if steerKey == "" {
		return
	}
	// Copy values under lock before I/O
	w.stateMu.RLock()
	steeringNotes := w.steeringNotes
	autonomyLevel := w.autonomyLevel
	w.stateMu.RUnlock()
	_ = w.store.SetSetting(steerKey, steeringNotes)
	_ = w.store.SetSetting(autoKey, autonomyLevel)
}

func researchLogPath(feature string) string {
	identifier := SanitizeIdentifier(feature)
	if identifier == "" {
		identifier = "default"
	}
	return filepath.Join(paths.BuckleyLogsDir(identifier), "research.jsonl")
}
