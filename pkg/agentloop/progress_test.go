package agentloop

import (
	"context"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

// TestProgressController_PriorityOrder locks the deterministic policy
// order (section 20.5): fuses beat budget, budget beats pressure,
// pressure beats debt, debt beats repetition, repetition beats parking,
// parking beats synthesis, and nothing firing means continue.
func TestProgressController_PriorityOrder(t *testing.T) {
	t.Parallel()

	ctrl := ProgressController{
		Mode:  ModeDynamic,
		Fuses: Fuses{ModelRequests: 500, ToolExecutions: 2000, WallTime: 6 * time.Hour},
	}

	everything := ProgressState{
		StateChanged:     true,
		VerificationDebt: 1,
		Repetition:       1,
		EvidenceObserved: true,
		EvidenceNovelty:  0,
		Uncertainty:      1,
		Pressure:         1,
		BudgetSet:        true,
		BudgetRemaining:  0,
		TaskDone:         true,
	}

	cases := []struct {
		name     string
		state    ProgressState
		counters FuseCounters
		want     ProgressDecision
	}{
		{
			name:     "fuse beats everything",
			state:    everything,
			counters: FuseCounters{ModelRequests: 500},
			want:     DecideStopSafety,
		},
		{
			name:  "budget beats pressure",
			state: everything,
			want:  DecidePark,
		},
		{
			name: "pressure beats debt",
			state: func() ProgressState {
				s := everything
				s.BudgetSet = false
				return s
			}(),
			want: DecideCheckpoint,
		},
		{
			name: "debt beats repetition",
			state: func() ProgressState {
				s := everything
				s.BudgetSet = false
				s.Pressure = 0
				return s
			}(),
			want: DecideVerify,
		},
		{
			name: "repetition beats parking",
			state: func() ProgressState {
				s := everything
				s.BudgetSet = false
				s.Pressure = 0
				s.StateChanged = false
				return s
			}(),
			want: DecideReplan,
		},
		{
			name: "parking beats synthesis",
			state: func() ProgressState {
				s := everything
				s.BudgetSet = false
				s.Pressure = 0
				s.StateChanged = false
				s.Repetition = 0
				return s
			}(),
			want: DecidePark,
		},
		{
			name:  "task done synthesizes",
			state: ProgressState{TaskDone: true},
			want:  DecideSynthesize,
		},
		{
			name:  "nothing fires continues",
			state: ProgressState{},
			want:  DecideContinue,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ctrl.Decide(tc.state, tc.counters)
			if got.Decision != tc.want {
				t.Fatalf("Decide = %s (%s), want %s\ntrace: %+v", got.Decision, got.Reason, tc.want, got.Trace)
			}
			if len(got.Trace) == 0 {
				t.Fatal("decision carried no policy trace")
			}
			fired := 0
			for _, s := range got.Trace {
				if s.Fired {
					fired++
				}
			}
			if fired != 1 {
				t.Fatalf("trace fired %d rules, want exactly 1: %+v", fired, got.Trace)
			}
		})
	}
}

// TestProgressController_UnknownSignalsNeverRoute locks the estimator
// rule: zero-valued estimator signals (novelty without observation,
// unknown uncertainty) must not trigger replanning or parking.
func TestProgressController_UnknownSignalsNeverRoute(t *testing.T) {
	t.Parallel()

	ctrl := ProgressController{Mode: ModeDynamic}
	state := ProgressState{
		Repetition:  1,
		Uncertainty: 1,
		// EvidenceObserved false: novelty is unknown, not "zero and low".
	}
	got := ctrl.Decide(state, FuseCounters{})
	if got.Decision != DecideContinue {
		t.Fatalf("Decide = %s, want continue when evidence signals are unobserved", got.Decision)
	}
}

// TestProgressController_ShadowModeNeverApplies locks the rollout
// contract: shadow decisions are recorded but not acted on.
func TestProgressController_ShadowModeNeverApplies(t *testing.T) {
	t.Parallel()

	shadow := ProgressController{Mode: ModeShadow, Fuses: Fuses{ModelRequests: 1}}
	got := shadow.Decide(ProgressState{}, FuseCounters{ModelRequests: 5})
	if got.Decision != DecideStopSafety {
		t.Fatalf("Decide = %s, want stop_safety", got.Decision)
	}
	if got.Apply {
		t.Fatal("shadow mode must not apply decisions")
	}

	dynamic := ProgressController{Mode: ModeDynamic, Fuses: Fuses{ModelRequests: 1}}
	if got := dynamic.Decide(ProgressState{}, FuseCounters{ModelRequests: 5}); !got.Apply {
		t.Fatal("dynamic mode must apply decisions")
	}
}

func TestNewProgressController_OnlyEnablesStagedModes(t *testing.T) {
	t.Parallel()

	if got := NewProgressController(ModeLegacy, "v1", Fuses{}); got != nil {
		t.Fatalf("legacy controller = %+v, want nil", got)
	}
	if got := NewProgressController("typo", "v1", Fuses{}); got != nil {
		t.Fatalf("unknown controller mode = %+v, want nil", got)
	}
	got := NewProgressController(" DYNAMIC ", "v1", Fuses{ModelRequests: 9})
	if got == nil || got.Mode != ModeDynamic || got.PolicyVersion != "v1" || got.Fuses.ModelRequests != 9 {
		t.Fatalf("dynamic controller = %+v", got)
	}
}

// progressToolCallResponse returns a response carrying one tool call, so
// a test model can keep the loop in tool rounds until a fuse or guard
// fires.
func progressToolCallResponse(name string) *model.ChatResponse {
	return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
		Role: "assistant",
		ToolCalls: []model.ToolCall{{
			ID:       "call-1",
			Function: model.FunctionCall{Name: name, Arguments: "{}"},
		}},
	}}}}
}

// newProgressTestController wires a Controller whose model always requests
// one more tool call, with the given progress controller attached.
func newProgressTestController(t *testing.T, progress *ProgressController, store runledger.Store, runID string) *Controller {
	t.Helper()
	round := 0
	ctrl, err := NewController(ControllerConfig{
		Progress: progress,
		BuildRequest: func(ctx context.Context, r int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			round++
			return progressToolCallResponse("probe"), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			// A distinct outcome per round keeps the Governor's repeat
			// detectors quiet so the progress fuse is what stops the loop.
			return []ToolOutcome{{Content: "result-" + time.Now().String() + string(rune('a'+round)), Success: true}}, nil
		}),
		RunLedger: store,
		RunID:     runID,
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return ctrl
}

// TestController_ProgressFuseStopsLoop locks dynamic-mode enforcement: an
// exceeded emergency fuse ends the turn as a loop-guard stop with the
// emergency_fuse kind, well before the Governor's own 32-round ceiling.
func TestController_ProgressFuseStopsLoop(t *testing.T) {
	t.Parallel()

	progress := &ProgressController{Mode: ModeDynamic, Fuses: Fuses{ModelRequests: 2}}
	ctrl := newProgressTestController(t, progress, nil, "")

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonLoopGuard {
		t.Fatalf("FinishReason = %q, want loop_guard", result.FinishReason)
	}
	if result.GuardDecision.Kind != "emergency_fuse" {
		t.Fatalf("GuardDecision.Kind = %q, want emergency_fuse", result.GuardDecision.Kind)
	}
	if result.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2 (fuse at 2 model requests)", result.Rounds)
	}
	if !strings.Contains(result.Content, "Buckley stopped the tool loop") {
		t.Fatalf("Content = %q, want guard stop message", result.Content)
	}
}

// TestController_ProgressShadowRecordsWithoutActing locks the shadow
// rollout contract at the engine level: the same exceeded fuse is
// recorded as a controller.decision event with applied=false, and the
// loop keeps running until the Governor's own ceiling stops it.
func TestController_ProgressShadowRecordsWithoutActing(t *testing.T) {
	t.Parallel()

	store, err := runledger.New(t.TempDir() + "/ledger.db")
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	progress := &ProgressController{Mode: ModeShadow, Fuses: Fuses{ModelRequests: 2}}
	ctrl := newProgressTestController(t, progress, store, run.RunID)

	result, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.GuardDecision.Kind == "emergency_fuse" {
		t.Fatal("shadow mode enforced the fuse")
	}
	if result.Rounds <= 2 {
		t.Fatalf("Rounds = %d, want the loop to outlive the shadow fuse", result.Rounds)
	}

	events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawShadowStop bool
	var sawProgressProjection bool
	for _, e := range events {
		if e.Type != runledger.EventControllerDecision {
			continue
		}
		if e.Payload["kind"] == "progress_policy" && e.Payload["decision"] == string(DecideStopSafety) {
			if applied, _ := e.Payload["applied"].(bool); applied {
				t.Fatalf("shadow decision recorded as applied: %+v", e.Payload)
			}
			projection, ok := e.Payload["progress"].(map[string]any)
			if !ok {
				t.Fatalf("progress decision omitted projection: %+v", e.Payload)
			}
			toolCalls, ok := projection["tool_calls"].(float64)
			if !ok || toolCalls < 1 {
				t.Fatalf("progress projection omitted observed tool calls: %+v", projection)
			}
			sawProgressProjection = true
			sawShadowStop = true
		}
	}
	if !sawShadowStop {
		t.Fatal("no shadow stop_safety decision was recorded to the ledger")
	}
	if !sawProgressProjection {
		t.Fatal("no durable progress projection was recorded to the ledger")
	}
}

// TestGovernorProgressSignals locks the two Governor-derived signals: a
// repeating outcome raises repetition pressure and lowers evidence
// novelty; distinct outcomes keep pressure low and novelty high.
func TestGovernorProgressSignals(t *testing.T) {
	t.Parallel()

	repeating := New(DefaultConfig())
	for i := 0; i < 4; i++ {
		repeating.Observe("probe", `{"q":1}`, "same result", true)
	}
	if got := repeating.RepetitionPressure(); got < 0.99 {
		t.Fatalf("RepetitionPressure after 4 identical outcomes = %.2f, want ~1", got)
	}
	novelty, observed := repeating.EvidenceNovelty()
	if !observed || novelty > 0.3 {
		t.Fatalf("EvidenceNovelty = (%.2f, %v), want observed and low", novelty, observed)
	}

	fresh := New(DefaultConfig())
	fresh.Observe("probe", `{"q":1}`, "result one", true)
	fresh.Observe("probe", `{"q":2}`, "result two", true)
	if _, observed := fresh.EvidenceNovelty(); observed {
		t.Fatal("novelty reported as observed with too few samples")
	}
	fresh.Observe("probe", `{"q":3}`, "result three", true)
	fresh.Observe("probe", `{"q":4}`, "result four", true)
	novelty, observed = fresh.EvidenceNovelty()
	if !observed || novelty < 0.99 {
		t.Fatalf("EvidenceNovelty = (%.2f, %v), want observed and ~1 for all-new outcomes", novelty, observed)
	}
	if got := fresh.RepetitionPressure(); got > 0.5 {
		t.Fatalf("RepetitionPressure for distinct outcomes = %.2f, want low", got)
	}
}

func TestProgressTracker_ProjectsZeroYieldWithoutCallingItFailure(t *testing.T) {
	t.Parallel()

	tracker := progressTracker{}
	tracker.Observe("search_text", ToolOutcome{
		Content:       `{"success":true,"count":0}`,
		Success:       true,
		YieldObserved: true,
		YieldCount:    0,
		YieldUnit:     "match",
	})
	tracker.Observe("find_files", ToolOutcome{
		Content:       `{"success":true,"count":0}`,
		Success:       true,
		YieldObserved: true,
		YieldCount:    0,
		YieldUnit:     "file",
	})
	tracker.Observe("list_directory", ToolOutcome{
		Content:       `{"success":true,"count":3}`,
		Success:       true,
		YieldObserved: true,
		YieldCount:    3,
		YieldUnit:     "entry",
	})
	tracker.Observe("read_file", ToolOutcome{Content: "Error: missing", Success: false})

	got := tracker.Snapshot()
	if got.ToolCalls != 4 || got.SuccessfulToolCalls != 3 || got.FailedToolCalls != 1 {
		t.Fatalf("operation totals = %+v", got)
	}
	if got.YieldObservedCalls != 3 || got.ZeroYieldCalls != 2 {
		t.Fatalf("yield totals = %+v", got)
	}
	if got.ConsecutiveZeroYieldCalls != 0 {
		t.Fatalf("positive yield did not reset streak: %+v", got)
	}
	if got.LastToolName != "list_directory" || got.LastYieldCount != 3 || got.LastYieldUnit != "entry" {
		t.Fatalf("last yield = %+v", got)
	}
}

func TestController_ExposesProgressProjectionOnNormalCompletion(t *testing.T) {
	t.Parallel()

	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return progressToolCallResponse("search_text"), nil
			}
			return textResponse("done", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{
				Content:       `{"success":true,"count":0}`,
				Success:       true,
				YieldObserved: true,
				YieldCount:    0,
				YieldUnit:     "match",
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want normal completion", result.FinishReason)
	}
	if result.Progress.ToolCalls != 1 || result.Progress.SuccessfulToolCalls != 1 || result.Progress.FailedToolCalls != 0 {
		t.Fatalf("operation totals = %+v", result.Progress)
	}
	if result.Progress.ZeroYieldCalls != 1 || result.Progress.ConsecutiveZeroYieldCalls != 1 {
		t.Fatalf("zero-yield progress = %+v", result.Progress)
	}
}

func BenchmarkProgressTrackerObserve(b *testing.B) {
	tracker := progressTracker{}
	outcome := ToolOutcome{
		Content:       `{"success":true,"count":0}`,
		Success:       true,
		YieldObserved: true,
		YieldCount:    0,
		YieldUnit:     "match",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.Observe("search_text", outcome)
	}
}

// TestProgressController_FuseReasons locks the fuse reporting: every
// exceeded fuse appears in the reason, and unset fuses never trip.
func TestProgressController_FuseReasons(t *testing.T) {
	t.Parallel()

	ctrl := ProgressController{
		Mode:  ModeDynamic,
		Fuses: Fuses{ModelRequests: 10, ToolExecutions: 20, WallTime: time.Hour, BudgetUSD: 5},
	}
	got := ctrl.Decide(ProgressState{}, FuseCounters{
		ModelRequests:  10,
		ToolExecutions: 25,
		Elapsed:        2 * time.Hour,
		SpentUSD:       6.50,
	})
	if got.Decision != DecideStopSafety {
		t.Fatalf("Decide = %s, want stop_safety", got.Decision)
	}
	for _, want := range []string{"model requests 10/10", "tool executions 25/20", "wall time", "spend $6.50/$5.00"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason %q missing %q", got.Reason, want)
		}
	}

	unset := ProgressController{Mode: ModeDynamic}
	if got := unset.Decide(ProgressState{}, FuseCounters{ModelRequests: 1_000_000}); got.Decision != DecideContinue {
		t.Fatalf("unset fuses tripped: %s", got.Decision)
	}
}
