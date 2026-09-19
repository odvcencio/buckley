package execprogram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestProgramExecutionProvenance(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	ctx := context.Background()
	ev, err := evidence.New(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var firstOutput, firstProgram, firstRun string
	for runIndex := 0; runIndex < 2; runIndex++ {
		run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "provenance", Backend: "code-mode"})
		if err != nil {
			t.Fatal(err)
		}
		program, err := NewProgramTool(t.TempDir(), ledger, ev, run.RunID, "provenance", nil)
		if err != nil {
			t.Fatal(err)
		}
		source := "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"same output\")}\n"
		if runIndex == 1 {
			source += "// different source, identical output\n"
		}
		for call := 0; call < 2; call++ {
			result, err := program.ExecuteWithContext(ctx, map[string]any{"source": source})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("execution err=%v", err)
			}
			id, _ := result.Data["execution_id"].(string)
			if id == "" || seen[id] {
				t.Fatalf("missing or repeated execution_id %q", id)
			}
			seen[id] = true
			programID := result.Data["program_evidence"].(string)
			outputID := result.Data["output_evidence"].(string)
			if firstOutput == "" {
				firstOutput, firstProgram, firstRun = outputID, programID, run.RunID
			}
			if outputID != firstOutput {
				t.Fatal("identical output stopped deduplicating")
			}
			if (runIndex == 0) != (programID == firstProgram) {
				t.Fatal("source identity mismatch")
			}
			events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
			if err != nil {
				t.Fatal(err)
			}
			var starts, finishes []runledger.Event
			for _, event := range events {
				if event.Type == "exec_program.started" {
					starts = append(starts, event)
				}
				if event.Type == "exec_program.finished" {
					finishes = append(finishes, event)
				}
			}
			if len(starts) != call+1 || len(finishes) != call+1 {
				t.Fatalf("starts=%d finishes=%d", len(starts), len(finishes))
			}
			start, finish := starts[call], finishes[call]
			if start.ID != id || finish.Payload["execution_id"] != id || start.Sequence >= finish.Sequence {
				t.Fatal("broken execution linkage")
			}
			if !reflect.DeepEqual(start.EvidenceIDs, []string{programID}) || !reflect.DeepEqual(finish.EvidenceIDs, []string{programID, outputID}) {
				t.Fatal("broken evidence references")
			}
			if finish.Payload["program_evidence"] != programID || finish.Payload["output_evidence"] != outputID || finish.Payload["success"] != true || finish.Payload["stdout_truncated"] != false || finish.Payload["stderr_truncated"] != false {
				t.Fatalf("finish=%+v", finish.Payload)
			}
			if _, ok := finish.Payload["stdout"]; ok {
				t.Fatal("duplicated output in ledger")
			}
			if _, ok := finish.Payload["stderr"]; ok {
				t.Fatal("duplicated stderr in ledger")
			}
		}
	}
	output, err := ev.Get(ctx, firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	if output.Metadata[evidence.MetaRunID] != firstRun || output.Metadata["program_evidence"] != firstProgram {
		t.Fatal("deduplicated metadata was rewritten instead of recording execution events")
	}
}

type failingExecutionLedger struct {
	runledger.Store
	failType        string
	capabilityCalls atomic.Int64
}

func (s *failingExecutionLedger) Append(ctx context.Context, event runledger.Event) (runledger.Event, error) {
	if event.Type == "capability.call" {
		s.capabilityCalls.Add(1)
	}
	if event.Type == s.failType {
		return runledger.Event{}, errors.New("injected execution ledger failure")
	}
	return s.Store.Append(ctx, event)
}

func TestProgramExecutionLedgerFailure(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	for _, failType := range []string{"exec_program.started", "exec_program.finished"} {
		t.Run(failType, func(t *testing.T) {
			ctx := context.Background()
			ev, err := evidence.New(filepath.Join(t.TempDir(), "evidence.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer ev.Close()
			store, err := runledger.NewWithDB(ev.DB())
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "failure", Backend: "code-mode"})
			if err != nil {
				t.Fatal(err)
			}
			ledger := &failingExecutionLedger{Store: store, failType: failType}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			program, err := NewProgramTool(root, ledger, ev, run.RunID, "failure", nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := program.ExecuteWithContext(ctx, map[string]any{"source": "package main\nimport \"execprogram/caps\"\nfunc main(){_,_,err:=caps.ReadFile(\"input.txt\");if err!=nil{panic(err)}}"})
			if err == nil || !strings.Contains(err.Error(), "injected execution ledger failure") || (result != nil && result.Success) {
				t.Fatalf("did not fail closed: err=%v", err)
			}
			wantCalls := 1
			if failType == "exec_program.started" {
				wantCalls = 0
			}
			if ledger.capabilityCalls.Load() != int64(wantCalls) {
				t.Fatalf("capability calls=%d want=%d", ledger.capabilityCalls.Load(), wantCalls)
			}
			events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == "exec_program.finished" {
					t.Fatal("failed finish was recorded as acknowledged")
				}
			}
		})
	}
}
