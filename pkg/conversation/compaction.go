package conversation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

const (
	defaultClassicAutoTrigger = 0.75
	defaultRLMAutoTrigger     = 0.85
	defaultCompactionRatio    = 0.45
	primaryCompactionModel    = "moonshotai/kimi-k2-thinking"
)

// CompactionResult captures the result of a compaction pass.
type CompactionResult struct {
	Conversation   *Conversation
	Summary        string
	CompactedCount int
	UsedModel      string
}

// CompactionConfig controls context compaction behavior.
type CompactionConfig struct {
	ClassicAutoTrigger float64
	RLMAutoTrigger     float64
	CompactionRatio    float64
}

// CompactionManager handles conversation compaction
type CompactionManager struct {
	modelManager   *model.Manager
	cfg            *config.Config
	summaryTimeout time.Duration
	conversation   *Conversation
	onComplete     func(*CompactionResult)
	compactionMu   sync.Mutex
	compacting     bool
}

// NewCompactionManager creates a new compaction manager
func NewCompactionManager(mgr *model.Manager, cfg *config.Config) *CompactionManager {
	timeout := 30 * time.Second
	if cfg != nil && cfg.Memory.SummaryTimeoutSecs > 0 {
		timeout = time.Duration(cfg.Memory.SummaryTimeoutSecs) * time.Second
	}
	return &CompactionManager{
		modelManager:   mgr,
		cfg:            cfg,
		summaryTimeout: timeout,
	}
}

// SetConversation assigns the conversation to compact.
func (cm *CompactionManager) SetConversation(conv *Conversation) {
	if cm == nil {
		return
	}
	cm.conversation = conv
}

// SetOnComplete registers a callback after compaction completes.
func (cm *CompactionManager) SetOnComplete(handler func(*CompactionResult)) {
	if cm == nil {
		return
	}
	cm.onComplete = handler
}

func (cm *CompactionManager) compactionConfig() CompactionConfig {
	cfg := CompactionConfig{
		ClassicAutoTrigger: defaultClassicAutoTrigger,
		RLMAutoTrigger:     defaultRLMAutoTrigger,
		CompactionRatio:    defaultCompactionRatio,
	}
	if cm == nil || cm.cfg == nil {
		return cfg
	}
	if cm.cfg.Memory.AutoCompactThreshold > 0 && cm.cfg.Memory.AutoCompactThreshold <= 1 {
		cfg.ClassicAutoTrigger = cm.cfg.Memory.AutoCompactThreshold
	}
	if cm.cfg.Compaction.RLMAutoTrigger > 0 && cm.cfg.Compaction.RLMAutoTrigger <= 1 {
		cfg.RLMAutoTrigger = cm.cfg.Compaction.RLMAutoTrigger
	}
	if cm.cfg.Compaction.CompactionRatio > 0 && cm.cfg.Compaction.CompactionRatio <= 1 {
		cfg.CompactionRatio = cm.cfg.Compaction.CompactionRatio
	}
	return cfg
}

// ShouldCompact checks if a conversation should be compacted
func (cm *CompactionManager) ShouldCompact(conv *Conversation, maxTokens int) bool {
	if conv == nil || maxTokens <= 0 {
		return false
	}
	thresholdRatio := cm.compactionConfig().ClassicAutoTrigger
	threshold := float64(maxTokens) * thresholdRatio
	return float64(conv.TokenCount) >= threshold
}

// ShouldAutoCompact checks if compaction should run based on usage ratio and mode.
func (cm *CompactionManager) ShouldAutoCompact(mode string, usageRatio float64) bool {
	if cm == nil || usageRatio <= 0 {
		return false
	}
	cfg := cm.compactionConfig()
	threshold := cfg.ClassicAutoTrigger
	if strings.EqualFold(mode, "rlm") {
		threshold = cfg.RLMAutoTrigger
	}
	return usageRatio >= threshold
}

// CompactAsync triggers compaction in the background with fallback models.
func (cm *CompactionManager) CompactAsync(ctx context.Context) {
	if cm == nil {
		return
	}

	cm.compactionMu.Lock()
	if cm.compacting {
		cm.compactionMu.Unlock()
		return
	}
	cm.compacting = true
	cm.compactionMu.Unlock()

	go func() {
		defer func() {
			cm.compactionMu.Lock()
			cm.compacting = false
			cm.compactionMu.Unlock()
		}()

		result, err := cm.Compact(ctx)
		if err != nil {
			for _, model := range cm.fallbackModels() {
				result, err = cm.compactWith(ctx, model)
				if err == nil {
					break
				}
			}
		}
		if err != nil {
			result = cm.compactFallback()
		}
		if result != nil && cm.onComplete != nil {
			cm.onComplete(result)
		}
	}()
}

// Compact compacts a conversation by summarizing old messages.
func (cm *CompactionManager) Compact(ctx context.Context) (*CompactionResult, error) {
	return cm.compactWith(ctx, primaryCompactionModel)
}

func (cm *CompactionManager) compactWith(ctx context.Context, modelID string) (*CompactionResult, error) {
	if cm == nil {
		return nil, fmt.Errorf("compaction manager unavailable")
	}
	conv := cm.conversation
	if conv == nil {
		return nil, fmt.Errorf("conversation required")
	}
	if len(conv.Messages) < 4 {
		return nil, fmt.Errorf("not enough messages to compact (need at least 4)")
	}

	ratio := cm.compactionConfig().CompactionRatio
	toSummarize, toKeep, err := selectCompactionSegments(conv.Messages, ratio)
	if err != nil {
		return nil, err
	}

	summary, err := cm.generateSummaryWithRetry(ctx, modelID, toSummarize)
	if err != nil {
		return nil, err
	}

	cm.applySummary(conv, summary, toSummarize, toKeep)
	return &CompactionResult{
		Conversation:   conv,
		Summary:        summary,
		CompactedCount: len(toSummarize),
		UsedModel:      modelID,
	}, nil
}

func (cm *CompactionManager) compactFallback() *CompactionResult {
	conv := cm.conversation
	if conv == nil || len(conv.Messages) < 4 {
		return nil
	}
	ratio := cm.compactionConfig().CompactionRatio
	toSummarize, toKeep, err := selectCompactionSegments(conv.Messages, ratio)
	if err != nil {
		return nil
	}
	summary := "[Earlier context summarized]"
	cm.applySummary(conv, summary, toSummarize, toKeep)
	return &CompactionResult{
		Conversation:   conv,
		Summary:        summary,
		CompactedCount: len(toSummarize),
		UsedModel:      "",
	}
}

func (cm *CompactionManager) applySummary(conv *Conversation, summary string, toSummarize []Message, toKeep []Message) {
	if conv == nil {
		return
	}
	summaryMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("[Summary of %d previous messages]\n\n%s", len(toSummarize), summary),
		Timestamp: toKeep[0].Timestamp,
		Tokens:    estimateTokens(summary),
		IsSummary: true,
	}
	conv.Messages = append([]Message{summaryMsg}, toKeep...)
	conv.UpdateTokenCount()
	conv.CompactionCount++
}

func (cm *CompactionManager) fallbackModels() []string {
	if cm == nil || cm.cfg == nil {
		return nil
	}
	models := append([]string{}, cm.cfg.Compaction.Models...)
	if cm.cfg.Models.Utility.Compaction != "" {
		models = append(models, cm.cfg.Models.Utility.Compaction)
	}
	return uniqueModels(models, primaryCompactionModel)
}

func uniqueModels(models []string, exclude string) []string {
	seen := map[string]struct{}{}
	if exclude != "" {
		seen[exclude] = struct{}{}
	}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func (cm *CompactionManager) generateSummaryWithRetry(ctx context.Context, modelID string, messages []Message) (string, error) {
	if cm == nil || cm.modelManager == nil {
		return "", fmt.Errorf("model manager unavailable")
	}
	if modelID == "" {
		return "", fmt.Errorf("model id required")
	}

	maxRetries := 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		summary, err := cm.generateSummary(ctx, modelID, messages)
		if err == nil {
			return summary, nil
		}
		lastErr = err
		if attempt < maxRetries-1 {
			backoffDuration := time.Duration(1<<uint(attempt)) * time.Second
			time.Sleep(backoffDuration)
		}
	}
	return "", lastErr
}

// selectCompactionSegments splits messages into segments to summarize and to retain,
// always retaining system messages/persona/steering content.
func selectCompactionSegments(messages []Message, ratio float64) ([]Message, []Message, error) {
	if len(messages) < 4 {
		return nil, nil, fmt.Errorf("not enough messages to compact (need at least 4)")
	}
	if ratio <= 0 || ratio > 1 {
		ratio = defaultCompactionRatio
	}

	var protected []Message
	var candidate []Message
	for _, msg := range messages {
		if msg.Role == "system" {
			protected = append(protected, msg)
			continue
		}
		candidate = append(candidate, msg)
	}

	if len(candidate) < 2 {
		return nil, nil, fmt.Errorf("not enough non-system messages to summarize")
	}

	cutoff := int(float64(len(candidate)) * ratio)
	if cutoff < 2 {
		cutoff = 2 // Summarize at least 2 messages
	}

	toSummarize := candidate[:cutoff]
	toKeep := append(protected, candidate[cutoff:]...)
	return toSummarize, toKeep, nil
}

// generateSummary generates a summary of messages using the LLM.
func (cm *CompactionManager) generateSummary(ctx context.Context, modelID string, messages []Message) (string, error) {
	if cm == nil || cm.modelManager == nil {
		return "", fmt.Errorf("model manager unavailable")
	}
	// Format messages for summarization
	content := formatMessagesForSummary(messages)

	// Create summarization request using a cheap, fast model
	req := model.ChatRequest{
		Model: modelID,
		Messages: []model.Message{
			{
				Role:    "system",
				Content: "You are a conversation summarizer. Create a concise summary of this conversation segment, preserving key decisions, code snippets, technical details, and context. Be specific and factual.",
			},
			{
				Role:    "user",
				Content: content,
			},
		},
		Temperature: 0.3, // Lower temperature for more consistent summaries
	}

	// Get summary from model
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, cm.summaryTimeout)
	defer cancel()
	resp, err := cm.modelManager.ChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from model")
	}

	return model.ExtractTextContent(resp.Choices[0].Message.Content)
}

// formatMessagesForSummary formats messages into a string for summarization
func formatMessagesForSummary(messages []Message) string {
	var b strings.Builder

	b.WriteString("Summarize this conversation segment:\n\n")

	for i, msg := range messages {
		b.WriteString(fmt.Sprintf("[Message %d - %s]\n", i+1, msg.Role))
		b.WriteString(GetContentAsString(msg.Content))
		b.WriteString("\n\n")
	}

	b.WriteString("Provide a concise summary that preserves important information, decisions, and context.")

	return b.String()
}

// EstimateTokensSaved calculates how many tokens would be saved by compaction
func (cm *CompactionManager) EstimateTokensSaved(conv *Conversation) int {
	if len(conv.Messages) < 4 {
		return 0
	}

	ratio := cm.compactionConfig().CompactionRatio
	cutoff := int(float64(len(conv.Messages)) * ratio)
	if cutoff < 2 {
		cutoff = 2
	}

	// Calculate tokens in messages that would be summarized
	tokensBefore := 0
	for i := 0; i < cutoff; i++ {
		tokensBefore += conv.Messages[i].Tokens
	}

	// Estimate summary would be about 30% of original
	estimatedSummaryTokens := int(float64(tokensBefore) * 0.3)

	return tokensBefore - estimatedSummaryTokens
}
