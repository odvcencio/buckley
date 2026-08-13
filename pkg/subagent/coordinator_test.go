package subagent

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestCoordinator_SpawnThreadsResolvedPersonaContract(t *testing.T) {
	requests := make(chan Request, 1)
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		started(42)
		requests <- request
		return "complete", nil
	}), 2)
	manager.SetPersonaContext(persona.NewRegistry(), persona.Persona{Name: "root", Tier: persona.TierReason})
	manager.personas.Add(persona.Persona{
		Name:         "worker",
		Model:        "sonnet",
		Prompt:       "Use the worker protocol.",
		AllowedTools: []string{"read_file"},
		StepCap:      5,
	})
	t.Cleanup(func() { _ = manager.Close() })

	coordinator := NewCoordinator(manager)
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-persona",
		ID:              "task-persona",
		ParentRunID:     "run-root",
		ParentSessionID: "session-1",
		Agent:           "reviewer",
		Task:            "inspect this",
		Persona:         "worker",
		Model:           "should-be-overridden",
		Tier:            "reason",
		SystemPrompt:    "should-be-overridden",
		AllowedTools:    []string{"read_file", "write_file"},
		StepCap:         20,
		Effort:          "high",
		WorkspaceClaims: []string{"pkg/subagent"},
		Isolation:       "worktree",
		OutputSchema:    "buckley.artifact/v1",
		ApprovalPosture: "safe",
		TimeoutSeconds:  40,
		Budget: agentcoord.Budget{
			MaxToolCalls:     13,
			MaxModelRequests: 8,
			MaxElapsedSecond: 35,
			MaxCostUSD:       0.75,
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if run.ID != "run-persona" || run.ParentRunID != "run-root" || run.ParentSessionID != "session-1" {
		t.Fatalf("run identity = %+v", run)
	}

	select {
	case request := <-requests:
		if request.ID != "run-persona" || request.TaskID != "task-persona" || request.ParentRunID != "run-root" {
			t.Fatalf("request identity = %+v", request)
		}
		if request.Model != "sonnet" || request.Tier != persona.TierExecute || request.SystemPrompt != "Use the worker protocol." || request.StepCap != 5 {
			t.Fatalf("resolved persona contract = %+v", request)
		}
		if got := strings.Join(request.AllowedTools, ","); got != "read_file" {
			t.Fatalf("AllowedTools = %q, want read_file", got)
		}
		if request.Effort != "high" || request.Isolation != "worktree" || request.OutputSchema != "buckley.artifact/v1" || request.ApprovalPosture != "safe" {
			t.Fatalf("execution constraints = %+v", request)
		}
		if request.TimeoutSeconds != 40 || request.Budget.MaxToolCalls != 13 || request.Budget.MaxModelRequests != 8 || request.Budget.MaxElapsedSecond != 35 || request.Budget.MaxCostUSD != 0.75 {
			t.Fatalf("execution limits = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner request")
	}
	if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCoordinator_AdmissionHardMaximaBoundOmittedTaskLimits(t *testing.T) {
	requests := make(chan Request, 1)
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, _ func(int)) (string, error) {
		requests <- request
		return "done", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithAdmissionPolicy(AdmissionPolicyFunc(func(context.Context, agentcoord.AgentTaskSpec) (AdmissionDecision, error) {
		return AdmissionDecision{Allowed: true, TimeoutSeconds: 120, StepCap: 15}, nil
	})))

	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-unbounded", Task: "inspect"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case request := <-requests:
		if request.TimeoutSeconds != 120 || request.StepCap != 15 || request.Budget.MaxElapsedSecond != 120 || request.Budget.MaxModelRequests != 15 {
			t.Fatalf("admission maxima did not bound omitted child limits: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner request")
	}
	if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCoordinator_RejectsNonFiniteCostBeforeAdmissionOrLaunch(t *testing.T) {
	launched := false
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		launched = true
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	admitted := false
	coordinator := NewCoordinator(manager, WithAdmissionPolicy(AdmissionPolicyFunc(func(context.Context, agentcoord.AgentTaskSpec) (AdmissionDecision, error) {
		admitted = true
		return AdmissionDecision{Allowed: true}, nil
	})))

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
			Task:   "inspect",
			Budget: agentcoord.Budget{MaxCostUSD: value},
		})
		if err == nil || !strings.Contains(err.Error(), "max_cost_usd must be finite") {
			t.Fatalf("Spawn(MaxCostUSD=%v) error = %v", value, err)
		}
	}
	if admitted || launched {
		t.Fatalf("invalid budget crossed boundary: admitted=%t launched=%t", admitted, launched)
	}
}

func TestCoordinator_ClaimsBlockOverlapBeforeSecondWorkerStarts(t *testing.T) {
	started := make(chan struct{}, 1)
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, startedPID func(int)) (string, error) {
		startedPID(7)
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager)

	first, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-first",
		ParentSessionID: "session-claims",
		Task:            "change pkg",
		WorkspaceClaims: []string{"pkg"},
	})
	if err != nil {
		t.Fatalf("spawn first: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	_, err = coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-second",
		ParentSessionID: "session-claims",
		Task:            "change subpackage",
		WorkspaceClaims: []string{"pkg/subagent"},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace claim conflict") {
		t.Fatalf("overlapping spawn error = %v, want claim conflict", err)
	}
	if _, err := coordinator.Cancel(context.Background(), first.ID, "test complete"); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if _, err := coordinator.Wait(context.Background(), first.ID); err != nil {
		t.Fatalf("wait first: %v", err)
	}
}

func TestCoordinator_SteerQueuesReadableMailbox(t *testing.T) {
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(9)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager)
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-mail", Task: "investigate"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	message, err := coordinator.Steer(context.Background(), run.ID, "focus on the failing search")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if message.Delivery != "queued" || message.Kind != "steer" {
		t.Fatalf("steer message = %+v", message)
	}
	messages, err := coordinator.Messages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "focus on the failing search" || messages[0].Delivery != "queued" {
		t.Fatalf("messages = %+v", messages)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "test complete")
}

func TestCoordinator_SendDeliversLiveAndPersistsDelivery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatalf("open evidence: %v", err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	received := make(chan agentcoord.Message, 1)
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(10)
		select {
		case delivery := <-commands:
			received <- delivery.Message
			delivery.Acknowledge(nil)
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-live-mail", ParentSessionID: "session-live-mail", Task: "investigate"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	message, err := coordinator.Send(context.Background(), agentcoord.Message{RunID: run.ID, To: run.ID, From: "parent", Kind: "message", Content: "check the hot path"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if message.Delivery != "delivered" {
		t.Fatalf("delivery = %q, want delivered", message.Delivery)
	}
	select {
	case got := <-received:
		if got.Content != "check the hot path" {
			t.Fatalf("received = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive runner did not receive command")
	}
	messages, err := coordinator.Messages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Delivery != "delivered" || messages[0].Content != "check the hot path" {
		t.Fatalf("messages = %+v", messages)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "test complete")
}

func TestCoordinator_DurableLifecycleStoresEvidenceAndSurvivesWorkerLoss(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatalf("open evidence: %v", err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, started func(int)) (string, error) {
		started(11)
		return "durable report", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-durable",
		ID:              "task-durable",
		ParentSessionID: "session-durable",
		ParentRunID:     "run-parent",
		Agent:           "reviewer",
		Task:            "review the patch",
		WorkspaceClaims: []string{"pkg/subagent"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	completed, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if completed.State != agentcoord.RunCompleted || completed.Result.Summary != "durable report" || len(completed.Result.EvidenceRefs) < 2 {
		t.Fatalf("completed run = %+v", completed)
	}
	var typedArtifact []byte
	for _, evidenceID := range completed.Result.EvidenceRefs {
		object, getErr := evidenceStore.Get(context.Background(), evidenceID)
		if getErr != nil || object.MediaType != artifactv1.MediaType {
			continue
		}
		typedArtifact = object.InlineBody
		break
	}
	if len(typedArtifact) == 0 {
		t.Fatalf("completed evidence = %v, want a typed artifact", completed.Result.EvidenceRefs)
	}
	artifact, _, err := artifactv1.DecodeProviderOutput(context.Background(), typedArtifact, artifactv1.OutputNativeJSONSchema, artifactv1.DecodeOptions{})
	if err != nil || artifact.Kind != artifactv1.KindSubagentResult || artifact.Status != artifactv1.StatusCompleted {
		t.Fatalf("stored subagent artifact = %+v, %v", artifact, err)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if durable.Status != string(agentcoord.RunCompleted) || durable.ParentRunID != "run-parent" {
		t.Fatalf("durable row = %+v", durable)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: run.ID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if !hasEvent(events, runledger.EventSubagentSpawned) || !hasEvent(events, runledger.EventSubagentCompleted) || !hasEvent(events, runledger.EventSubagentClaimed) || !hasEvent(events, runledger.EventSubagentReleased) {
		t.Fatalf("durable events = %+v", events)
	}

	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID:     "run-lost-worker",
		SessionID: "session-durable",
		Backend:   "local-process",
		Status:    "running",
	}); err != nil {
		t.Fatalf("seed lost-worker run: %v", err)
	}
	recovered := NewCoordinator(nil, WithRunLedger(ledger), WithEvidence(evidenceStore))
	lost, err := recovered.Status(context.Background(), "run-lost-worker")
	if err != nil {
		t.Fatalf("Status after worker loss: %v", err)
	}
	if lost.State != agentcoord.RunResumable {
		t.Fatalf("lost worker state = %q, want resumable", lost.State)
	}
	queued, err := recovered.Send(context.Background(), agentcoord.Message{
		RunID:   "run-lost-worker",
		To:      "run-lost-worker",
		From:    "parent",
		Kind:    "message",
		Content: "continue after reattach",
	})
	if err != nil {
		t.Fatalf("Send after worker loss: %v", err)
	}
	if queued.Delivery != "queued" {
		t.Fatalf("delivery = %q, want durable queued", queued.Delivery)
	}
	messages, err := recovered.Messages(context.Background(), "run-lost-worker")
	if err != nil || len(messages) != 1 || messages[0].Content != "continue after reattach" {
		t.Fatalf("recovered mailbox = %+v, %v", messages, err)
	}
}

func TestCoordinator_DurableReportRecoversOutputBeyondSnapshotPreview(t *testing.T) {
	root := t.TempDir()
	evidenceStore, err := evidence.New(filepath.Join(root, "large-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "MIDDLE-FINDING-MUST-SURVIVE"
	payload := strings.Repeat("a", 200*1024) + sentinel + strings.Repeat("z", 200*1024)
	spoolPath := filepath.Join(root, "subagent-report.log")
	if err := os.WriteFile(spoolPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(capturedRunnerFunc(func(context.Context, Request, func(int)) (CapturedOutput, error) {
		return CapturedOutput{
			Preview:       boundedOutput(payload),
			SpoolPath:     spoolPath,
			ObservedBytes: int64(len(payload)),
			CapturedBytes: int64(len(payload)),
			LimitBytes:    DefaultOutputSpoolLimit,
		}, nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))

	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-large-report",
		ID:              "task-large-report",
		ParentSessionID: "session-large-report",
		Agent:           "reviewer",
		Task:            "produce an exhaustive report",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != agentcoord.RunCompleted {
		t.Fatalf("completed = %+v", completed)
	}
	if strings.Contains(completed.Result.Summary, sentinel) {
		t.Fatal("test sentinel unexpectedly fit in the bounded snapshot preview")
	}
	found := false
	for _, evidenceID := range completed.Result.EvidenceRefs {
		object, getErr := evidenceStore.Get(context.Background(), evidenceID)
		if getErr == nil && object.MediaType == "application/json" && strings.Contains(string(object.InlineBody), sentinel) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("middle finding was absent from durable evidence refs %v", completed.Result.EvidenceRefs)
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("manager did not clean the consumed spool: %v", err)
	}
}

func hasEvent(events []runledger.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
