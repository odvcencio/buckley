package agentloop

import (
	"errors"
	"fmt"
	"strings"
)

// TaskIntent classifies a task by its execution model and expected outcomes.
type TaskIntent string

const (
	// MutationIntent tasks require observable workspace changes and post-change verification.
	MutationIntent TaskIntent = "mutation"
	// ReadOnlyIntent tasks accept answers without requiring workspace changes.
	ReadOnlyIntent TaskIntent = "read_only"
	// UnknownIntent is the fallback when intent cannot be determined.
	UnknownIntent TaskIntent = "unknown"
)

func ParseTaskIntent(value string) (TaskIntent, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(UnknownIntent):
		return UnknownIntent, nil
	case string(ReadOnlyIntent), "read-only", "readonly":
		return ReadOnlyIntent, nil
	case string(MutationIntent), "mutate", "write":
		return MutationIntent, nil
	default:
		return "", fmt.Errorf("task intent must be unknown, read_only, or mutation")
	}
}

func (intent TaskIntent) Valid() bool {
	switch intent {
	case "", UnknownIntent, ReadOnlyIntent, MutationIntent:
		return true
	default:
		return false
	}
}

type CompletionContractReason string

const (
	CompletionMissingObservableChange       CompletionContractReason = "missing_observable_change"
	CompletionMissingPostChangeVerification CompletionContractReason = "missing_post_change_verification"
	CompletionFailedPostChangeVerification  CompletionContractReason = "failed_post_change_verification"
	CompletionStateObservationFailed        CompletionContractReason = "state_observation_failed"
	CompletionUnknownTaskIntent             CompletionContractReason = "unknown_task_intent"
	CompletionInvalidFinalResponse          CompletionContractReason = "invalid_final_response"
)

const (
	// TerminationCompletedUnverified ends a continuing run whose model gave a
	// final answer while no check could verify the latest change: the workspace
	// has no test or build command, or the attempts to run one all failed to
	// start. The run is conclusive, and the reason says it is unverified.
	TerminationCompletedUnverified = "completed_unverified"
	// TerminationVerificationStalled ends a continuing run that could not reach
	// a passing verification: the same check kept failing with no change, or
	// verification stayed unsettled across many continuations. The run is
	// incomplete.
	TerminationVerificationStalled = "verification_stalled"
)

const (
	// IncompleteToolRoundInterrupted reports that a tool round stopped after
	// some outcomes were confirmed while at least one callback, persistence, or
	// observer path remained unresolved. The raw cause stays in the error chain;
	// this code is the bounded public projection.
	IncompleteToolRoundInterrupted = "tool_round_interrupted"
)

type CompletionContractError struct {
	Reason CompletionContractReason
	Detail string
}

func (e *CompletionContractError) Error() string {
	if e == nil {
		return "completion contract failed"
	}
	if e.Detail != "" {
		return e.Detail
	}
	return string(e.Reason)
}

type IncompleteResultNotice struct {
	Code       string
	Reason     string
	NextAction string
	Message    string
}

func PresentIncompleteResult(err error) IncompleteResultNotice {
	notice := IncompleteResultNotice{
		Code:       "incomplete_turn",
		Reason:     "the turn stopped before a conclusive answer was produced",
		NextAction: "Review the partial evidence and ask Buckley to retry after addressing the reason above.",
	}
	var incomplete *IncompleteTurnError
	if errors.As(err, &incomplete) {
		if code := strings.TrimSpace(incomplete.Code); code != "" {
			notice.Code = code
		} else if finish := strings.TrimSpace(incomplete.FinishReason); finish != "" {
			notice.Code = finish
		}
		if reason := strings.TrimSpace(incomplete.Reason); reason != "" {
			notice.Reason = reason
		}
		if detail := strings.TrimSpace(incomplete.FinalizationError); detail != "" {
			notice.Reason = detail
		}
		if detail := strings.TrimSpace(incomplete.ProviderError); detail != "" && notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = detail
		}
	}
	var contractErr *CompletionContractError
	if errors.As(err, &contractErr) {
		if contractErr.Reason != "" {
			notice.Code = string(contractErr.Reason)
		}
		if detail := strings.TrimSpace(contractErr.Detail); detail != "" {
			notice.Reason = detail
		}
	}
	applyIncompleteNoticeCode(&notice)
	notice.Code = sanitizeIncompleteNoticeCode(notice.Code)
	notice.Reason = sanitizeIncompleteNoticeText(notice.Reason, 240)
	notice.NextAction = sanitizeIncompleteNoticeText(notice.NextAction, 180)
	if notice.Code == FinishReasonModelError {
		notice.NextAction = "Retry the request; Buckley preserved any partial provider evidence instead of treating it as final."
	}
	if notice.Code == IncompleteToolRoundInterrupted {
		if notice.Reason == "" || notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = "tool execution stopped with unresolved outcome evidence"
		}
		notice.NextAction = "Inspect and reconcile the actual workspace state before retrying or continuing."
	}
	notice.Message = "Incomplete result: " + strings.TrimSuffix(notice.Reason, ".") + ". Next: " + notice.NextAction
	return notice
}

func applyIncompleteNoticeCode(notice *IncompleteResultNotice) {
	if notice == nil {
		return
	}
	switch CompletionContractReason(notice.Code) {
	case CompletionMissingObservableChange:
		if notice.Reason == "" || notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = "task requires observable workspace change but no mutations were recorded"
		}
		notice.NextAction = "Ask Buckley to make the requested change, then run the cheapest relevant verification."
	case CompletionMissingPostChangeVerification:
		if notice.Reason == "" || notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = "missing successful verification after the latest workspace change"
		}
		notice.NextAction = "Ask Buckley to run the cheapest relevant verification after the latest change."
	case CompletionFailedPostChangeVerification:
		if notice.Reason == "" || notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = "latest verification after the final workspace change did not pass"
		}
		notice.NextAction = "Ask Buckley to fix the verification failure if it is in scope, then rerun verification."
	case CompletionStateObservationFailed:
		if notice.Reason == "" || notice.Reason == "the turn stopped before a conclusive answer was produced" {
			notice.Reason = "workspace state could not be observed after a tool that may affect completion evidence"
		}
		notice.NextAction = "Restore observable workspace state or report that blocker without claiming verification."
	case TerminationVerificationStalled:
		notice.NextAction = "Fix or replace the check that keeps failing, or verify the change by hand, then rerun."
	}
}

func sanitizeIncompleteNoticeCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "incomplete_turn"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "incomplete_turn"
	}
	code := b.String()
	if len(code) > 64 {
		return code[:64]
	}
	return code
}

func sanitizeIncompleteNoticeText(value string, limit int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return "the turn stopped before a conclusive answer was produced"
	}
	return truncateIncompleteNoticeText(value, limit)
}

func truncateIncompleteNoticeText(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	const ellipsis = "..."
	ellipsisRunes := []rune(ellipsis)
	if limit <= len(ellipsisRunes) {
		return string(ellipsisRunes[:limit])
	}
	keep := limit - len(ellipsisRunes)
	return strings.TrimSpace(string(runes[:keep])) + ellipsis
}

// CompletionContract is an optional, provider-neutral gate for accepting a
// terminal assistant answer as a usable result. Callers that do not populate
// it retain the legacy shared-controller behavior.
type CompletionContract struct {
	RequirePostChangeVerification bool
	RequireObservableChange       bool
	MaxRepairAttempts             int
	MaxContinuations              int
	// MaxNoChangeContinuations caps continuations rejected only because no
	// workspace change was recorded. Zero means no separate cap.
	MaxNoChangeContinuations int
	// The verification limits below apply only to continuing runs
	// (MaxContinuations > 0) and only to answers rejected for missing or failed
	// post-change verification. Zero disables each one.
	//
	// MaxVerificationUnavailable ends the run as completed_unverified once this
	// many verification calls in a row could not run.
	MaxVerificationUnavailable int
	// MaxVerificationStalls ends the run as verification_stalled once the same
	// verification has failed this many times in a row with no workspace change
	// between runs.
	MaxVerificationStalls int
	// MaxVerificationContinuations ends the run as verification_stalled after
	// this many continuations rejected for verification reasons.
	MaxVerificationContinuations int
	// VerificationSurface reports whether any verification command can apply to
	// the workspace at all. When it reports false, an answer that lacks
	// verification ends the run as completed_unverified with the given reason.
	VerificationSurface       func() (available bool, reason string)
	OnContinuation            func(int, string)
	TolerateObservationErrors bool
	RepairInstruction         string
	TaskIntent                TaskIntent
	ValidateFinalResponse     func(string) error
	// ObserveObservableChange optionally checks session-scoped mutation evidence
	// instead of treating every tool-level state transition as task work.
	ObserveObservableChange func() (bool, error)

	// SubmittedResponse optionally supplies a caller-owned result accepted by a
	// tool. Returning ready=false retains normal model completion. Returning
	// ready=true means the caller-owned result was accepted by a tool and is
	// used as the final response, but it still requires the controller's
	// progress and output gates.
	SubmittedResponse func() (string, bool)
}

// Normalize returns the contract with safe execution defaults applied.
func (c CompletionContract) Normalize() CompletionContract {
	if c.MaxRepairAttempts <= 0 {
		c.MaxRepairAttempts = 1
	}
	if c.RepairInstruction == "" {
		c.RepairInstruction = defaultCompletionRepairInstruction
	}
	if c.TaskIntent == "" {
		c.TaskIntent = UnknownIntent
	}
	return c
}

const defaultCompletionRepairInstruction = "Your previous answer was not yet usable because the workspace changed after the last successful verification. Run the cheapest relevant verification now, then answer with the result."

func (c CompletionContract) Validate(snapshot ProgressSnapshot) error {
	return c.Normalize().evaluate(snapshot)
}

func (c CompletionContract) RepairInstructionFor(err error) string {
	normalized := c.Normalize()
	if normalized.RepairInstruction != "" && normalized.RepairInstruction != defaultCompletionRepairInstruction {
		return normalized.RepairInstruction
	}
	var contractErr *CompletionContractError
	if errors.As(err, &contractErr) {
		switch contractErr.Reason {
		case CompletionMissingObservableChange:
			return "Your previous answer was not yet usable because this task requires an observable workspace change and none was recorded. Make the smallest scoped change now, then run the cheapest relevant verification before answering."
		case CompletionFailedPostChangeVerification:
			return "Your previous answer was not yet usable because the latest verification after the final workspace change failed. Fix the failure if it is in scope, rerun the cheapest relevant verification, then answer with the result."
		case CompletionMissingPostChangeVerification:
			return defaultCompletionRepairInstruction
		case CompletionStateObservationFailed:
			return "Your previous answer was not yet usable because Buckley could not observe workspace state before and after a tool that may affect completion evidence. If the workspace is not a Git checkout or state cannot be observed, report that blocker without claiming the change was verified; otherwise run the cheapest scoped check that restores observable evidence, then answer."
		case CompletionInvalidFinalResponse:
			return "Your previous response failed its output contract: " + contractErr.Detail + ". Correct the output or gather missing evidence with offered tools within the remaining budget; do not invent results."
		}
	}
	return defaultCompletionRepairInstruction
}

func (c CompletionContract) evaluateFinalResponse(snapshot ProgressSnapshot, text string) error {
	if err := c.evaluate(snapshot); err != nil {
		return err
	}
	if c.ValidateFinalResponse == nil {
		return nil
	}
	if err := c.ValidateFinalResponse(text); err != nil {
		return &CompletionContractError{
			Reason: CompletionInvalidFinalResponse,
			Detail: "required final response is invalid: " + err.Error(),
		}
	}
	return nil
}

func (c CompletionContract) evaluate(snapshot ProgressSnapshot) error {
	normalized := c.Normalize()

	stateChanged := snapshot.StateChangedCalls > 0
	observableChange := stateChanged
	if normalized.RequireObservableChange && normalized.ObserveObservableChange != nil {
		var err error
		observableChange, err = normalized.ObserveObservableChange()
		if err != nil {
			return &CompletionContractError{
				Reason: CompletionStateObservationFailed,
				Detail: "session mutations could not be observed: " + err.Error(),
			}
		}
		stateChanged = stateChanged || observableChange
	}
	if normalized.RequirePostChangeVerification && !normalized.TolerateObservationErrors && snapshot.StateObservationFailures > 0 {
		detail := "workspace state could not be observed after a tool that may affect completion evidence"
		if snapshot.LastStateObservationError != "" {
			detail += ": " + snapshot.LastStateObservationError
		}
		return &CompletionContractError{
			Reason: CompletionStateObservationFailed,
			Detail: detail,
		}
	}
	if normalized.RequirePostChangeVerification && stateChanged {
		lastChange := snapshot.LastStateChangeSequence
		if normalized.TolerateObservationErrors {
			lastChange = max(lastChange, snapshot.LastStateFailureSequence)
		}
		if snapshot.LastVerificationSequence <= lastChange {
			return &CompletionContractError{
				Reason: CompletionMissingPostChangeVerification,
				Detail: "missing successful verification after the latest workspace change",
			}
		}
		if !snapshot.LastVerificationPassed {
			return &CompletionContractError{
				Reason: CompletionFailedPostChangeVerification,
				Detail: "latest verification after the final workspace change did not pass",
			}
		}
	}

	switch normalized.TaskIntent {
	case ReadOnlyIntent:
		return nil
	case MutationIntent:
		if normalized.RequireObservableChange {
			if !observableChange {
				return &CompletionContractError{
					Reason: CompletionMissingObservableChange,
					Detail: "task requires observable workspace change but no mutations were recorded",
				}
			}
		}
		return nil
	case UnknownIntent:
		if normalized.RequireObservableChange && !observableChange {
			return &CompletionContractError{
				Reason: CompletionMissingObservableChange,
				Detail: "task requires observable workspace change but no mutations were recorded",
			}
		}
		return nil
	default:
		return &CompletionContractError{
			Reason: CompletionUnknownTaskIntent,
			Detail: fmt.Sprintf("unknown task intent %q", normalized.TaskIntent),
		}
	}
}

// verificationAction is what a rejected final answer leads to.
type verificationAction int

const (
	// verificationContinue sends the answer back with the usual instruction.
	verificationContinue verificationAction = iota
	// verificationConfirm asks the model once whether its work is finished,
	// because nothing in the workspace can run a check. The answer to that
	// question decides: more work, or a final report of unverified work.
	verificationConfirm
	// verificationEndUnverified ends the run as completed_unverified.
	verificationEndUnverified
	// verificationEndStalled ends the run as verification_stalled.
	verificationEndStalled
)

// verificationStop decides what to do with a final answer that the contract
// rejected for missing or failed post-change verification. rejections counts
// the earlier continuations rejected for verification reasons, and confirmed
// says the model was already asked whether its work is finished.
//
// The order matters. Evidence that nothing can verify the change ends the run
// as completed_unverified, after one confirmation question when the workspace
// has no check at all, so a model that only narrated a next step is not cut
// off. A real failing check is never reported as unverified work: it ends as
// verification_stalled only when the model keeps re-running it unchanged.
func (c CompletionContract) verificationStop(err error, snapshot ProgressSnapshot, rejections int, confirmed bool) (verificationAction, string) {
	var contractErr *CompletionContractError
	if !errors.As(err, &contractErr) {
		return verificationContinue, ""
	}
	switch contractErr.Reason {
	case CompletionMissingPostChangeVerification:
		if c.VerificationSurface != nil {
			if available, why := c.VerificationSurface(); !available {
				why = strings.TrimSpace(why)
				if why == "" {
					why = "nothing in the workspace can run a build or test"
				}
				reason := "no check applies to this workspace: " + why
				if confirmed {
					return verificationEndUnverified, reason
				}
				return verificationConfirm, reason
			}
		}
		// The streak counts only attempts made since the latest workspace
		// change, so a model that kept working after refused commands has not
		// yet tried to verify its newest change.
		if c.MaxVerificationUnavailable > 0 && snapshot.VerificationUnavailableStreak >= c.MaxVerificationUnavailable {
			why := snapshot.lastUnavailableReason
			if why == "" {
				why = "the check did not run"
			}
			return verificationEndUnverified, fmt.Sprintf("%d verification attempts in a row could not run, last reason: %s", snapshot.VerificationUnavailableStreak, why)
		}
	case CompletionFailedPostChangeVerification:
		if c.MaxVerificationStalls > 0 && snapshot.VerificationFailureStreak >= c.MaxVerificationStalls {
			return verificationEndStalled, fmt.Sprintf("verification failed %d times in a row with no workspace change between runs", snapshot.VerificationFailureStreak)
		}
	default:
		return verificationContinue, ""
	}
	if c.MaxVerificationContinuations > 0 && rejections >= c.MaxVerificationContinuations {
		return verificationEndStalled, fmt.Sprintf("verification was still unsettled after %d continuations: %s", rejections, contractErr.Detail)
	}
	return verificationContinue, ""
}

// verificationNudge returns a short note for the tool result that pushed a
// verification streak to one step below its limit, so the model changes course
// before the run ends.
func (c CompletionContract) verificationNudge(snapshot ProgressSnapshot, unavailable bool) string {
	if unavailable {
		if c.MaxVerificationUnavailable > 1 && snapshot.VerificationUnavailableStreak == c.MaxVerificationUnavailable-1 {
			return fmt.Sprintf("Harness notice: %d verification attempts in a row did not run. Try one accepted command, such as go test ./..., make check, npm test, cargo test, or pytest. If none applies to this workspace, finish with a report that says the change is unverified and why; the run then ends as completed_unverified.", snapshot.VerificationUnavailableStreak)
		}
		return ""
	}
	if c.MaxVerificationStalls > 1 && snapshot.VerificationFailureStreak == c.MaxVerificationStalls-1 {
		return fmt.Sprintf("Harness notice: this verification failed %d times in a row with no workspace change between runs. Change the code or the check before running it again; if it fails once more without a change, the run ends as verification_stalled.", snapshot.VerificationFailureStreak)
	}
	return ""
}

// isVerificationContractError reports whether err rejected an answer for
// missing or failed post-change verification.
func isVerificationContractError(err error) bool {
	var contractErr *CompletionContractError
	if !errors.As(err, &contractErr) {
		return false
	}
	return contractErr.Reason == CompletionMissingPostChangeVerification || contractErr.Reason == CompletionFailedPostChangeVerification
}
