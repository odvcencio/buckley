package conversation

import (
	"math"

	"m31labs.dev/buckley/v2/pkg/model"
)

// ContextProjectionStats describes how durable conversation history was
// projected into one provider request. Projection never mutates stored history.
type ContextProjectionStats struct {
	ContextWindow  int
	BudgetTokens   int
	OriginalTokens int
	ProjectedTokens int
	MessagesBefore int
	MessagesAfter  int
	Compacted      bool
	Emergency      bool
	Scale          float64
}

// ProjectModelMessagesForRequest preserves full history while it fits within
// the model's advertised context, then progressively compacts older evidence as
// pressure rises. scale is normally 1; callers may lower it for an emergency
// retry after a provider reports an unexpectedly smaller effective window.
func ProjectModelMessagesForRequest(messages []model.Message, req model.ChatRequest, contextWindow int, scale float64) ([]model.Message, ContextProjectionStats) {
	scale = normalizedProjectionScale(scale)
	fullReq := req
	fullReq.Messages = messages
	originalEstimate := model.EstimateRequestTokens(fullReq)
	stats := ContextProjectionStats{
		ContextWindow:   contextWindow,
		OriginalTokens:  originalEstimate.Total,
		ProjectedTokens: originalEstimate.Total,
		MessagesBefore:  len(messages),
		MessagesAfter:   len(messages),
		Emergency:       scale < 0.999,
		Scale:           scale,
	}

	if contextWindow <= 0 {
		opts := DefaultEfficientContextOptions()
		opts.MaxBytes = maxProjectionInt(16*1024, int(float64(opts.MaxBytes)*scale))
		stats.BudgetTokens = opts.MaxBytes / 4
		projected := CompactModelMessages(messages, opts)
		return finishProjectionStats(projected, req, stats)
	}

	messageBudget, requestBudget := projectionTokenBudget(req, contextWindow, scale)
	stats.BudgetTokens = messageBudget
	if originalEstimate.Messages <= messageBudget && originalEstimate.Total <= requestBudget {
		return cloneProjectionMessages(messages), stats
	}

	opts := adaptiveContextOptions(contextWindow, messageBudget, scale)
	projected := CompactModelMessages(messages, opts)
	for attempt := 0; attempt < 5; attempt++ {
		projectedReq := req
		projectedReq.Messages = projected
		if model.EstimateRequestTokens(projectedReq).Total <= requestBudget {
			break
		}
		opts.MaxBytes = maxProjectionInt(4*1024, opts.MaxBytes*7/10)
		opts.RecentMessages = maxProjectionInt(8, opts.RecentMessages*3/4)
		opts.KeepReasoningRecent = maxProjectionInt(2, opts.KeepReasoningRecent*3/4)
		opts.OldToolBytes = maxProjectionInt(192, opts.OldToolBytes*3/4)
		opts.OldToolArgumentBytes = maxProjectionInt(192, opts.OldToolArgumentBytes*3/4)
		opts.OldAssistantBytes = maxProjectionInt(240, opts.OldAssistantBytes*3/4)
		projected = CompactModelMessages(messages, opts)
	}
	return finishProjectionStats(projected, req, stats)
}

func projectionTokenBudget(req model.ChatRequest, contextWindow int, scale float64) (messageBudget, requestBudget int) {
	probe := req
	probe.Messages = nil
	overhead := model.EstimateRequestTokens(probe).Total
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
	opts.OldToolBytes = clampProjectionInt(contextWindow/64, opts.OldToolBytes, 8*1024)
	opts.OldToolArgumentBytes = clampProjectionInt(contextWindow/256, opts.OldToolArgumentBytes, 4*1024)
	opts.OldAssistantBytes = clampProjectionInt(contextWindow/48, opts.OldAssistantBytes, 10*1024)

	if scale < 0.75 {
		opts.RecentMessages = maxProjectionInt(12, int(float64(opts.RecentMessages)*scale))
		opts.KeepReasoningRecent = maxProjectionInt(4, int(float64(opts.KeepReasoningRecent)*scale))
		opts.OldToolBytes = maxProjectionInt(384, int(float64(opts.OldToolBytes)*scale))
		opts.OldToolArgumentBytes = maxProjectionInt(256, int(float64(opts.OldToolArgumentBytes)*scale))
		opts.OldAssistantBytes = maxProjectionInt(512, int(float64(opts.OldAssistantBytes)*scale))
	}
	return opts
}

func finishProjectionStats(projected []model.Message, req model.ChatRequest, stats ContextProjectionStats) ([]model.Message, ContextProjectionStats) {
	projectedReq := req
	projectedReq.Messages = projected
	stats.ProjectedTokens = model.EstimateRequestTokens(projectedReq).Total
	stats.MessagesAfter = len(projected)
	stats.Compacted = stats.MessagesAfter != stats.MessagesBefore ||
		modelMessagesBytes(projected) < modelMessagesBytes(reqMessagesForComparison(req, stats, projected)) ||
		stats.ProjectedTokens < stats.OriginalTokens
	return projected, stats
}

// reqMessagesForComparison exists only to keep compaction detection independent
// from token-estimator rounding. The original byte count is reconstructed from
// stats by callers through OriginalTokens when req.Messages is unavailable, so
// projected-token reduction remains the primary signal.
func reqMessagesForComparison(_ model.ChatRequest, stats ContextProjectionStats, projected []model.Message) []model.Message {
	if stats.ProjectedTokens < stats.OriginalTokens || stats.MessagesAfter != stats.MessagesBefore {
		return make([]model.Message, len(projected)+1)
	}
	return projected
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
