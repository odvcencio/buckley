package replay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func newReplayStores(t *testing.T) (*runledger.SQLiteStore, *evidence.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	return ledger, ev
}

func TestVerify_ReplayReadyRun(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := context.Background()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	stepID := "run/task/turn/round-001/model-000"
	input, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelRequest, MediaType: "application/json", InlineBody: []byte(`{"model":"test"}`)})
	if err != nil {
		t.Fatalf("store input evidence: %v", err)
	}
	output, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: []byte(`{"choices":[]}`)})
	if err != nil {
		t.Fatalf("store output evidence: %v", err)
	}
	if _, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, TaskID: "task-1", StepID: stepID, Kind: "model", InputDigest: "digest-a"}); err != nil {
		t.Fatalf("BeginStep: %v", err)
	}
	if err := ledger.CompleteStep(ctx, run.RunID, stepID, output.ID, output.ContentSHA256, time.Now().UTC()); err != nil {
		t.Fatalf("CompleteStep: %v", err)
	}
	for _, event := range []runledger.Event{
		{Type: runledger.EventModelRequestPlanned, TaskID: "task-1", Payload: map[string]any{"step_id": stepID, "input_digest": "digest-a", "request_evidence_id": input.ID}},
		{Type: runledger.EventModelRequestCompleted, TaskID: "task-1", Payload: map[string]any{"step_id": stepID, "input_digest": "digest-a", "response_evidence_id": output.ID}, EvidenceIDs: []string{output.ID}},
	} {
		event.RunID = run.RunID
		if _, err := ledger.Append(ctx, event); err != nil {
			t.Fatalf("Append %s: %v", event.Type, err)
		}
	}

	report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Valid || report.StepCount != 1 || report.EvidenceCount != 2 {
		t.Fatalf("report = %+v, want valid one-step report with two evidence objects", report)
	}
}

func TestVerify_MissingOutputEvidenceIsInvalid(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := context.Background()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := ledger.Append(ctx, runledger.Event{
		RunID:  run.RunID,
		Type:   runledger.EventModelRequestCompleted,
		TaskID: "task-1",
		Payload: map[string]any{
			"step_id":      "run/task/turn/round-001/model-000",
			"input_digest": "digest-a",
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Valid {
		t.Fatalf("report = %+v, want invalid missing-evidence report", report)
	}
	var found bool
	for _, issue := range report.Issues {
		if issue.Code == "missing_output_evidence" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want missing_output_evidence", report.Issues)
	}
}
