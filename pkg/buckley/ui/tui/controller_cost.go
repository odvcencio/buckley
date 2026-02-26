package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/cost"
	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/fluffyui/toast"
)

func (c *Controller) recordCost(sessionID, modelID string, usage *model.Usage) {
	if c == nil || usage == nil {
		return
	}
	tracker := c.ensureCostTracker(sessionID)
	if tracker == nil {
		return
	}
	if _, err := tracker.RecordAPICall(modelID, usage.PromptTokens, usage.CompletionTokens); err != nil {
		return
	}
	if c.budgetAlerts != nil {
		c.budgetAlerts.Check(tracker.CheckBudget())
	}
}

func (c *Controller) ensureCostTracker(sessionID string) *cost.Tracker {
	if c == nil || c.store == nil || c.modelMgr == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	c.mu.Lock()
	if c.costTrackers == nil {
		c.costTrackers = map[string]*cost.Tracker{}
	}
	if tracker, ok := c.costTrackers[sessionID]; ok {
		c.mu.Unlock()
		return tracker
	}
	c.mu.Unlock()

	tracker, err := cost.New(sessionID, c.store, c.modelMgr)
	if err != nil {
		return nil
	}
	if c.cfg != nil {
		tracker.SetBudgets(
			c.cfg.CostManagement.SessionBudget,
			c.cfg.CostManagement.DailyBudget,
			c.cfg.CostManagement.MonthlyBudget,
			c.cfg.CostManagement.AutoStopAt,
		)
	}

	c.mu.Lock()
	if c.costTrackers == nil {
		c.costTrackers = map[string]*cost.Tracker{}
	}
	c.costTrackers[sessionID] = tracker
	c.mu.Unlock()
	return tracker
}

func formatBudgetToast(alert cost.BudgetAlert) (toast.ToastLevel, string, string) {
	level := toast.ToastInfo
	title := "Budget alert"
	switch alert.Level {
	case cost.BudgetAlertWarning:
		level = toast.ToastWarning
		title = "Budget warning"
	case cost.BudgetAlertCritical:
		level = toast.ToastError
		title = "Budget critical"
	case cost.BudgetAlertExceeded:
		level = toast.ToastError
		title = "Budget exceeded"
	}

	label := "Session"
	costValue := alert.Status.SessionCost
	budgetValue := alert.Status.SessionBudget
	switch strings.ToLower(alert.BudgetType) {
	case cost.BudgetTypeDaily:
		label = "Daily"
		costValue = alert.Status.DailyCost
		budgetValue = alert.Status.DailyBudget
	case cost.BudgetTypeMonthly:
		label = "Monthly"
		costValue = alert.Status.MonthlyCost
		budgetValue = alert.Status.MonthlyBudget
	}

	message := fmt.Sprintf("%s budget %.0f%% ($%.2f / $%.2f)", label, alert.Percent, costValue, budgetValue)
	return level, title, message
}

func (c *Controller) newEmbeddingProvider() embeddings.EmbeddingProvider {
	if c == nil || c.cfg == nil {
		return nil
	}
	cacheDir := ""
	if strings.TrimSpace(c.workDir) != "" {
		cacheDir = filepath.Join(c.workDir, ".buckley", "embeddings")
	}

	type providerCandidate struct {
		id       string
		settings config.ProviderSettings
		kind     embeddings.ProviderKind
	}
	candidates := []providerCandidate{
		{id: "openrouter", settings: c.cfg.Providers.OpenRouter, kind: embeddings.ProviderOpenRouter},
		{id: "openai", settings: c.cfg.Providers.OpenAI, kind: embeddings.ProviderOpenAI},
	}

	pick := func(candidate providerCandidate) embeddings.EmbeddingProvider {
		if !candidate.settings.Enabled || strings.TrimSpace(candidate.settings.APIKey) == "" {
			return nil
		}
		return embeddings.NewService(embeddings.ServiceOptions{
			APIKey:   candidate.settings.APIKey,
			Provider: candidate.kind,
			BaseURL:  embeddingsBaseURL(candidate.settings.BaseURL),
			CacheDir: cacheDir,
		})
	}

	preferred := strings.ToLower(strings.TrimSpace(c.cfg.Models.DefaultProvider))
	for _, candidate := range candidates {
		if candidate.id == preferred {
			if provider := pick(candidate); provider != nil {
				return provider
			}
			break
		}
	}
	for _, candidate := range candidates {
		if provider := pick(candidate); provider != nil {
			return provider
		}
	}
	return nil
}

func parseExportFormat(raw string) (conversation.ExportFormat, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "json":
		return conversation.ExportJSON, true
	case "markdown", "md":
		return conversation.ExportMarkdown, true
	case "html", "htm":
		return conversation.ExportHTML, true
	default:
		return "", false
	}
}

func exportExtension(format conversation.ExportFormat) string {
	switch format {
	case conversation.ExportJSON:
		return ".json"
	case conversation.ExportHTML:
		return ".html"
	default:
		return ".md"
	}
}

func embeddingsBaseURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	return strings.TrimRight(base, "/") + "/embeddings"
}
