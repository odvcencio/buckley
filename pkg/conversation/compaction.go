package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

// CompactionManager handles conversation compaction
type CompactionManager struct {
	modelManager   *model.Manager
	cfg            *config.Config
	summaryTimeout time.Duration
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

// ShouldCompact checks if a conversation should be compacted
func (cm *CompactionManager) ShouldCompact(conv *Conversation, maxTokens int) bool {
	thresholdRatio := 0.9
	if cm.cfg != nil && cm.cfg.Memory.AutoCompactThreshold > 0 && cm.cfg.Memory.AutoCompactThreshold <= 1 {
		thresholdRatio = cm.cfg.Memory.AutoCompactThreshold
	}
	threshold := float64(maxTokens) * thresholdRatio
	return float64(conv.TokenCount) >= threshold
}

// Compact compacts a conversation by summarizing old messages
func (cm *CompactionManager) Compact(conv *Conversation) error {
	if len(conv.Messages) < 4 {
		return fmt.Errorf("not enough messages to compact (need at least 4)")
	}

	toSummarize, toKeep, err := selectCompactionSegments(conv.Messages)
	if err != nil {
		return err
	}

	// 2. Generate summary with retry logic
	var summary string
	var lastErr error
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		var err error
		summary, err = cm.generateSummary(toSummarize)
		if err == nil {
			break
		}
		lastErr = err

		// Exponential backoff: 1s, 2s, 4s
		if attempt < maxRetries-1 {
			backoffDuration := time.Duration(1<<uint(attempt)) * time.Second
			time.Sleep(backoffDuration)
		}
	}

	// If all retries failed, fall back to simple truncation
	if lastErr != nil && summary == "" {
		// Create a simple placeholder summary instead of failing
		summary = fmt.Sprintf("[%d messages truncated due to context limits. Summary unavailable - retry failed after %d attempts]",
			len(toSummarize), maxRetries)
	}

	// 3. Replace old messages with summary message
	summaryMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("[Summary of %d previous messages]\n\n%s", len(toSummarize), summary),
		Timestamp: toKeep[0].Timestamp, // Use timestamp of first kept message
		Tokens:    estimateTokens(summary),
		IsSummary: true,
	}

	conv.Messages = append([]Message{summaryMsg}, toKeep...)

	// 4. Recalculate token count
	conv.UpdateTokenCount()
	conv.CompactionCount++

	return nil
}

// selectCompactionSegments splits messages into segments to summarize and to retain,
// always retaining system messages/persona/steering content.
func selectCompactionSegments(messages []Message) ([]Message, []Message, error) {
	if len(messages) < 4 {
		return nil, nil, fmt.Errorf("not enough messages to compact (need at least 4)")
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

	cutoff := int(float64(len(candidate)) * 0.4)
	if cutoff < 2 {
		cutoff = 2 // Summarize at least 2 messages
	}

	toSummarize := candidate[:cutoff]
	toKeep := append(protected, candidate[cutoff:]...)
	return toSummarize, toKeep, nil
}

// generateSummary generates a summary of messages using the LLM
func (cm *CompactionManager) generateSummary(messages []Message) (string, error) {
	// Format messages for summarization
	content := formatMessagesForSummary(messages)

	// Create summarization request using a cheap, fast model
	req := model.ChatRequest{
		Model: cm.getUtilityModel(),
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
	ctx, cancel := context.WithTimeout(context.Background(), cm.summaryTimeout)
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

	cutoff := int(float64(len(conv.Messages)) * 0.4)
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

// getUtilityModel returns the configured utility model for compaction
func (cm *CompactionManager) getUtilityModel() string {
	if cm.cfg != nil {
		return cm.cfg.GetUtilityCompactionModel()
	}
	return config.DefaultUtilityModel
}
