package agentloop

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseTaskIntent(t *testing.T) {
	tests := []struct {
		value   string
		want    TaskIntent
		wantErr bool
	}{
		{value: "", want: UnknownIntent},
		{value: "unknown", want: UnknownIntent},
		{value: "read_only", want: ReadOnlyIntent},
		{value: "read-only", want: ReadOnlyIntent},
		{value: "readonly", want: ReadOnlyIntent},
		{value: "mutation", want: MutationIntent},
		{value: "mutate", want: MutationIntent},
		{value: "write", want: MutationIntent},
		{value: "question", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseTaskIntent(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTaskIntent(%q) error = nil", tt.value)
				}
				if !strings.Contains(err.Error(), "unknown, read_only, or mutation") {
					t.Fatalf("ParseTaskIntent(%q) error = %v", tt.value, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseTaskIntent(%q) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
}

func TestPresentIncompleteResult_CodeOnlyContractReason(t *testing.T) {
	notice := PresentIncompleteResult(&IncompleteTurnError{
		Code: string(CompletionMissingObservableChange),
	})
	if notice.Code != string(CompletionMissingObservableChange) {
		t.Fatalf("Code = %q", notice.Code)
	}
	if !strings.Contains(notice.Reason, "observable workspace change") {
		t.Fatalf("Reason = %q, want code-derived reason", notice.Reason)
	}
	if !strings.Contains(notice.NextAction, "make the requested change") {
		t.Fatalf("NextAction = %q, want code-derived action", notice.NextAction)
	}
}

func TestPresentIncompleteResult_ToolRoundInterruptedGuidesReconciliation(t *testing.T) {
	notice := PresentIncompleteResult(&IncompleteTurnError{
		Code:   IncompleteToolRoundInterrupted,
		Reason: "tool execution stopped after a callback, persistence, or observer path left outcome evidence unresolved",
	})
	if notice.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("Code = %q", notice.Code)
	}
	if strings.Contains(notice.Message, "context canceled") || strings.Contains(notice.Message, "observer failed") {
		t.Fatalf("Message leaked raw cause text: %q", notice.Message)
	}
	if !strings.Contains(notice.NextAction, "Inspect and reconcile") || strings.Contains(strings.ToLower(notice.NextAction), "blind") {
		t.Fatalf("NextAction = %q, want inspect/reconcile guidance", notice.NextAction)
	}
}

func TestPresentIncompleteResult_SanitizesHostileProviderText(t *testing.T) {
	err := &IncompleteTurnError{
		Code:              "Model Error!!\n<script>" + strings.Repeat("Z", 120),
		FinalizationError: fmt.Sprintf("provider exploded\n日本語日本語\n%s\nsecret-looking-token", strings.Repeat("x", 600)),
	}
	notice := PresentIncompleteResult(err)
	if !strings.HasPrefix(notice.Code, "modelerrorscript") || len(notice.Code) > 64 || strings.Contains(notice.Code, ".") {
		t.Fatalf("Code = %q, want sanitized compact code", notice.Code)
	}
	for _, field := range []string{notice.Reason, notice.NextAction, notice.Message} {
		if strings.ContainsAny(field, "\n\r\t") {
			t.Fatalf("notice field retained control whitespace: %q", field)
		}
		if !utf8.ValidString(field) {
			t.Fatalf("notice field is not valid UTF-8: %q", field)
		}
	}
	if utf8.RuneCountInString(notice.Reason) > 240 {
		t.Fatalf("Reason length = %d, want <= 240", utf8.RuneCountInString(notice.Reason))
	}
	if utf8.RuneCountInString(notice.NextAction) > 180 {
		t.Fatalf("NextAction length = %d, want <= 180", utf8.RuneCountInString(notice.NextAction))
	}
	if !strings.HasPrefix(notice.Message, "Incomplete result: ") || !strings.Contains(notice.Message, "Next: ") {
		t.Fatalf("Message = %q", notice.Message)
	}
}

func TestCompletionContract_ReadOnlyIntent(t *testing.T) {
	tests := []struct {
		name     string
		snapshot ProgressSnapshot
		wantErr  bool
	}{
		{
			name: "read_only answer without mutation is accepted",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        0,
				StateChangedCalls:         0,
				VerificationObservedCalls: 0,
			},
			wantErr: false,
		},
		{
			name: "read_only answer with mutation and no verification is rejected",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				VerificationObservedCalls: 0,
				LastStateChangeSequence:   1,
			},
			wantErr: true,
		},
		{
			name: "read_only answer with mutation and verification is accepted",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				LastStateChangeSequence:   1,
				LastVerificationSequence:  2,
				LastVerificationPassed:    true,
				VerificationObservedCalls: 1,
				VerificationPassedCalls:   1,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := CompletionContract{
				TaskIntent:                    ReadOnlyIntent,
				RequirePostChangeVerification: true,
				RequireObservableChange:       false,
			}
			err := contract.Validate(tt.snapshot)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompletionContract_RepairInstructionForReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		contains string
		absent   string
	}{
		{
			name: "missing mutation",
			err: &CompletionContractError{
				Reason: CompletionMissingObservableChange,
				Detail: "task requires observable workspace change but no mutations were recorded",
			},
			contains: "requires an observable workspace change",
			absent:   "workspace changed after",
		},
		{
			name: "failed verification",
			err: &CompletionContractError{
				Reason: CompletionFailedPostChangeVerification,
				Detail: "latest verification after the final workspace change did not pass",
			},
			contains: "latest verification after the final workspace change failed",
		},
		{
			name: "missing verification",
			err: &CompletionContractError{
				Reason: CompletionMissingPostChangeVerification,
				Detail: "missing successful verification after the latest workspace change",
			},
			contains: "workspace changed after the last successful verification",
		},
		{
			name: "state observation failure",
			err: &CompletionContractError{
				Reason: CompletionStateObservationFailed,
				Detail: "workspace state could not be observed after a tool that may affect completion evidence",
			},
			contains: "could not observe workspace state",
		},
	}

	contract := CompletionContract{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contract.RepairInstructionFor(tt.err)
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("repair instruction = %q, want containing %q", got, tt.contains)
			}
			if tt.absent != "" && strings.Contains(got, tt.absent) {
				t.Fatalf("repair instruction = %q, must not contain %q", got, tt.absent)
			}
			var contractErr *CompletionContractError
			if !errors.As(tt.err, &contractErr) {
				t.Fatalf("test error is not typed: %v", tt.err)
			}
		})
	}
}

func TestCompletionContract_MutationIntent_WithObservableChange(t *testing.T) {
	tests := []struct {
		name     string
		snapshot ProgressSnapshot
		wantErr  bool
	}{
		{
			name: "mutation task with no change is rejected",
			snapshot: ProgressSnapshot{
				StateObservedCalls: 0,
				StateChangedCalls:  0,
			},
			wantErr: true,
		},
		{
			name: "mutation task with change is accepted",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				LastStateChangeSequence:   1,
				VerificationObservedCalls: 0,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := CompletionContract{
				TaskIntent:              MutationIntent,
				RequireObservableChange: true,
			}
			err := contract.Validate(tt.snapshot)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompletionContract_MutationIntent_WithPostChangeVerification(t *testing.T) {
	tests := []struct {
		name     string
		snapshot ProgressSnapshot
		wantErr  bool
	}{
		{
			name: "mutation with change but no verification is rejected",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				LastStateChangeSequence:   1,
				VerificationObservedCalls: 0,
			},
			wantErr: true,
		},
		{
			name: "mutation with change and passed verification is accepted",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				LastStateChangeSequence:   1,
				VerificationObservedCalls: 1,
				VerificationPassedCalls:   1,
				LastVerificationSequence:  2,
				LastVerificationPassed:    true,
			},
			wantErr: false,
		},
		{
			name: "state observation failure remains rejected after later observed change and verification",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				StateObservationFailures:  1,
				LastStateFailureSequence:  1,
				LastStateChangeSequence:   2,
				VerificationObservedCalls: 1,
				VerificationPassedCalls:   1,
				LastVerificationSequence:  3,
				LastVerificationPassed:    true,
			},
			wantErr: true,
		},
		{
			name: "mutation with change and failed verification is rejected",
			snapshot: ProgressSnapshot{
				StateObservedCalls:        1,
				StateChangedCalls:         1,
				LastStateChangeSequence:   1,
				VerificationObservedCalls: 1,
				VerificationPassedCalls:   0,
				LastVerificationSequence:  2,
				LastVerificationPassed:    false,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := CompletionContract{
				TaskIntent:                    MutationIntent,
				RequirePostChangeVerification: true,
				RequireObservableChange:       false,
			}
			err := contract.Validate(tt.snapshot)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompletionContract_UnknownIntent_FallbackBehavior(t *testing.T) {
	tests := []struct {
		name     string
		contract CompletionContract
		snapshot ProgressSnapshot
		wantErr  bool
	}{
		{
			name: "unknown intent with no requirements accepts all",
			contract: CompletionContract{
				TaskIntent:                    UnknownIntent,
				RequirePostChangeVerification: false,
				RequireObservableChange:       false,
			},
			snapshot: ProgressSnapshot{
				StateChangedCalls: 0,
			},
			wantErr: false,
		},
		{
			name: "unknown intent with RequireObservableChange rejects no-edit",
			contract: CompletionContract{
				TaskIntent:              UnknownIntent,
				RequireObservableChange: true,
			},
			snapshot: ProgressSnapshot{
				StateChangedCalls: 0,
			},
			wantErr: true,
		},
		{
			name: "unknown intent with RequirePostChangeVerification rejects missing verification",
			contract: CompletionContract{
				TaskIntent:                    UnknownIntent,
				RequirePostChangeVerification: true,
			},
			snapshot: ProgressSnapshot{
				StateChangedCalls:        1,
				LastStateChangeSequence:  1,
				LastVerificationSequence: 0,
			},
			wantErr: true,
		},
		{
			name: "unknown intent with RequirePostChangeVerification accepts with verification",
			contract: CompletionContract{
				TaskIntent:                    UnknownIntent,
				RequirePostChangeVerification: true,
			},
			snapshot: ProgressSnapshot{
				StateChangedCalls:        1,
				LastStateChangeSequence:  1,
				LastVerificationSequence: 2,
				LastVerificationPassed:   true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.contract.Validate(tt.snapshot)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompletionContract_Normalize(t *testing.T) {
	tests := []struct {
		name                  string
		contract              CompletionContract
		wantMaxRepairAttempts int
		wantRepairInstruction string
		wantTaskIntent        TaskIntent
	}{
		{
			name:                  "apply defaults",
			contract:              CompletionContract{},
			wantMaxRepairAttempts: 1,
			wantRepairInstruction: defaultCompletionRepairInstruction,
			wantTaskIntent:        UnknownIntent,
		},
		{
			name: "preserve non-zero MaxRepairAttempts",
			contract: CompletionContract{
				MaxRepairAttempts: 3,
			},
			wantMaxRepairAttempts: 3,
			wantRepairInstruction: defaultCompletionRepairInstruction,
			wantTaskIntent:        UnknownIntent,
		},
		{
			name: "preserve custom RepairInstruction",
			contract: CompletionContract{
				RepairInstruction: "custom repair",
			},
			wantMaxRepairAttempts: 1,
			wantRepairInstruction: "custom repair",
			wantTaskIntent:        UnknownIntent,
		},
		{
			name: "preserve TaskIntent",
			contract: CompletionContract{
				TaskIntent: ReadOnlyIntent,
			},
			wantMaxRepairAttempts: 1,
			wantRepairInstruction: defaultCompletionRepairInstruction,
			wantTaskIntent:        ReadOnlyIntent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := tt.contract.Normalize()
			if normalized.MaxRepairAttempts != tt.wantMaxRepairAttempts {
				t.Errorf("MaxRepairAttempts: got %d, want %d", normalized.MaxRepairAttempts, tt.wantMaxRepairAttempts)
			}
			if normalized.RepairInstruction != tt.wantRepairInstruction {
				t.Errorf("RepairInstruction: got %q, want %q", normalized.RepairInstruction, tt.wantRepairInstruction)
			}
			if normalized.TaskIntent != tt.wantTaskIntent {
				t.Errorf("TaskIntent: got %q, want %q", normalized.TaskIntent, tt.wantTaskIntent)
			}
		})
	}
}
