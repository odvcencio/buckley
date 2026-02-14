package rlm

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"strconv"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/oklog/ulid/v2"
)

// Close shuts down the runtime and waits for background workers.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		close(r.compactionCh)
	})
	r.compactionWg.Wait()
	if r.scratchpadRAG != nil {
		r.scratchpadRAG.Close()
	}
	return nil
}

// compactionWorker processes async compaction requests.
func (r *Runtime) compactionWorker() {
	defer r.compactionWg.Done()
	for range r.compactionCh {
		r.historyMu.Lock()
		r.compactHistoryLocked()
		r.historyMu.Unlock()
	}
}

// triggerAsyncCompaction triggers a non-blocking compaction.
func (r *Runtime) triggerAsyncCompaction() {
	if r == nil {
		return
	}
	select {
	case r.compactionCh <- struct{}{}:
	default:
	}
}

// countTokens returns cached token count or computes and caches it.
func (r *Runtime) countTokens(text string) int {
	if r == nil {
		return approximateTokenCount(text)
	}

	h := fnv.New64a()
	_, _ = io.WriteString(h, text)
	key := strconv.FormatUint(h.Sum64(), 36)

	r.tokenCacheMu.RLock()
	if count, ok := r.tokenCache[key]; ok {
		r.tokenCacheMu.RUnlock()
		return count
	}
	r.tokenCacheMu.RUnlock()

	count := approximateTokenCount(text)

	r.tokenCacheMu.Lock()
	if len(r.tokenCache) > 10000 {
		r.tokenCache = make(map[string]int)
	}
	r.tokenCache[key] = count
	r.tokenCacheMu.Unlock()

	return count
}

// approximateTokenCount provides a fast estimate of token count.
// Uses a simple approximation: ~4 characters per token on average.
func approximateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	count := len(text) / 4
	if count < 1 {
		return 1
	}
	return count
}

// rateLimitIteration enforces a minimum delay between iterations.
// Returns true if the iteration should proceed, false if context is cancelled.
func (r *Runtime) rateLimitIteration(ctx context.Context) bool {
	if r == nil || r.minIterationDelay <= 0 {
		return ctx.Err() == nil
	}
	if err := ctx.Err(); err != nil {
		return false
	}

	lastTime := time.Unix(0, r.lastIterationTime.Load())
	elapsed := time.Since(lastTime)
	if elapsed < r.minIterationDelay {
		select {
		case <-time.After(r.minIterationDelay - elapsed):
		case <-ctx.Done():
			return false
		}
	}

	r.lastIterationTime.Store(time.Now().UnixNano())
	return ctx.Err() == nil
}

// OnIteration registers a hook for iteration events.
func (r *Runtime) OnIteration(hook IterationHook) {
	if r == nil || hook == nil {
		return
	}
	r.hooksMu.Lock()
	r.hooks = append(r.hooks, hook)
	r.hooksMu.Unlock()
}

// CreateCheckpoint captures the current execution state for later resumption.
func (r *Runtime) CreateCheckpoint(task string, answer *Answer) (*Checkpoint, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if answer == nil {
		return nil, fmt.Errorf("answer is nil")
	}

	r.historyMu.RLock()
	history := make([]IterationHistory, len(r.history))
	copy(history, r.history)
	r.historyMu.RUnlock()

	var scratchpadKeys []string
	if r.scratchpad != nil {
		summaries, err := r.scratchpad.ListSummaries(context.Background(), 50)
		if err == nil {
			for _, s := range summaries {
				scratchpadKeys = append(scratchpadKeys, s.Key)
			}
		}
	}

	checkpoint := &Checkpoint{
		ID:         ulid.Make().String(),
		Task:       task,
		Answer:     *answer,
		History:    history,
		CreatedAt:  time.Now().UTC(),
		Scratchpad: scratchpadKeys,
	}

	return checkpoint, nil
}

// ResumeFromCheckpoint restores state from a checkpoint and continues execution.
func (r *Runtime) ResumeFromCheckpoint(ctx context.Context, checkpoint *Checkpoint) (*Answer, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("checkpoint is nil")
	}

	r.historyMu.Lock()
	r.history = make([]IterationHistory, len(checkpoint.History))
	copy(r.history, checkpoint.History)
	r.historyMu.Unlock()

	checkpoint.ResumedAt = time.Now().UTC()

	if r.telemetry != nil {
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventType("rlm.resumed"),
			SessionID: r.sessionID,
			Data: map[string]any{
				"checkpoint_id": checkpoint.ID,
				"task":          checkpoint.Task,
				"iteration":     checkpoint.Answer.Iteration,
				"tokens_used":   checkpoint.Answer.TokensUsed,
			},
		})
	}

	return r.executeFromState(ctx, checkpoint.Task, &checkpoint.Answer)
}
