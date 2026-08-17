package replay

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
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

func putPartialModelResponse(t *testing.T, ctx context.Context, ev evidence.Store, providerError string) evidence.Object {
	t.Helper()
	body, err := modelstep.EncodeResponse(modelstep.ResponseEnvelope{
		Response: &model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "partial"}}},
			Usage:   model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		},
		ChargedCostUSD: 0.25,
		CostRecorded:   true,
		Partial:        true,
		ProviderError:  providerError,
	})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: body})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func blockPartialModelStep(t *testing.T, ctx context.Context, ledger runledger.BlockingStepJournal, step runledger.ExecutionStep, providerError, evidenceID, outputDigest string) {
	t.Helper()
	marker, err := modelstep.EncodeBlockedMarker(modelstep.BlockedMarker{
		Incomplete:         true,
		ProviderError:      providerError,
		DurabilityError:    "response completion persistence failed",
		ResponseEvidenceID: evidenceID,
		OutputDigest:       outputDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BlockStep(ctx, step, marker, evidenceID, outputDigest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
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
		{Type: runledger.EventModelRequestPlanned, TaskID: "task-1", Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "request_evidence_id": input.ID}},
		{Type: runledger.EventModelRequestCompleted, TaskID: "task-1", Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "response_evidence_id": output.ID}, EvidenceIDs: []string{output.ID}},
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

func TestVerify_ReportsExecutionStepsWithoutLedgerEvents(t *testing.T) {
	for _, tt := range []struct {
		name       string
		transition func(context.Context, *testing.T, *runledger.SQLiteStore, runledger.ExecutionStep)
	}{
		{name: "claimed"},
		{name: "dispatched", transition: func(ctx context.Context, t *testing.T, ledger *runledger.SQLiteStore, step runledger.ExecutionStep) {
			t.Helper()
			if err := ledger.MarkStepDispatched(ctx, step, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "blocked", transition: func(ctx context.Context, t *testing.T, ledger *runledger.SQLiteStore, step runledger.ExecutionStep) {
			t.Helper()
			if err := ledger.BlockStep(ctx, step, "operator reconciliation required", "", "", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "completed", transition: func(ctx context.Context, t *testing.T, ledger *runledger.SQLiteStore, step runledger.ExecutionStep) {
			t.Helper()
			if err := ledger.CompleteStepAttempt(ctx, step, "evidence-a", "digest-a", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "orphan-step"})
			if err != nil {
				t.Fatal(err)
			}
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: "orphan-" + tt.name, Kind: "tool", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			if tt.transition != nil {
				tt.transition(ctx, t, ledger, step)
			}

			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range report.Issues {
				found = found || issue.Code == "orphan_step_record" && issue.StepID == step.StepID
			}
			if report.Valid || !report.StepEnumerationComplete || report.StepCount != 1 || !found {
				t.Fatalf("report=%+v, want enumerated orphan step failure", report)
			}
		})
	}
}

func TestVerify_ReportsMultipleOrphanStepsInEnumeratorOrder(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := t.Context()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "ordered-orphans"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stepID := range []string{"step-c", "step-a", "step-b"} {
		if _, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "tool", InputDigest: "digest-" + stepID}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var orphanIDs []string
	for _, issue := range report.Issues {
		if issue.Code == "orphan_step_record" {
			orphanIDs = append(orphanIDs, issue.StepID)
		}
	}
	want := []string{"step-a", "step-b", "step-c"}
	if !slices.Equal(orphanIDs, want) {
		t.Fatalf("orphan issue order=%v, want %v", orphanIDs, want)
	}
}

func TestVerify_LegacyStepJournalWithoutEnumerationIsExplicitlyPartial(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := t.Context()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "partial-step-enumeration"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: "invisible-to-legacy-adapter", Kind: "tool", InputDigest: "digest-a"}); err != nil {
		t.Fatal(err)
	}

	report, err := Verify(ctx, ledger, legacyReplayStepJournal{StepJournal: ledger}, ev, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range report.Issues {
		found = found || issue.Code == "step_enumeration_unavailable" && issue.Severity == SeverityWarning
	}
	if !report.Valid || report.StepEnumerationComplete || report.StepCount != 0 || !found {
		t.Fatalf("report=%+v, want explicit partial-verification warning", report)
	}
}

type legacyReplayStepJournal struct {
	runledger.StepJournal
}

func TestVerify_StepEventProjectionMustMatchJournal(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mutate    func(map[string]any)
		wantIssue string
	}{
		{name: "input digest", mutate: func(payload map[string]any) { payload["input_digest"] = "digest-b" }, wantIssue: "step_input_digest_mismatch"},
		{name: "attempt", mutate: func(payload map[string]any) { payload["attempt"] = 2 }, wantIssue: "step_attempt_mismatch"},
		{name: "missing attempt", mutate: func(payload map[string]any) { delete(payload, "attempt") }, wantIssue: "missing_step_attempt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "projection-corruption"})
			if err != nil {
				t.Fatal(err)
			}
			output, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: []byte(`{"choices":[]}`)})
			if err != nil {
				t.Fatal(err)
			}
			stepID := "run/task/turn/round-001/model-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.CompleteStepAttempt(ctx, step, output.ID, output.ContentSHA256, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "response_evidence_id": output.ID}
			tt.mutate(payload)
			if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, Type: runledger.EventModelRequestCompleted, Payload: payload, EvidenceIDs: []string{output.ID}}); err != nil {
				t.Fatal(err)
			}
			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range report.Issues {
				found = found || issue.Code == tt.wantIssue
			}
			if report.Valid || !found {
				t.Fatalf("report=%+v, want %s", report, tt.wantIssue)
			}
		})
	}
}

func TestVerify_OutputEvidenceProjectionMustMatchJournal(t *testing.T) {
	projections := []struct {
		name            string
		payloadEvidence string
		listEvidence    []string
		valid           bool
	}{
		{name: "matching payload and list", payloadEvidence: "first", listEvidence: []string{"first"}, valid: true},
		{name: "payload points elsewhere", payloadEvidence: "second", listEvidence: []string{"second"}},
		{name: "payload A list A B", payloadEvidence: "first", listEvidence: []string{"first", "second"}},
		{name: "payload A list B A", payloadEvidence: "first", listEvidence: []string{"second", "first"}},
		{name: "no payload matching list", listEvidence: []string{"first"}, valid: true},
		{name: "no payload list A B", listEvidence: []string{"first", "second"}},
		{name: "no payload list B A", listEvidence: []string{"second", "first"}},
		{name: "no payload or list"},
	}
	for _, tt := range []struct {
		name         string
		eventType    string
		stepKind     string
		evidenceKind evidence.Kind
		payloadKey   string
		retryBlocked bool
	}{
		{name: "model completed", eventType: runledger.EventModelRequestCompleted, stepKind: "model", evidenceKind: evidence.KindModelResponse, payloadKey: "response_evidence_id"},
		{name: "model replayed", eventType: runledger.EventModelRequestReplayed, stepKind: "model", evidenceKind: evidence.KindModelResponse, payloadKey: "response_evidence_id"},
		{name: "tool completed", eventType: runledger.EventToolCompleted, stepKind: "tool", evidenceKind: evidence.KindToolResult, payloadKey: "output_evidence_id"},
		{name: "tool replayed", eventType: runledger.EventToolReplayed, stepKind: "tool", evidenceKind: evidence.KindToolResult, payloadKey: "output_evidence_id"},
		{name: "retry blocked", eventType: runledger.EventModelRequestReplayed, stepKind: "model", evidenceKind: evidence.KindModelResponse, payloadKey: "response_evidence_id", retryBlocked: true},
	} {
		for _, projection := range projections {
			t.Run(tt.name+"/"+projection.name, func(t *testing.T) {
				ledger, ev := newReplayStores(t)
				ctx := t.Context()
				run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "output-projection"})
				if err != nil {
					t.Fatal(err)
				}

				providerError := "provider failed after partial response"
				firstBody := []byte(`{"choices":[]}`)
				secondBody := []byte(`{"choices":[{"message":{"role":"assistant","content":"other"}}]}`)
				if tt.stepKind == "tool" {
					firstBody = []byte(`{"content":"first","success":true}`)
					secondBody = []byte(`{"content":"second","success":true}`)
				} else if tt.retryBlocked {
					firstBody, err = modelstep.EncodeResponse(modelstep.ResponseEnvelope{
						Response:      &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "first"}}}},
						Partial:       true,
						ProviderError: providerError,
					})
					if err != nil {
						t.Fatal(err)
					}
					secondBody, err = modelstep.EncodeResponse(modelstep.ResponseEnvelope{
						Response:      &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "second"}}}},
						Partial:       true,
						ProviderError: providerError,
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				first, err := ev.Put(ctx, evidence.Object{Kind: tt.evidenceKind, MediaType: "application/json", InlineBody: firstBody})
				if err != nil {
					t.Fatal(err)
				}
				second, err := ev.Put(ctx, evidence.Object{Kind: tt.evidenceKind, MediaType: "application/json", InlineBody: secondBody})
				if err != nil {
					t.Fatal(err)
				}
				if first.ID == second.ID {
					t.Fatal("test evidence objects unexpectedly share an identity")
				}

				stepID := "run/task/turn/round-001/" + tt.stepKind + "-000"
				step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: tt.stepKind, InputDigest: "digest-a"})
				if err != nil {
					t.Fatal(err)
				}
				if tt.retryBlocked {
					blockPartialModelStep(t, ctx, ledger, step, providerError, first.ID, first.ContentSHA256)
				} else if err := ledger.CompleteStepAttempt(ctx, step, first.ID, first.ContentSHA256, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}

				payload := map[string]any{
					"step_id":      stepID,
					"attempt":      1,
					"input_digest": "digest-a",
				}
				if projection.payloadEvidence == "first" {
					payload[tt.payloadKey] = first.ID
				} else if projection.payloadEvidence == "second" {
					payload[tt.payloadKey] = second.ID
				}
				if tt.retryBlocked {
					payload["retry_blocked"] = true
					payload["provider_error"] = providerError
				}
				evidenceIDs := make([]string, 0, len(projection.listEvidence))
				for _, selected := range projection.listEvidence {
					if selected == "first" {
						evidenceIDs = append(evidenceIDs, first.ID)
					} else {
						evidenceIDs = append(evidenceIDs, second.ID)
					}
				}
				if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, Type: tt.eventType, Payload: payload, EvidenceIDs: evidenceIDs}); err != nil {
					t.Fatal(err)
				}

				report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if projection.valid {
					if !report.Valid {
						t.Fatalf("report=%+v, want matching projection to be valid", report)
					}
					return
				}
				wantIssue := "step_output_evidence_mismatch"
				if tt.retryBlocked {
					wantIssue = "retry_blocked_evidence"
				}
				found := false
				for _, issue := range report.Issues {
					found = found || issue.Code == wantIssue
				}
				if report.Valid || !found {
					t.Fatalf("report=%+v, want %s", report, wantIssue)
				}
			})
		}
	}
}

func TestVerify_ToolOutputEvidenceDigestMustMatchJournal(t *testing.T) {
	for _, eventType := range []string{runledger.EventToolCompleted, runledger.EventToolReplayed} {
		t.Run(eventType, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "output-digest"})
			if err != nil {
				t.Fatal(err)
			}
			first, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, MediaType: "application/json", InlineBody: []byte(`{"content":"first"}`)})
			if err != nil {
				t.Fatal(err)
			}
			second, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, MediaType: "application/json", InlineBody: []byte(`{"content":"second"}`)})
			if err != nil {
				t.Fatal(err)
			}
			stepID := "run/task/turn/round-001/tool-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "tool", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.CompleteStepAttempt(ctx, step, first.ID, second.ContentSHA256, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.Append(ctx, runledger.Event{
				RunID:       run.RunID,
				Type:        eventType,
				Payload:     map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "output_evidence_id": first.ID},
				EvidenceIDs: []string{first.ID},
			}); err != nil {
				t.Fatal(err)
			}
			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range report.Issues {
				found = found || issue.Code == "step_output_evidence_mismatch"
			}
			if report.Valid || !found {
				t.Fatalf("report=%+v, want step_output_evidence_mismatch", report)
			}
		})
	}
}

func TestVerify_RetryBlockedModelReplayAllowsOptionalEvidence(t *testing.T) {
	for _, withEvidence := range []bool{false, true} {
		name := "without evidence"
		if withEvidence {
			name = "with evidence"
		}
		t.Run(name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-blocked"})
			if err != nil {
				t.Fatal(err)
			}
			stepID := "run/task/turn/round-001/model-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, TaskID: "task-1", StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			providerError := "provider failed after partial response"
			var evidenceID, outputDigest string
			if withEvidence {
				obj := putPartialModelResponse(t, ctx, ev, providerError)
				evidenceID, outputDigest = obj.ID, obj.ContentSHA256
			}
			blockPartialModelStep(t, ctx, ledger, step, providerError, evidenceID, outputDigest)
			payload := map[string]any{
				"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "retry_blocked": true,
				"provider_error": providerError,
			}
			var evidenceIDs []string
			if evidenceID != "" {
				payload["response_evidence_id"] = evidenceID
				evidenceIDs = []string{evidenceID}
			}
			if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, TaskID: "task-1", Type: runledger.EventModelRequestReplayed, Payload: payload, EvidenceIDs: evidenceIDs}); err != nil {
				t.Fatal(err)
			}

			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Valid || report.StepCount != 1 {
				t.Fatalf("report = %+v", report)
			}
			wantEvidenceCount := 0
			if withEvidence {
				wantEvidenceCount = 1
			}
			if report.EvidenceCount != wantEvidenceCount {
				t.Fatalf("evidence count = %d, want %d", report.EvidenceCount, wantEvidenceCount)
			}
		})
	}
}

func TestVerify_RetryBlockedModelReplayCorruptionFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name      string
		corrupt   string
		wantIssue string
	}{
		{name: "wrong terminal status", corrupt: "status", wantIssue: "retry_blocked_record"},
		{name: "missing provider projection", corrupt: "provider", wantIssue: "retry_blocked_record"},
		{name: "event evidence mismatch", corrupt: "event_evidence", wantIssue: "retry_blocked_evidence"},
		{name: "step digest mismatch", corrupt: "digest", wantIssue: "retry_blocked_record"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-blocked-corrupt"})
			if err != nil {
				t.Fatal(err)
			}
			providerError := "provider failed"
			obj := putPartialModelResponse(t, ctx, ev, providerError)
			stepID := "run/task/turn/round-001/model-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, TaskID: "task-1", StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			outputDigest := obj.ContentSHA256
			if tt.corrupt == "digest" {
				outputDigest = "not-the-content-digest"
			}
			blockPartialModelStep(t, ctx, ledger, step, providerError, obj.ID, outputDigest)
			if tt.corrupt == "status" {
				if _, err := ev.DB().ExecContext(ctx, `UPDATE execution_steps SET status = ? WHERE run_id = ? AND step_id = ?`, runledger.StepCompleted, run.RunID, stepID); err != nil {
					t.Fatal(err)
				}
			}
			if tt.corrupt == "provider" {
				providerError = ""
			}
			eventEvidenceID := obj.ID
			if tt.corrupt == "event_evidence" {
				other, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: []byte(`{"other":true}`)})
				if err != nil {
					t.Fatal(err)
				}
				eventEvidenceID = other.ID
			}
			payload := map[string]any{
				"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "retry_blocked": true,
				"provider_error": providerError, "response_evidence_id": eventEvidenceID,
			}
			if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, TaskID: "task-1", Type: runledger.EventModelRequestReplayed, Payload: payload, EvidenceIDs: []string{eventEvidenceID}}); err != nil {
				t.Fatal(err)
			}

			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if report.Valid {
				t.Fatalf("report = %+v, want invalid", report)
			}
			found := false
			for _, issue := range report.Issues {
				if issue.Code == tt.wantIssue {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %+v, want %s", report.Issues, tt.wantIssue)
			}
		})
	}
}

func TestVerify_BlockedModelStepValidatedBeforeReplayEvent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		corrupt string
	}{
		{name: "valid"},
		{name: "marker", corrupt: "marker"},
		{name: "evidence shape", corrupt: "evidence"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "blocked-before-replay"})
			if err != nil {
				t.Fatal(err)
			}
			providerError := "provider failed"
			object := putPartialModelResponse(t, ctx, ev, providerError)
			if tt.corrupt == "evidence" {
				object, err = ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, MediaType: "application/json", InlineBody: object.InlineBody})
				if err != nil {
					t.Fatal(err)
				}
			}
			stepID := "run/task/turn/round-001/model-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			marker, err := modelstep.EncodeBlockedMarker(modelstep.BlockedMarker{Incomplete: true, ProviderError: providerError, DurabilityError: "completion persistence failed", ResponseEvidenceID: object.ID, OutputDigest: object.ContentSHA256})
			if err != nil {
				t.Fatal(err)
			}
			if tt.corrupt == "marker" {
				marker = modelstep.BlockedMarkerPrefix + "{"
			}
			if err := ledger.BlockStep(ctx, step, marker, object.ID, object.ContentSHA256, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			for _, eventType := range []string{runledger.EventModelRequestPlanned, runledger.EventModelRequestStarted} {
				if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, Type: eventType, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a"}}); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := ledger.GetStep(ctx, run.RunID, stepID)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := ev.Get(ctx, object.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, controllerValidationErr := modelstep.ValidateBlockedReplay(stored, &loaded)
			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if (controllerValidationErr == nil) != report.Valid {
				t.Fatalf("controller validation=%v report=%+v", controllerValidationErr, report)
			}
			if tt.corrupt != "" {
				count := 0
				for _, issue := range report.Issues {
					if issue.Code == "blocked_model_record" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("issues=%+v, want one deduplicated blocked_model_record", report.Issues)
				}
			}
		})
	}
}

func TestVerify_AttemptHistoryAllowsEarlierNonterminalEvents(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mutate    func([]runledger.Event)
		wantIssue string
	}{
		{name: "valid history"},
		{name: "future attempt", mutate: func(events []runledger.Event) { events[0].Payload["attempt"] = 3 }, wantIssue: "step_attempt_mismatch"},
		{name: "zero attempt", mutate: func(events []runledger.Event) { events[0].Payload["attempt"] = 0 }, wantIssue: "missing_step_attempt"},
		{name: "stale terminal projection", mutate: func(events []runledger.Event) { events[len(events)-1].Payload["attempt"] = 1 }, wantIssue: "step_attempt_mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "attempt-history"})
			if err != nil {
				t.Fatal(err)
			}
			stepID := "run/task/turn/round-001/model-000"
			first, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.FailStepAttempt(ctx, first, "predispatch failure", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			second, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			output, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: []byte(`{"choices":[]}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.CompleteStepAttempt(ctx, second, output.ID, output.ContentSHA256, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			events := []runledger.Event{
				{Type: runledger.EventModelRequestPlanned, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestStarted, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestFailed, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestPlanned, Payload: map[string]any{"step_id": stepID, "attempt": 2, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestStarted, Payload: map[string]any{"step_id": stepID, "attempt": 2, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestCompleted, Payload: map[string]any{"step_id": stepID, "attempt": 2, "input_digest": "digest-a", "response_evidence_id": output.ID}, EvidenceIDs: []string{output.ID}},
			}
			if tt.mutate != nil {
				tt.mutate(events)
			}
			for _, event := range events {
				event.RunID = run.RunID
				if _, err := ledger.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantIssue == "" {
				if !report.Valid {
					t.Fatalf("report=%+v", report)
				}
				return
			}
			found := false
			for _, issue := range report.Issues {
				found = found || issue.Code == tt.wantIssue
			}
			if report.Valid || !found {
				t.Fatalf("report=%+v, want %s", report, tt.wantIssue)
			}
		})
	}
}

func TestVerify_PricingErrorUsesSharedCanonicalEnvelope(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		name := "noncanonical"
		if canonical {
			name = "canonical"
		}
		t.Run(name, func(t *testing.T) {
			ledger, ev := newReplayStores(t)
			ctx := t.Context()
			run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "pricing-envelope"})
			if err != nil {
				t.Fatal(err)
			}
			rawPricingError := "pricing failed sk-abcdefghijklmnopqrstuvwxyz1234"
			envelope := modelstep.ResponseEnvelope{
				Version:      modelstep.ResponseVersion,
				Response:     &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "response"}}}},
				PricingError: rawPricingError,
			}
			var body []byte
			if canonical {
				envelope.PricingError = modelstep.NormalizeErrorText(rawPricingError)
				body, err = modelstep.EncodeResponse(envelope)
			} else {
				body, err = json.Marshal(envelope)
			}
			if err != nil {
				t.Fatal(err)
			}
			object, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: body})
			if err != nil {
				t.Fatal(err)
			}
			stepID := "run/task/turn/round-001/model-000"
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "model", InputDigest: "digest-a"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.CompleteStepAttempt(ctx, step, object.ID, object.ContentSHA256, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			for _, event := range []runledger.Event{
				{Type: runledger.EventModelRequestPlanned, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a"}},
				{Type: runledger.EventModelRequestCompleted, Payload: map[string]any{"step_id": stepID, "attempt": 1, "input_digest": "digest-a", "response_evidence_id": object.ID}, EvidenceIDs: []string{object.ID}},
			} {
				event.RunID = run.RunID
				if _, err := ledger.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			_, controllerValidationErr := modelstep.ValidateResponseEvidence(object.ID, object.ContentSHA256, object)
			report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if (controllerValidationErr == nil) != report.Valid {
				t.Fatalf("controller validation=%v report=%+v", controllerValidationErr, report)
			}
		})
	}
}

func TestVerify_SequenceGapFailsClosed(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := context.Background()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for round := range 3 {
		if _, err := ledger.Append(ctx, runledger.Event{
			RunID:   run.RunID,
			Type:    runledger.EventModelRequestStarted,
			TaskID:  "task-1",
			Payload: map[string]any{"step_id": "run/task/turn/round-001/model-000", "input_digest": "digest-a"},
		}); err != nil {
			t.Fatalf("Append round %d: %v", round, err)
		}
	}
	if _, err := ev.DB().ExecContext(ctx, `
		DELETE FROM run_events WHERE run_id = ? AND sequence = (
			SELECT MIN(sequence) + 1 FROM run_events WHERE run_id = ?
		)`, run.RunID, run.RunID); err != nil {
		t.Fatalf("delete middle event: %v", err)
	}

	report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Valid {
		t.Fatalf("report = %+v, want invalid report for sequence gap", report)
	}
	var found bool
	for _, issue := range report.Issues {
		if issue.Code == "sequence_gap" && issue.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want sequence_gap error", report.Issues)
	}
	if len(report.SequenceGaps) == 0 {
		t.Fatalf("SequenceGaps = %v, want the missing sequence listed", report.SequenceGaps)
	}
}

func TestVerify_LegacyEventsWarnAndRemainInspectable(t *testing.T) {
	ledger, ev := newReplayStores(t)
	ctx := context.Background()
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "replay-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// A pre-Phase-0 ledger recorded completed model and tool events with no
	// step metadata and no evidence references.
	for _, eventType := range []string{runledger.EventModelRequestCompleted, runledger.EventToolCompleted} {
		if _, err := ledger.Append(ctx, runledger.Event{
			RunID:   run.RunID,
			Type:    eventType,
			TaskID:  "task-1",
			Payload: map[string]any{"model": "legacy"},
		}); err != nil {
			t.Fatalf("Append %s: %v", eventType, err)
		}
	}

	report, err := Verify(ctx, ledger, ledger, ev, run.RunID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Valid {
		t.Fatalf("report = %+v, want legacy ledger to stay inspectable", report)
	}
	warnings := 0
	for _, issue := range report.Issues {
		if issue.Severity != SeverityWarning {
			t.Fatalf("issue = %+v, want warnings only for legacy events", issue)
		}
		if issue.Code == "legacy_step_event" {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("issues = %+v, want two legacy_step_event warnings", report.Issues)
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
