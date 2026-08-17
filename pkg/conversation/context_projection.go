package conversation

import (
	"math"

	"m31labs.dev/buckley/pkg/model"
)

// ContextProjectionStats describes how durable conversation history was
// projected into one provider request. Projection never mutates stored history.
type ContextProjectionStats struct {
	ContextWindow   int
	BudgetTokens    int
	OriginalTokens  int
	ProjectedTokens int
	OriginalBytes   int
	ProjectedBytes  int
	MessagesBefore  int
	MessagesAfter   int
	Compacted       bool
	Emergency       bool
	Scale           float64

	// ToolResultsCompacted and ToolBytesCompacted describe selective dynamic
	// pruning of older completed tool results. The durable transcript is never
	// changed; these counts describe only this provider-facing projection.
	ToolResultsCompacted  int
	ToolBytesCompacted    int
	ReasoningBytesRemoved int
	HistoryCollapsed      bool

	// ContinuationActive is true when this projection pinned the prefix of
	// history an active provider continuation window represents (decision
	// 0001), suspending reasoning stripping and pruning for that prefix.
	ContinuationActive bool
	// ContinuationHit reports whether the provider continuation cursor's
	// represented prefix was still valid for this request (an incremental
	// request was sent) rather than reset (a full recompiled request). It is
	// only meaningful when ContinuationActive is true or a continuation
	// attempt was made; callers outside the continuation path leave it false.
	ContinuationHit bool
}

// ProjectModelMessagesForRequest preserves full history while it fits within
// the model's advertised context, then progressively compacts older evidence as
// pressure rises. scale is normally 1; callers may lower it for an emergency
// retry after a provider reports an unexpectedly smaller effective window.
func ProjectModelMessagesForRequest(messages []model.Message, req model.ChatRequest, contextWindow int, scale float64) ([]model.Message, ContextProjectionStats) {
	return ProjectModelMessagesForRequestPinned(messages, req, contextWindow, scale, 0)
}

// ProjectModelMessagesForRequestPinned behaves like ProjectModelMessagesForRequest,
// except every message before pinnedFromIndex is treated as the prefix an
// active provider continuation window already represents (decision 0001):
// the provider owns that prefix and the cursor fingerprints it, so
// projection and compaction leave it byte-identical and shape only the
// suffix. A pinnedFromIndex <= 0 disables the pin.
func ProjectModelMessagesForRequestPinned(messages []model.Message, req model.ChatRequest, contextWindow int, scale float64, pinnedFromIndex int) ([]model.Message, ContextProjectionStats) {
	scale = normalizedProjectionScale(scale)
	fullReq := req
	fullReq.Messages = messages
	originalEstimate := model.EstimateRequestTokens(fullReq)
	stats := ContextProjectionStats{
		ContextWindow:      contextWindow,
		OriginalTokens:     originalEstimate.Total,
		ProjectedTokens:    originalEstimate.Total,
		OriginalBytes:      modelMessagesBytes(messages),
		ProjectedBytes:     modelMessagesBytes(messages),
		MessagesBefore:     len(messages),
		MessagesAfter:      len(messages),
		Emergency:          scale < 0.999,
		Scale:              scale,
		ContinuationActive: pinnedFromIndex > 0,
	}

	if contextWindow <= 0 {
		opts := DefaultEfficientContextOptions()
		opts.MaxBytes = maxProjectionInt(16*1024, int(float64(opts.MaxBytes)*scale))
		opts.PinnedFromIndex = pinnedFromIndex
		stats.BudgetTokens = opts.MaxBytes / 4
		projected := CompactModelMessages(messages, opts)
		return finishProjectionStats(messages, projected, req, stats)
	}

	messageBudget, requestBudget := projectionTokenBudget(req, originalEstimate, contextWindow, scale)
	stats.BudgetTokens = messageBudget
	if originalEstimate.Messages <= messageBudget && originalEstimate.Total <= requestBudget {
		return cloneProjectionMessages(messages), stats
	}

	opts := adaptiveContextOptions(contextWindow, messageBudget, scale)
	opts.PinnedFromIndex = pinnedFromIndex
	projected := packProjectionToRequestBudget(messages, req, requestBudget, opts)
	return finishProjectionStats(messages, projected, req, stats)
}

// packProjectionToRequestBudget finds the least destructive byte budget whose
// exact request-token estimate fits. The old fixed 30% shrink loop could leave
// sizeable usable context empty after a successful retry. Searching the
// monotonic byte budget keeps as much durable evidence as the request allows.
func packProjectionToRequestBudget(messages []model.Message, req model.ChatRequest, requestBudget int, opts EfficientContextOptions) []model.Message {
	projected := CompactModelMessages(messages, opts)
	if projectionFitsRequestBudget(projected, req, requestBudget) {
		return projected
	}

	const minimumBytes = 4 * 1024
	low := minimumBytes
	high := opts.MaxBytes - 1
	var best []model.Message
	for attempt := 0; attempt < 8 && low <= high; attempt++ {
		mid := low + (high-low+1)/2
		candidateOpts := opts
		candidateOpts.MaxBytes = mid
		candidate := CompactModelMessages(messages, candidateOpts)
		if projectionFitsRequestBudget(candidate, req, requestBudget) {
			best = candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if best != nil {
		return best
	}

	// A pinned continuation prefix or a protocol-critical tool tail can be
	// larger than the normal floor. Retain the existing emergency squeeze for
	// that exceptional case, rather than relaxing those correctness invariants.
	opts.MaxBytes = minimumBytes
	for attempt := 0; attempt < 5; attempt++ {
		projected = CompactModelMessages(messages, opts)
		if projectionFitsRequestBudget(projected, req, requestBudget) {
			break
		}
		opts.RecentMessages = maxProjectionInt(8, opts.RecentMessages*3/4)
		opts.KeepReasoningRecent = maxProjectionInt(2, opts.KeepReasoningRecent*3/4)
		opts.OldToolBytes = maxProjectionInt(192, opts.OldToolBytes*3/4)
		opts.OldToolArgumentBytes = maxProjectionInt(192, opts.OldToolArgumentBytes*3/4)
		opts.OldAssistantBytes = maxProjectionInt(240, opts.OldAssistantBytes*3/4)
	}
	return projected
}

func projectionFitsRequestBudget(messages []model.Message, req model.ChatRequest, requestBudget int) bool {
	projectedReq := req
	projectedReq.Messages = messages
	return model.EstimateRequestTokens(projectedReq).Total <= requestBudget
}

// projectionTokenBudget derives the request overhead (everything but message
// content) from originalEstimate instead of re-running EstimateRequestTokens
// on a Messages=nil probe. The two are equivalent: EstimateRequestTokens
// computes Tools and Fixed independently of Messages, and a nil Messages
// slice always estimates to 1 token (the "messages":null JSON envelope), so
// the probe's Total is always 1 + originalEstimate.Tools + originalEstimate.Fixed.
func projectionTokenBudget(req model.ChatRequest, originalEstimate model.RequestTokenEstimate, contextWindow int, scale float64) (messageBudget, requestBudget int) {
	overhead := 1 + originalEstimate.Tools + originalEstimate.Fixed
	completionReserve := requestedCompletionReserve(req, contextWindow)
	safetyReserve := contextWindow / 20
	if safetyReserve < 1024 {
		safetyReserve = 1024
	}
	if safetyReserve > 32*1024 {
		safetyReserve = 32 * 1024
	}
	requestBudget = contextWindow - completionReserve - safetyReserve
	if requestBudget < 2048 {
		requestBudget = maxProjectionInt(1024, contextWindow*3/5)
	}
	messageBudget = requestBudget - overhead
	if messageBudget < 1024 {
		messageBudget = 1024
	}
	messageBudget = int(math.Floor(float64(messageBudget) * scale))
	if messageBudget < 1024 {
		messageBudget = 1024
	}
	requestBudget = overhead + messageBudget
	return messageBudget, requestBudget
}

func requestedCompletionReserve(req model.ChatRequest, contextWindow int) int {
	reserve := req.MaxCompletionTokens
	if req.MaxTokens > reserve {
		reserve = req.MaxTokens
	}
	minimum := 2048
	if contextWindow >= 128*1024 {
		minimum = 8 * 1024
	}
	if contextWindow >= 512*1024 {
		minimum = 16 * 1024
	}
	if reserve < minimum {
		reserve = minimum
	}
	maximum := contextWindow / 3
	if maximum > 0 && reserve > maximum {
		reserve = maximum
	}
	return reserve
}

func adaptiveContextOptions(contextWindow, messageBudget int, scale float64) EfficientContextOptions {
	opts := DefaultEfficientContextOptions()
	opts.MaxBytes = maxProjectionInt(4*1024, messageBudget*4)
	opts.RecentMessages = clampProjectionInt(contextWindow/4096, 24, 192)
	opts.KeepReasoningRecent = clampProjectionInt(opts.RecentMessages/4, 8, 32)
	// Use a context-scaled protected envelope instead of OpenCode's fixed
	// 20K/40K token thresholds. At 128K this preserves roughly 2K tokens per
	// older tool result; at larger windows it grows with the model while the
	// request budget remains the final authority. This keeps DeepSeek's broad
	// context useful without allowing one noisy result to dominate a request.
	opts.OldToolBytes = clampProjectionInt(contextWindow/16, opts.OldToolBytes, 64*1024)
	opts.OldToolArgumentBytes = clampProjectionInt(contextWindow/128, opts.OldToolArgumentBytes, 16*1024)
	opts.OldAssistantBytes = clampProjectionInt(contextWindow/32, opts.OldAssistantBytes, 24*1024)

	if scale < 0.75 {
		opts.RecentMessages = maxProjectionInt(12, int(float64(opts.RecentMessages)*scale))
		opts.KeepReasoningRecent = maxProjectionInt(4, int(float64(opts.KeepReasoningRecent)*scale))
		opts.OldToolBytes = maxProjectionInt(384, int(float64(opts.OldToolBytes)*scale))
		opts.OldToolArgumentBytes = maxProjectionInt(256, int(float64(opts.OldToolArgumentBytes)*scale))
		opts.OldAssistantBytes = maxProjectionInt(512, int(float64(opts.OldAssistantBytes)*scale))
	}
	return opts
}

func finishProjectionStats(original, projected []model.Message, req model.ChatRequest, stats ContextProjectionStats) ([]model.Message, ContextProjectionStats) {
	projectedReq := req
	projectedReq.Messages = projected
	stats.ProjectedTokens = model.EstimateRequestTokens(projectedReq).Total
	stats.ProjectedBytes = modelMessagesBytes(projected)
	stats.MessagesAfter = len(projected)
	stats.ToolResultsCompacted, stats.ToolBytesCompacted, stats.ReasoningBytesRemoved = projectionReductions(original, projected)
	stats.HistoryCollapsed = len(projected) != len(original)
	stats.Compacted = stats.MessagesAfter != stats.MessagesBefore ||
		stats.ProjectedBytes < stats.OriginalBytes ||
		stats.ProjectedTokens < stats.OriginalTokens
	return projected, stats
}

// projectionReductions reports only reductions that can be paired by message
// index. A collapsed historical prefix is reported separately because its
// replacement summary is intentionally not a one-to-one message mapping.
func projectionReductions(original, projected []model.Message) (toolResults, toolBytes, reasoningBytes int) {
	limit := len(original)
	if len(projected) < limit {
		limit = len(projected)
	}
	for i := 0; i < limit; i++ {
		before, after := original[i], projected[i]
		if before.Role == "tool" && after.Role == "tool" {
			beforeBytes := len(GetContentAsString(before.Content))
			afterBytes := len(GetContentAsString(after.Content))
			if afterBytes < beforeBytes {
				toolResults++
				toolBytes += beforeBytes - afterBytes
			}
		}
		if before.Role == "assistant" && after.Role == "assistant" {
			beforeBytes := len(before.Reasoning)
			afterBytes := len(after.Reasoning)
			if afterBytes < beforeBytes {
				reasoningBytes += beforeBytes - afterBytes
			}
		}
	}
	return toolResults, toolBytes, reasoningBytes
}

func cloneProjectionMessages(messages []model.Message) []model.Message {
	cloned := make([]model.Message, len(messages))
	copy(cloned, messages)
	for i := range cloned {
		cloned[i].ToolCalls = append([]model.ToolCall(nil), messages[i].ToolCalls...)
		cloned[i].ReasoningDetails = append([]model.ReasoningDetail(nil), messages[i].ReasoningDetails...)
	}
	return cloned
}

func normalizedProjectionScale(scale float64) float64 {
	if scale <= 0 || scale > 1 {
		return 1
	}
	if scale < 0.2 {
		return 0.2
	}
	return scale
}

func clampProjectionInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func maxProjectionInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
