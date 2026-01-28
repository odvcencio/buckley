package conversation

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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

	// Tiered threshold constants
	warningThreshold  = 0.80
	compactThreshold  = 0.90
	queueBufferSize   = 3
	defaultMaxQueueWait = 5 * time.Second
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

// CompactionRequest represents a request to compact a conversation
type CompactionRequest struct {
	Ctx        context.Context
	Conv       *Conversation
	OnComplete func(*CompactionResult)
}

// Compressor defines the interface for message compression
type Compressor interface {
	Compress(data []byte) ([]byte, error)
	Decompress(data []byte) ([]byte, error)
	Name() string
}

// GzipCompressor implements Compressor using gzip
type GzipCompressor struct {
	level int
}

// NewGzipCompressor creates a new gzip compressor with the specified level
func NewGzipCompressor(level int) *GzipCompressor {
	if level < gzip.DefaultCompression || level > gzip.BestCompression {
		level = gzip.DefaultCompression
	}
	return &GzipCompressor{level: level}
}

// Compress compresses data using gzip
func (g *GzipCompressor) Compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, g.level)
	if err != nil {
		return nil, fmt.Errorf("creating gzip writer: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return nil, fmt.Errorf("writing gzip data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip writer: %w", err)
	}
	return buf.Bytes(), nil
}

// Decompress decompresses gzip data
func (g *GzipCompressor) Decompress(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer reader.Close()
	result, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading gzip data: %w", err)
	}
	return result, nil
}

// Name returns the compressor name
func (g *GzipCompressor) Name() string {
	return "gzip"
}

// tokenEstimateCache provides cached token estimation for improved performance
type tokenEstimateCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	maxSize int
}

type cacheEntry struct {
	tokens    int
	timestamp time.Time
}

// newTokenEstimateCache creates a new token estimation cache
func newTokenEstimateCache(maxSize int) *tokenEstimateCache {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &tokenEstimateCache{
		entries: make(map[string]cacheEntry),
		maxSize: maxSize,
	}
}

// Get retrieves a cached token count for the given text
func (c *tokenEstimateCache) Get(text string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[text]
	if !ok {
		return 0, false
	}
	// Entries older than 5 minutes are considered stale
	if time.Since(entry.timestamp) > 5*time.Minute {
		return 0, false
	}
	return entry.tokens, true
}

// Set stores a token count in the cache
func (c *tokenEstimateCache) Set(text string, tokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// Simple eviction: if at capacity, clear half the entries
	if len(c.entries) >= c.maxSize {
		c.evictHalf()
	}
	
	c.entries[text] = cacheEntry{
		tokens:    tokens,
		timestamp: time.Now(),
	}
}

// evictHalf removes half of the entries (oldest first)
func (c *tokenEstimateCache) evictHalf() {
	type kv struct {
		key   string
		value cacheEntry
	}
	var sorted []kv
	for k, v := range c.entries {
		sorted = append(sorted, kv{k, v})
	}
	// Sort by timestamp (oldest first)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].value.timestamp.After(sorted[j].value.timestamp) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	// Remove half
	removeCount := len(sorted) / 2
	for i := 0; i < removeCount && i < len(sorted); i++ {
		delete(c.entries, sorted[i].key)
	}
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

	// Async queue and worker
	compactionQueue chan CompactionRequest
	queueMu         sync.Mutex
	queueRunning    bool
	queueDone       chan struct{}
	queueWG         sync.WaitGroup
	queueCtx        context.Context
	queueCancel     context.CancelFunc

	// Compression support
	compressor Compressor

	// Token estimation cache
	tokenCache *tokenEstimateCache

	// Backpressure control
	backpressureMu      sync.RWMutex
	pendingCompactions  int
	maxPending          int

	// Tiered threshold callbacks
	warningThresholdFn func(float64)
}

// NewCompactionManager creates a new compaction manager
func NewCompactionManager(mgr *model.Manager, cfg *config.Config) *CompactionManager {
	timeout := 30 * time.Second
	if cfg != nil && cfg.Memory.SummaryTimeoutSecs > 0 {
		timeout = time.Duration(cfg.Memory.SummaryTimeoutSecs) * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())

	cm := &CompactionManager{
		modelManager:        mgr,
		cfg:                 cfg,
		summaryTimeout:      timeout,
		compactionQueue:     make(chan CompactionRequest, queueBufferSize),
		queueDone:           make(chan struct{}),
		queueCtx:            ctx,
		queueCancel:         cancel,
		compressor:          NewGzipCompressor(gzip.DefaultCompression),
		tokenCache:          newTokenEstimateCache(1000),
		maxPending:          queueBufferSize,
		warningThresholdFn:  nil,
	}

	// Start background worker
	cm.startQueueWorker()

	return cm
}

// startQueueWorker starts the background worker goroutine
func (cm *CompactionManager) startQueueWorker() {
	cm.queueMu.Lock()
	defer cm.queueMu.Unlock()

	if cm.queueRunning {
		return
	}

	cm.queueRunning = true
	cm.queueWG.Add(1)

	go cm.queueWorker()
}

// queueWorker processes compaction requests from the queue
func (cm *CompactionManager) queueWorker() {
	defer cm.queueWG.Done()

	for {
		select {
		case <-cm.queueCtx.Done():
			return
		case req, ok := <-cm.compactionQueue:
			if !ok {
				return
			}
			cm.processQueueRequest(req)
		}
	}
}

// processQueueRequest processes a single compaction request from the queue
func (cm *CompactionManager) processQueueRequest(req CompactionRequest) {
	// Decrement pending count
	cm.backpressureMu.Lock()
	cm.pendingCompactions--
	if cm.pendingCompactions < 0 {
		cm.pendingCompactions = 0
	}
	cm.backpressureMu.Unlock()

	// Perform compaction
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// Set conversation temporarily
	cm.compactionMu.Lock()
	oldConv := cm.conversation
	cm.conversation = req.Conv
	cm.compacting = true
	cm.compactionMu.Unlock()

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

	// Restore original conversation
	cm.compactionMu.Lock()
	cm.conversation = oldConv
	cm.compacting = false
	cm.compactionMu.Unlock()

	// Call completion handler
	if result != nil && req.OnComplete != nil {
		req.OnComplete(result)
	}
	// Also call global onComplete if set
	if result != nil && cm.onComplete != nil {
		cm.onComplete(result)
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

// SetCompressor sets a custom compressor implementation
func (cm *CompactionManager) SetCompressor(c Compressor) {
	if cm == nil {
		return
	}
	cm.compressor = c
}

// SetWarningThresholdFn sets the callback for warning threshold
func (cm *CompactionManager) SetWarningThresholdFn(fn func(float64)) {
	if cm == nil {
		return
	}
	cm.warningThresholdFn = fn
}

// Stop gracefully shuts down the compaction manager
func (cm *CompactionManager) Stop() {
	if cm == nil {
		return
	}

	cm.queueMu.Lock()
	if !cm.queueRunning {
		cm.queueMu.Unlock()
		return
	}
	cm.queueRunning = false
	cm.queueMu.Unlock()

	// Cancel context to stop worker
	if cm.queueCancel != nil {
		cm.queueCancel()
	}

	// Close queue channel
	close(cm.compactionQueue)

	// Wait for worker to finish with timeout
	done := make(chan struct{})
	go func() {
		cm.queueWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker finished cleanly
	case <-time.After(10 * time.Second):
		// Timeout waiting for worker
	}
}

// IsQueueRunning returns whether the background queue worker is running
func (cm *CompactionManager) IsQueueRunning() bool {
	if cm == nil {
		return false
	}
	cm.queueMu.Lock()
	defer cm.queueMu.Unlock()
	return cm.queueRunning
}

// QueueLength returns the current number of pending compaction requests
func (cm *CompactionManager) QueueLength() int {
	if cm == nil {
		return 0
	}
	return len(cm.compactionQueue)
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

// CheckThresholds checks if the usage ratio crosses warning or compact thresholds
// Returns: shouldCompact (bool), isWarning (bool)
func (cm *CompactionManager) CheckThresholds(usageRatio float64) (shouldCompact bool, isWarning bool) {
	if cm == nil {
		return false, false
	}

	// Check warning threshold first (80%)
	if usageRatio >= warningThreshold && usageRatio < compactThreshold {
		if cm.warningThresholdFn != nil {
			cm.warningThresholdFn(usageRatio)
		}
		return false, true
	}

	// Check compact threshold (90%)
	if usageRatio >= compactThreshold {
		return true, false
	}

	return false, false
}

// GetThresholds returns the current warning and compact thresholds
func (cm *CompactionManager) GetThresholds() (warning float64, compact float64) {
	return warningThreshold, compactThreshold
}

// TriggerAsyncCompaction submits a compaction request to the async queue
// This method does not block - returns immediately after queueing
// Returns true if the request was queued, false if queue is full (backpressure)
func (cm *CompactionManager) TriggerAsyncCompaction(ctx context.Context, conv *Conversation, onComplete func(*CompactionResult)) bool {
	if cm == nil {
		return false
	}

	// Check backpressure
	cm.backpressureMu.Lock()
	if cm.pendingCompactions >= cm.maxPending {
		cm.backpressureMu.Unlock()
		return false // Queue full, apply backpressure
	}
	cm.pendingCompactions++
	cm.backpressureMu.Unlock()

	req := CompactionRequest{
		Ctx:        ctx,
		Conv:       conv,
		OnComplete: onComplete,
	}

	// Try to queue with timeout
	select {
	case cm.compactionQueue <- req:
		return true
	case <-time.After(defaultMaxQueueWait):
		// Decrement pending count on timeout
		cm.backpressureMu.Lock()
		cm.pendingCompactions--
		if cm.pendingCompactions < 0 {
			cm.pendingCompactions = 0
		}
		cm.backpressureMu.Unlock()
		return false
	case <-ctx.Done():
		// Decrement pending count on context cancellation
		cm.backpressureMu.Lock()
		cm.pendingCompactions--
		if cm.pendingCompactions < 0 {
			cm.pendingCompactions = 0
		}
		cm.backpressureMu.Unlock()
		return false
	}
}

// CompactAsync triggers compaction in the background with fallback models.
// Note: This is the original async method that uses the queue internally
func (cm *CompactionManager) CompactAsync(ctx context.Context) {
	if cm == nil {
		return
	}

	cm.compactionMu.Lock()
	if cm.compacting {
		cm.compactionMu.Unlock()
		return
	}
	// Note: We set compacting here but the actual work is done in the queue
	// This maintains backward compatibility with existing behavior
	cm.compacting = true
	cm.compactionMu.Unlock()

	// Use the queue for processing
	go func() {
		defer func() {
			cm.compactionMu.Lock()
			cm.compacting = false
			cm.compactionMu.Unlock()
		}()

		result, err := cm.Compact(ctx)
		if err != nil {
			for _, m := range cm.fallbackModels() {
				var fallbackErr error
				result, fallbackErr = cm.compactWith(ctx, m)
				if fallbackErr == nil {
					break
				}
			}
		}
		if result == nil {
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

	// Compress large summaries before storing
	summaryContent := fmt.Sprintf("[Summary of %d previous messages]\n\n%s", len(toSummarize), summary)
	if cm.compressor != nil && len(summaryContent) > 4096 {
		compressed, err := cm.compressor.Compress([]byte(summaryContent))
		if err == nil {
			// Store compressed indicator prefix
			summaryContent = fmt.Sprintf("[COMPRESSED:%s]%s", cm.compressor.Name(), string(compressed))
		}
	}

	summaryMsg := Message{
		Role:      "system",
		Content:   summaryContent,
		Timestamp: toKeep[0].Timestamp,
		Tokens:    cm.estimateTokensCached(summary),
		IsSummary: true,
	}
	conv.Messages = append([]Message{summaryMsg}, toKeep...)
	conv.UpdateTokenCount()
	conv.CompactionCount++
}

// estimateTokensCached provides cached token estimation
func (cm *CompactionManager) estimateTokensCached(text string) int {
	if cm == nil || cm.tokenCache == nil {
		return estimateTokens(text)
	}

	// Check cache first
	if tokens, ok := cm.tokenCache.Get(text); ok {
		return tokens
	}

	// Calculate and cache
	tokens := estimateTokens(text)
	cm.tokenCache.Set(text, tokens)
	return tokens
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
