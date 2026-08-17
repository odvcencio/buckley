package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
)

const (
	agentLoopMaxSerializedBytes = 4096
	agentLoopMaxIdentifierBytes = 128
	agentLoopMaxCounter         = 1_000_000_000
)

// NewAgentLoopObserver adapts the shared Controller lifecycle stream to the
// existing non-blocking telemetry hub. The projection has a fixed schema:
// bounded identifiers and numeric counters, allowlisted categorical values,
// and non-reversible fingerprints for free-form errors or unknown decisions.
// It never publishes prompts, tool arguments/results, model output, evidence
// bodies, or raw provider/tool/controller error text.
func NewAgentLoopObserver(hub *Hub) lifecycle.Observer {
	return func(source lifecycle.Event) {
		if hub == nil {
			return
		}

		lifecycleType := knownLifecycleType(source.Type)
		data := map[string]any{
			"sequence":       source.Sequence,
			"lifecycle_type": lifecycleType,
		}
		putIdentifier(data, "run_id", source.RunID)
		putIdentifier(data, "task_id", source.TaskID)
		putIdentifier(data, "turn_id", source.TurnID)
		putIdentifier(data, "step_id", source.StepID)
		putIdentifier(data, "model", source.ModelID)
		putIdentifier(data, "provider", source.ProviderID)
		putIdentifier(data, "tool", source.ToolName)
		putIdentifier(data, "call_id", source.ToolCallID)
		putPositiveCounter(data, "round", source.Round)
		putPositiveCounter(data, "attempt", source.Attempt)
		putPositiveCounter(data, "run_attempt", source.RunAttempt)
		if source.Continuation {
			data["continuation"] = true
		}

		if source.Phase != "" {
			data["phase"] = knownPhase(source.Phase)
		}
		if source.Replayed {
			data["replayed"] = true
		}
		if source.Success != nil {
			data["success"] = *source.Success
		}
		putPositiveCounter(data, "prompt_tokens", source.Usage.PromptTokens)
		putPositiveCounter(data, "completion_tokens", source.Usage.CompletionTokens)
		putPositiveCounter(data, "total_tokens", source.Usage.TotalTokens)
		if source.CostUSD > 0 && !math.IsNaN(source.CostUSD) && !math.IsInf(source.CostUSD, 0) {
			data["cost_usd"] = source.CostUSD
		}
		if source.Status != "" {
			data["status"] = knownStatus(source.Status)
		}
		if source.FinishReason != "" {
			code, known := knownFinishCode(source.FinishReason)
			data["finish_code"] = code
			if !known {
				putFingerprint(data, "finish", source.FinishReason)
			}
		}
		if source.StopReason != "" {
			code, known := knownStopCode(source.StopReason)
			data["stop_code"] = code
			if !known {
				putFingerprint(data, "stop", source.StopReason)
			}
		}
		if source.Error != "" {
			data["error_code"] = errorCode(source.Type, source.Phase)
			putFingerprint(data, "error", source.Error)
		}

		projected := Event{
			Type:      agentLoopTelemetryType(source.Type),
			Timestamp: boundedLifecycleTimestamp(source.Timestamp),
			SessionID: boundedIdentifier(source.SessionID),
			TaskID:    boundedIdentifier(source.TaskID),
			Data:      data,
		}
		if !agentLoopEventWithinBound(projected) {
			// This should only be reachable if the fixed schema grows without
			// its bounds being updated. Preserve ordering/correlation metadata
			// rather than allowing an unexpectedly large event onto the hub.
			projected.Data = map[string]any{
				"sequence":       source.Sequence,
				"lifecycle_type": lifecycleType,
				"projection":     "size_limited",
			}
			putIdentifier(projected.Data, "turn_id", source.TurnID)
			if !agentLoopEventWithinBound(projected) {
				return
			}
		}
		hub.Publish(projected)
	}
}

func agentLoopEventWithinBound(event Event) bool {
	encoded, err := json.Marshal(event)
	return err == nil && len(encoded) <= agentLoopMaxSerializedBytes
}

func boundedLifecycleTimestamp(timestamp time.Time) time.Time {
	if timestamp.IsZero() || timestamp.Year() < 0 || timestamp.Year() > 9999 {
		return time.Now().UTC()
	}
	return timestamp.UTC()
}

func putIdentifier(data map[string]any, key, value string) {
	if projected := boundedIdentifier(value); projected != "" {
		data[key] = projected
	}
}

func boundedIdentifier(value string) string {
	if value == "" {
		return ""
	}
	normalized := strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if normalized == "" {
		return ""
	}
	if len(normalized) <= agentLoopMaxIdentifierBytes && isMetadataIdentifier(normalized) {
		return normalized
	}
	return stableFingerprint(value)
}

func isMetadataIdentifier(value string) bool {
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case strings.ContainsRune("._:/@+-", char):
		default:
			return false
		}
	}
	return value != ""
}

func putPositiveCounter(data map[string]any, key string, value int) {
	if value <= 0 {
		return
	}
	if value > agentLoopMaxCounter {
		value = agentLoopMaxCounter
	}
	data[key] = value
}

func putFingerprint(data map[string]any, prefix, value string) {
	data[prefix+"_fingerprint"] = stableFingerprint(value)
	data[prefix+"_length"] = len(value)
}

func stableFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:16])
}

func knownLifecycleType(eventType lifecycle.EventType) string {
	switch eventType {
	case lifecycle.TurnStart, lifecycle.AttemptStart, lifecycle.AttemptEnd, lifecycle.StepStart, lifecycle.ModelRequest,
		lifecycle.ModelResponse, lifecycle.ModelError, lifecycle.ToolCall,
		lifecycle.ToolStart, lifecycle.ToolResult, lifecycle.ToolError,
		lifecycle.TurnStopping, lifecycle.TurnEnd:
		return string(eventType)
	default:
		return "unknown"
	}
}

func knownPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ToValidUTF8(phase, "�"))) {
	case "model":
		return "model"
	case "finalize":
		return "finalize"
	case "tool":
		return "tool"
	default:
		return "unknown"
	}
}

func knownStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ToValidUTF8(status, "�"))) {
	case "conclusive":
		return "conclusive"
	case "incomplete":
		return "incomplete"
	default:
		return "unknown"
	}
}

func knownFinishCode(reason string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.ToValidUTF8(reason, "�"))) {
	case "loop_guard":
		return "loop_guard", true
	case "step_cap":
		return "step_cap", true
	case "empty_choices":
		return "empty_choices", true
	case "invalid_completion":
		return "invalid_completion", true
	case "model_error":
		return "model_error", true
	default:
		return "other", false
	}
}

func knownStopCode(reason string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.ToValidUTF8(reason, "�"))) {
	case "step_cap":
		return "step_cap", true
	case "round_limit":
		return "round_limit", true
	case "model_request_limit":
		return "model_request_limit", true
	case "tool_call_limit":
		return "tool_call_limit", true
	case "exact_repeat":
		return "exact_repeat", true
	case "action_cycle":
		return "action_cycle", true
	case "outcome_repeat":
		return "outcome_repeat", true
	case "cost_limit":
		return "cost_limit", true
	case "cost_accounting":
		return "cost_accounting", true
	case "model_error":
		return "model_error", true
	case "empty_choices":
		return "empty_choices", true
	case "invalid_completion":
		return "invalid_completion", true
	case "emergency_fuse":
		return "emergency_fuse", true
	case "finalization_started":
		return "finalization_started", true
	case "finalization_completed":
		return "finalization_completed", true
	case "finalization_failed":
		return "finalization_failed", true
	case "progress_policy":
		return "progress_policy", true
	default:
		return "other", false
	}
}

func errorCode(eventType lifecycle.EventType, phase string) string {
	switch eventType {
	case lifecycle.ModelError:
		return "model_error"
	case lifecycle.ToolError:
		return "tool_error"
	case lifecycle.TurnEnd:
		return "finalization_error"
	default:
		switch knownPhase(phase) {
		case "model":
			return "model_error"
		case "tool":
			return "tool_error"
		case "finalize":
			return "finalization_error"
		default:
			return "lifecycle_error"
		}
	}
}

func agentLoopTelemetryType(eventType lifecycle.EventType) EventType {
	switch eventType {
	case lifecycle.TurnStart:
		return EventAgentTurnStarted
	case lifecycle.AttemptStart:
		return EventAgentAttemptStarted
	case lifecycle.AttemptEnd:
		return EventAgentAttemptEnded
	case lifecycle.StepStart:
		return EventAgentStepStarted
	case lifecycle.ModelRequest:
		return EventAgentModelRequest
	case lifecycle.ModelResponse:
		return EventAgentModelResponse
	case lifecycle.ModelError:
		return EventAgentModelError
	case lifecycle.ToolCall:
		return EventAgentToolCall
	case lifecycle.ToolStart:
		return EventAgentToolStarted
	case lifecycle.ToolResult:
		return EventAgentToolResult
	case lifecycle.ToolError:
		return EventAgentToolError
	case lifecycle.TurnStopping:
		return EventAgentTurnStopping
	case lifecycle.TurnEnd:
		return EventAgentTurnEnded
	default:
		return EventDebug
	}
}
