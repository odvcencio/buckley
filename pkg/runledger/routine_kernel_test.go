package runledger

import (
	"context"
	"errors"
	"testing"
)

func TestRunContract_IdempotentIdentityAndDigestFence(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	run := AgentRun{
		RunID: "run-contract", SessionID: "session-contract", ParentRunID: "run-parent",
		TaskID: "task-contract", AgentID: "worker", Backend: "local-process", Status: "queued",
	}
	first, created, err := store.EnsureRunContract(ctx, run, "digest-a", "evidence-a")
	if err != nil || !created || first.RunID != run.RunID {
		t.Fatalf("first contract = %+v, created=%t, err=%v", first, created, err)
	}
	second, created, err := store.EnsureRunContract(ctx, run, "digest-a", "evidence-a")
	if err != nil || created || second.RunID != run.RunID {
		t.Fatalf("duplicate contract = %+v, created=%t, err=%v", second, created, err)
	}
	if _, _, err := store.EnsureRunContract(ctx, run, "digest-b", "evidence-b"); !errors.Is(err, ErrRunContractConflict) {
		t.Fatalf("digest drift = %v, want ErrRunContractConflict", err)
	}
	changed := run
	changed.SessionID = "session-foreign"
	if _, _, err := store.EnsureRunContract(ctx, changed, "digest-a", "evidence-a"); !errors.Is(err, ErrRunContractConflict) {
		t.Fatalf("identity drift = %v, want ErrRunContractConflict", err)
	}
	contract, err := store.GetRunContract(ctx, run.RunID)
	if err != nil || contract.InputDigest != "digest-a" || contract.TaskEvidenceID != "evidence-a" {
		t.Fatalf("stored contract = %+v, %v", contract, err)
	}
}
