package execprogram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

type cancellationContextKey struct{}

type finalizationContext struct {
	err       error
	value     any
	deadline  time.Time
	remaining time.Duration
}

func observeFinalization(ctx context.Context) finalizationContext {
	deadline, _ := ctx.Deadline()
	return finalizationContext{ctx.Err(), ctx.Value(cancellationContextKey{}), deadline, time.Until(deadline)}
}

type cancellationEvidence struct {
	evidence.Store
	outputContext finalizationContext
	failOutput    error
}

func (s *cancellationEvidence) Put(ctx context.Context, object evidence.Object) (evidence.Object, error) {
	if object.Kind == evidence.KindToolResult {
		s.outputContext = observeFinalization(ctx)
		if s.failOutput != nil {
			return evidence.Object{}, s.failOutput
		}
	}
	return s.Store.Put(ctx, object)
}

type cancellationLedger struct {
	runledger.Store
	onStarted     func()
	onCapability  func()
	finishContext finalizationContext
	failFinish    error
}

func (s *cancellationLedger) Append(ctx context.Context, event runledger.Event) (runledger.Event, error) {
	if event.Type == "exec_program.finished" {
		s.finishContext = observeFinalization(ctx)
		if s.failFinish != nil {
			return runledger.Event{}, s.failFinish
		}
	}
	stored, err := s.Store.Append(ctx, event)
	if err == nil {
		if event.Type == "exec_program.started" && s.onStarted != nil {
			s.onStarted()
		}
		if event.Type == "capability.call" && s.onCapability != nil {
			s.onCapability()
		}
	}
	return stored, err
}

func TestProgramCancellation_Finalization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline bool
		fail     string
	}{
		{"canceled", false, ""},
		{"deadline", true, ""},
		{"output_failure", false, "output"},
		{"finish_failure", false, "finish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := evidence.New(filepath.Join(t.TempDir(), "evidence.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ledger, err := runledger.NewWithDB(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "cancel"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), cancellationContextKey{}, "kept")
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			failure := errors.New("finalization unavailable")
			ev := &cancellationEvidence{Store: store}
			trace := &cancellationLedger{Store: ledger, onStarted: cancel}
			if tc.deadline {
				trace.onStarted = func() { <-ctx.Done() }
			}
			if tc.fail == "output" {
				ev.failOutput = failure
			}
			if tc.fail == "finish" {
				trace.failFinish = failure
			}
			// Cancellation happens after the start record, before any program can execute.
			runner, err := execmode.NewRunner(t.TempDir(), execmode.AuditSinkFunc(func(execmode.AuditRecord) error { return nil }), time.Minute, execmode.WithoutIsolation())
			if err != nil {
				t.Fatal(err)
			}
			program := &ProgramTool{runner: runner, evidence: ev, ledger: trace, runID: run.RunID, sessionID: "cancel"}
			const source = "package main\nfunc main(){}"
			result, execErr := program.ExecuteWithContext(ctx, map[string]any{"source": source})
			if tc.fail != "" {
				if result != nil || !errors.Is(execErr, failure) || !errors.Is(execErr, ctx.Err()) {
					t.Fatalf("lost cancellation or persistence failure: result=%v error=%v", result, execErr)
				}
			} else if execErr != nil || result == nil || result.Success || !strings.Contains(result.Error, ctx.Err().Error()) || result.Data["exit_code"] != -1 {
				t.Fatalf("cancellation result=%v error=%v", result, execErr)
			}
			for _, observed := range []finalizationContext{ev.outputContext, trace.finishContext} {
				if observed.deadline.IsZero() && tc.fail == "output" {
					continue
				}
				if observed.err != nil || observed.value != "kept" || observed.remaining <= 0 || observed.remaining > finalizationTimeout {
					t.Fatalf("unbounded, canceled or stripped finalization context: %+v", observed)
				}
			}
			if tc.fail != "output" && ev.outputContext.deadline != trace.finishContext.deadline {
				t.Fatal("output and finish writes do not share one budget")
			}
			events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: run.RunID})
			if err != nil {
				t.Fatal(err)
			}
			wantEvents := 2
			if tc.fail != "" {
				wantEvents = 1
			}
			if len(events) != wantEvents || events[0].Type != "exec_program.started" {
				t.Fatalf("execution records: %+v", events)
			}
			if tc.fail != "" {
				return
			}
			finish := events[1]
			if finish.Type != "exec_program.finished" || finish.Payload["success"] != false || finish.Payload["error"] != result.Error || finish.Payload["execution_id"] != events[0].ID || !reflect.DeepEqual(finish.EvidenceIDs, []string{result.Data["program_evidence"].(string), result.Data["output_evidence"].(string)}) {
				t.Fatalf("incorrect terminal record: %+v", finish)
			}
			for _, id := range finish.EvidenceIDs {
				if _, err := store.Get(context.Background(), id); err != nil {
					t.Fatalf("missing canceled execution evidence: %v", err)
				}
			}
		})
	}
}

func TestProgramCancellation_BeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &sourceLimitEvidence{}
	program := &ProgramTool{evidence: store}
	result, err := program.ExecuteWithContext(ctx, map[string]any{"source": "package main\nfunc main(){}"})
	if result != nil || !errors.Is(err, context.Canceled) || store.puts != 0 {
		t.Fatalf("started canceled work: result=%v error=%v puts=%d", result, err, store.puts)
	}
}

func TestProgramCancellation_RunningProgramPreservesEvidence(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	path := filepath.Join(t.TempDir(), "evidence.db")
	store, err := evidence.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "running-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	trace := &cancellationLedger{Store: ledger, onCapability: cancel}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := NewProgramTool(root, trace, store, run.RunID, "running-cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	const source = `package main
import("fmt";"execprogram/caps")
func main(){fmt.Println("before cancel");caps.ReadFile("input.txt");for {}}`
	result, err := program.ExecuteWithContext(ctx, map[string]any{"source": source})
	if err != nil || result == nil || result.Success || !strings.Contains(result.Error, context.Canceled.Error()) || result.Data["exit_code"] != -1 || result.Data["stdout"] != "before cancel\n" {
		t.Fatalf("running cancellation: result=%v error=%v", result, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = evidence.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err = runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	programID := result.Data["program_evidence"].(string)
	outputID := result.Data["output_evidence"].(string)
	savedSource, err := store.Get(context.Background(), programID)
	if err != nil || string(savedSource.InlineBody) != source {
		t.Fatalf("lost source after reopening: %v", err)
	}
	savedOutput, err := store.Get(context.Background(), outputID)
	if err != nil || !strings.Contains(string(savedOutput.InlineBody), "exit=-1") || !strings.Contains(string(savedOutput.InlineBody), "before cancel\n") {
		t.Fatalf("lost partial output after reopening: %v", err)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: run.RunID})
	if err != nil || len(events) != 3 || events[0].Type != "exec_program.started" || events[1].Type != "capability.call" || events[2].Type != "exec_program.finished" {
		t.Fatalf("lost execution history: events=%+v error=%v", events, err)
	}
	if events[2].Payload["success"] != false || events[2].Payload["error"] != result.Error || events[2].Payload["execution_id"] != events[0].ID || !reflect.DeepEqual(events[2].EvidenceIDs, []string{programID, outputID}) {
		t.Fatalf("incorrect canceled execution history: %+v", events[2])
	}
}
