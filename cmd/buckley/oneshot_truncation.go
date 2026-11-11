package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
)

const (
	// truncationRetryInstruction is the user message added for the one retry
	// after a completion that ended at its output cap with no answer text.
	truncationRetryInstruction = "Your previous attempt ended at the output limit before you wrote any answer text. Answer now. Write the answer first, directly and concisely, in the requested format; keep private reasoning short."
	// truncationReasoningKeepBytes bounds the reasoning text kept in memory.
	truncationReasoningKeepBytes = 256 * 1024
	truncationRetryMinBudget     = 1024
)

// oneShotLastResponse records what the most recent model response looked like,
// so the loop can tell a completion that ended at its output cap with no
// answer text from one that did not.
type oneShotLastResponse struct {
	valid           bool
	truncated       bool
	finish          string
	publicBytes     int
	hadToolCalls    bool
	reasoning       string
	reasoningTokens int
	inputTokens     int
	outputCap       int
	effort          string // effort word the request carried, if any
}

// emptyTruncation reports a completion that ended at its output cap with no
// public text and no tool call.
func (r oneShotLastResponse) emptyTruncation() bool {
	return r.valid && r.truncated && r.publicBytes == 0 && !r.hadToolCalls
}

// oneShotTruncationState is the retry bookkeeping of one loop run.
type oneShotTruncationState struct {
	last        oneShotLastResponse
	retries     int
	override    *model.ReasoningConfig // applied to every later request
	retryBudget int                    // reasoning budget of the retry, 0 when it used an effort
	retryEffort string
	reasoning   strings.Builder // reasoning text of every empty attempt, bounded
}

// recordLastResponse stores the shape of resp (the post-controller request is req).
func (s *oneShotTruncationState) recordLastResponse(req model.ChatRequest, resp *model.ChatResponse, observedToolDelta bool) {
	if s == nil || resp == nil || len(resp.Choices) == 0 {
		return
	}
	choice := resp.Choices[0]
	text, _ := model.ExtractTextContent(choice.Message.Content)
	last := oneShotLastResponse{
		valid:        true,
		truncated:    model.IsTruncatedFinishReason(choice.FinishReason),
		finish:       choice.FinishReason,
		publicBytes:  len(strings.TrimSpace(text)),
		hadToolCalls: len(choice.Message.ToolCalls) > 0 || observedToolDelta,
		reasoning:    choice.Message.Reasoning,
		inputTokens:  resp.Usage.PromptTokens,
		outputCap:    max(req.MaxTokens, req.MaxCompletionTokens),
	}
	if resp.UsagePresent && resp.Usage.CompletionTokenDetails != nil {
		last.reasoningTokens = resp.Usage.CompletionTokenDetails.ReasoningTokens
	}
	if req.Reasoning != nil {
		last.effort = req.Reasoning.Effort
	}
	s.last = last
}

// keepReasoning appends the last response's reasoning text, up to the bound.
func (s *oneShotTruncationState) keepReasoning() {
	if s == nil || strings.TrimSpace(s.last.reasoning) == "" {
		return
	}
	room := truncationReasoningKeepBytes - s.reasoning.Len()
	if room <= 0 {
		return
	}
	text := s.last.reasoning
	if len(text) > room {
		text = text[:room]
	}
	if s.reasoning.Len() > 0 {
		s.reasoning.WriteString("\n\n--- next attempt ---\n\n")
	}
	s.reasoning.WriteString(text)
}

// planTruncationRetryReasoning chooses the reasoning setting for the retry:
// a budget of max(1,024, cap/8) tokens for providers that take one, else the
// effort "low" ("minimal" if the first attempt already used "low"). It returns
// nil when the route does not support reasoning, so the retry only adds the
// instruction.
func planTruncationRetryReasoning(last oneShotLastResponse, providerID string, reasoningSupported bool) (cfg *model.ReasoningConfig, budget int, effort string) {
	if !reasoningSupported {
		return nil, 0, ""
	}
	if truncationBudgetProvider(providerID) {
		budget = max(truncationRetryMinBudget, last.outputCap/8)
		if last.outputCap > budget {
			return &model.ReasoningConfig{MaxTokens: budget}, budget, ""
		}
	}
	effort = "low"
	if strings.EqualFold(last.effort, "low") {
		effort = "minimal"
	}
	return &model.ReasoningConfig{Effort: effort}, 0, effort
}

// oneShotNoAnswerError reports a run whose completion, and its one retry,
// ended at the output cap without answer text.
type oneShotNoAnswerError struct {
	cause           error
	Finish          string
	OutputCap       int
	ReasoningTokens int
	InputTokens     int
	Retries         int
	RetryBudget     int
	RetryEffort     string
	ReasoningPath   string
}

func (e *oneShotNoAnswerError) Unwrap() error { return e.cause }

func (e *oneShotNoAnswerError) Error() string { return e.Message() }

// Message names the cause and three remedies.
func (e *oneShotNoAnswerError) Message() string {
	var b strings.Builder
	b.WriteString("Incomplete result: ")
	if e.ReasoningTokens > 0 {
		fmt.Fprintf(&b, "the model used its whole output cap of %d tokens on reasoning (%d reasoning tokens reported) and wrote no answer", e.OutputCap, e.ReasoningTokens)
	} else {
		fmt.Fprintf(&b, "the model ended at its output cap of %d tokens and wrote no answer", e.OutputCap)
	}
	switch {
	case e.RetryBudget > 0:
		fmt.Fprintf(&b, "; one retry with reasoning limited to %d tokens also wrote no answer", e.RetryBudget)
	case e.RetryEffort != "":
		fmt.Fprintf(&b, "; one retry with reasoning effort %s also wrote no answer", e.RetryEffort)
	default:
		b.WriteString("; one retry also wrote no answer")
	}
	fmt.Fprintf(&b, ". Next: lower the reasoning effort (--effort low), raise --max-output-tokens (now %d), or shorten the prompt (%d input tokens).", e.OutputCap, e.InputTokens)
	return b.String()
}

// CauseLine is the standard-error line that carries the machine-readable cause.
func (e *oneShotNoAnswerError) CauseLine() string {
	saved := e.ReasoningPath
	if saved == "" {
		saved = "none"
	}
	return fmt.Sprintf("One-shot cause: no_answer_text finish=%s output_cap=%d reasoning_tokens=%d retries=%d reasoning_saved=%s", e.Finish, e.OutputCap, e.ReasoningTokens, e.Retries, saved)
}

// saveOneShotReasoning writes the reasoning text to a file under
// <workDir>/.buckley/logs and returns its path, or "" when there is nothing to
// save or the write fails.
func saveOneShotReasoning(workDir, text string) string {
	if strings.TrimSpace(text) == "" || strings.TrimSpace(workDir) == "" {
		return ""
	}
	dir := filepath.Join(workDir, ".buckley", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, "oneshot-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".reasoning.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return ""
	}
	return path
}

// noAnswerErrorFor returns the clear failure when the retry was used and the
// last response again ended at the output cap with no answer text; else nil.
func (s *oneShotTruncationState) noAnswerErrorFor(result *agentloop.Result, cause error, limits acpLoopLimits, workDir string) error {
	if s == nil || result == nil || limits.truncatedEmptyRetries <= 0 || s.retries < limits.truncatedEmptyRetries {
		return nil
	}
	if result.FinishReason != agentloop.FinishReasonInvalidCompletion || !s.last.emptyTruncation() {
		return nil
	}
	s.keepReasoning()
	return &oneShotNoAnswerError{
		cause:           cause,
		Finish:          s.last.finish,
		OutputCap:       s.last.outputCap,
		ReasoningTokens: s.last.reasoningTokens,
		InputTokens:     s.last.inputTokens,
		Retries:         s.retries,
		RetryBudget:     s.retryBudget,
		RetryEffort:     s.retryEffort,
		ReasoningPath:   saveOneShotReasoning(workDir, s.reasoning.String()),
	}
}

// truncationBudgetProvider reports whether Buckley already handles a reasoning
// budget (reasoning.max_tokens) for the provider.
func truncationBudgetProvider(providerID string) bool {
	return providerID == "openrouter" || providerID == "anthropic"
}
