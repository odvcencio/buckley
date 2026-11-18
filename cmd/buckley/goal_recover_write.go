package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type goalWriteIntent struct {
	StepID          string
	Attempt         int
	ClaimGeneration int
	InputDigest     string
	Target          builtin.WriteFileIntent
}

func recordGoalWriteIntent(ctx context.Context, journal runledger.StepJournal, store evidence.Store, runID, stepID string, target builtin.WriteFileIntent) error {
	step, err := journal.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if step.Status != runledger.StepStarted || step.DispatchState != runledger.StepDispatchDispatched {
		return fmt.Errorf("write step %s is no longer active", stepID)
	}
	_, err = putGoalStepEvidence(ctx, store, step, evidence.KindToolIntent, goalWriteIntent{
		StepID: stepID, Attempt: step.Attempt, ClaimGeneration: step.ClaimGeneration, InputDigest: step.InputDigest, Target: target,
	})
	return err
}

func putGoalStepEvidence(ctx context.Context, store evidence.Store, step runledger.ExecutionStep, kind evidence.Kind, value any) (evidence.Object, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return evidence.Object{}, err
	}
	return store.Put(ctx, evidence.Object{Kind: kind, MediaType: "application/json", InlineBody: body,
		Metadata: map[string]any{evidence.MetaRunID: step.RunID, evidence.MetaTaskID: step.TaskID, evidence.MetaEntityID: step.StepID},
	})
}

func runGoalRecoverWrite(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: buckley goal recover-write <run-id> <step-id>")
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		return err
	}
	defer cleanup()
	obj, err := recoverGoalWrite(context.Background(), stores.ledger, stores.evidence, args[0], args[1])
	if err != nil {
		return err
	}
	fmt.Printf("Write receipt %s for step %s\n", obj.ID, args[1])
	return nil
}

func recoverGoalWrite(ctx context.Context, ledger *runledger.SQLiteStore, store evidence.Store, runID, stepID string) (evidence.Object, error) {
	step, err := ledger.GetStep(ctx, runID, stepID)
	if err != nil {
		return evidence.Object{}, err
	}
	if step.Kind != "tool" || step.DispatchState != runledger.StepDispatchDispatched {
		return evidence.Object{}, fmt.Errorf("step %s is not a dispatched tool", stepID)
	}
	candidates, err := store.Query(ctx, evidence.Query{Kinds: []evidence.Kind{evidence.KindToolIntent}, RunID: runID, EntityID: stepID, Limit: 2})
	if err != nil {
		return evidence.Object{}, err
	}
	if len(candidates) != 1 {
		return evidence.Object{}, fmt.Errorf("step %s has no unique recorded file write intent", stepID)
	}
	obj, err := store.Get(ctx, candidates[0].ID)
	if err != nil {
		return evidence.Object{}, err
	}
	var intent goalWriteIntent
	if err := json.Unmarshal(obj.InlineBody, &intent); err != nil {
		return evidence.Object{}, err
	}
	if intent.StepID != stepID || intent.Attempt != step.Attempt || intent.ClaimGeneration != step.ClaimGeneration ||
		intent.InputDigest != step.InputDigest || intent.Target.After == "" || intent.Target.Before == intent.Target.After {
		return evidence.Object{}, fmt.Errorf("step %s write intent does not match the current attempt", stepID)
	}
	if step.Status == runledger.StepCompleted {
		return store.Get(ctx, step.OutputEvidenceID)
	}
	current, err := intent.Target.CurrentDigest()
	if err != nil {
		return evidence.Object{}, fmt.Errorf("inspect write target: %w", err)
	}
	if current != intent.Target.After {
		if current == intent.Target.Before {
			return evidence.Object{}, fmt.Errorf("step %s still has the pre-write content; write remains unresolved", stepID)
		}
		return evidence.Object{}, fmt.Errorf("step %s has different file content; write remains unresolved", stepID)
	}
	receipt, err := putGoalStepEvidence(ctx, store, step, evidence.KindToolResult, struct {
		StepID string `json:"step_id"`
		agentloop.ToolOutcome
	}{
		StepID: stepID,
		ToolOutcome: agentloop.ToolOutcome{
			Content: fmt.Sprintf("Recovered write receipt for %s from matching workspace content.", intent.Target.Path),
			Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true,
		},
	})
	if err != nil {
		return evidence.Object{}, err
	}
	if err := ledger.ReconcileToolStep(ctx, step, receipt.ID, receipt.ContentSHA256, time.Now().UTC()); err != nil {
		return evidence.Object{}, err
	}
	return receipt, nil
}
