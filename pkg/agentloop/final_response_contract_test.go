package agentloop

import (
	"errors"
	"strings"
	"testing"
)

func TestFinalResponseContract(t *testing.T) {
	readOnlySnapshot := ProgressSnapshot{}
	mutationSnapshot := ProgressSnapshot{}

	t.Run("nil predicate preserves read-only acceptance", func(t *testing.T) {
		c := CompletionContract{TaskIntent: ReadOnlyIntent}.Normalize()
		if err := c.evaluateFinalResponse(readOnlySnapshot, "any answer"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("predicate sees exact response and accepts", func(t *testing.T) {
		var seen string
		c := CompletionContract{
			TaskIntent:            ReadOnlyIntent,
			ValidateFinalResponse: func(s string) error { seen = s; return nil },
		}.Normalize()
		if err := c.evaluateFinalResponse(readOnlySnapshot, "exact response"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if seen != "exact response" {
			t.Fatalf("predicate saw %q, want %q", seen, "exact response")
		}
	})

	t.Run("rejection has typed reason and actionable repair instruction", func(t *testing.T) {
		c := CompletionContract{
			TaskIntent:            ReadOnlyIntent,
			ValidateFinalResponse: func(string) error { return errors.New("missing citation") },
		}.Normalize()
		err := c.evaluateFinalResponse(readOnlySnapshot, "bad answer")
		var contractErr *CompletionContractError
		if !errors.As(err, &contractErr) {
			t.Fatalf("expected *CompletionContractError, got %v", err)
		}
		if contractErr.Reason != CompletionInvalidFinalResponse {
			t.Fatalf("reason = %q, want %q", contractErr.Reason, CompletionInvalidFinalResponse)
		}
		wantDetail := "required final response is invalid: missing citation"
		if contractErr.Detail != wantDetail {
			t.Fatalf("detail = %q, want %q", contractErr.Detail, wantDetail)
		}
		instruction := c.RepairInstructionFor(err)
		if !strings.Contains(instruction, wantDetail) {
			t.Fatalf("repair instruction %q missing detail %q", instruction, wantDetail)
		}
		if !strings.Contains(instruction, "do not invent results") {
			t.Fatalf("repair instruction %q missing no-invention guidance", instruction)
		}
	})

	t.Run("mutation progress failure skips predicate", func(t *testing.T) {
		called := false
		c := CompletionContract{
			TaskIntent:              MutationIntent,
			RequireObservableChange: true,
			ValidateFinalResponse:   func(string) error { called = true; return nil },
		}.Normalize()
		err := c.evaluateFinalResponse(mutationSnapshot, "answer")
		var contractErr *CompletionContractError
		if !errors.As(err, &contractErr) {
			t.Fatalf("expected *CompletionContractError, got %v", err)
		}
		if contractErr.Reason != CompletionMissingObservableChange {
			t.Fatalf("reason = %q, want %q", contractErr.Reason, CompletionMissingObservableChange)
		}
		if called {
			t.Fatal("predicate should not have been invoked")
		}
	})
}
