package experiment

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/transparency"
	"m31labs.dev/buckley/pkg/worktree"
)

// mockWorktreeManager implements parallel.WorktreeManager for testing
type mockWorktreeManager struct {
	createFn func(branch string) (*worktree.Worktree, error)
	removeFn func(branch string, force bool) error
}

func (m *mockWorktreeManager) Create(branch string) (*worktree.Worktree, error) {
	if m.createFn != nil {
		return m.createFn(branch)
	}
	return &worktree.Worktree{Branch: branch, Path: "/tmp/worktree/" + branch}, nil
}

func (m *mockWorktreeManager) Remove(branch string, force bool) error {
	if m.removeFn != nil {
		return m.removeFn(branch, force)
	}
	return nil
}

type successfulExperimentExecutor struct{}

func (successfulExperimentExecutor) Execute(ctx context.Context, task *parallel.AgentTask, wtPath string) (*parallel.AgentResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &parallel.AgentResult{
		Success: true,
		Output:  "variant completed",
		Files:   []string{"result.txt"},
		Metrics: map[string]int{
			"prompt_tokens":     7,
			"completion_tokens": 3,
			"tool_calls":        1,
		},
		TotalCost: 0.0123,
	}, nil
}

type countingExperimentExecutor struct {
	count *int
}

func (e countingExperimentExecutor) Execute(ctx context.Context, task *parallel.AgentTask, wtPath string) (*parallel.AgentResult, error) {
	if e.count != nil {
		(*e.count)++
	}
	return (&successfulExperimentExecutor{}).Execute(ctx, task, wtPath)
}

func TestNewRunner(t *testing.T) {
	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	tests := []struct {
		name    string
		cfg     RunnerConfig
		deps    Dependencies
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid dependencies",
			cfg:  RunnerConfig{MaxConcurrent: 2, DefaultTimeout: time.Minute},
			deps: Dependencies{
				Config:       cfg,
				ModelManager: mgr,
				Worktree:     wt,
			},
			wantErr: false,
		},
		{
			name: "nil config returns error",
			cfg:  RunnerConfig{},
			deps: Dependencies{
				Config:       nil,
				ModelManager: mgr,
				Worktree:     wt,
			},
			wantErr: true,
			errMsg:  "config is required",
		},
		{
			name: "nil model manager returns error",
			cfg:  RunnerConfig{},
			deps: Dependencies{
				Config:       cfg,
				ModelManager: nil,
				Worktree:     wt,
			},
			wantErr: true,
			errMsg:  "model manager is required",
		},
		{
			name: "nil worktree returns error",
			cfg:  RunnerConfig{},
			deps: Dependencies{
				Config:       cfg,
				ModelManager: mgr,
				Worktree:     nil,
			},
			wantErr: true,
			errMsg:  "worktree manager is required",
		},
		{
			name: "zero MaxConcurrent defaults to 4",
			cfg:  RunnerConfig{MaxConcurrent: 0},
			deps: Dependencies{
				Config:       cfg,
				ModelManager: mgr,
				Worktree:     wt,
			},
			wantErr: false,
		},
		{
			name: "zero DefaultTimeout defaults to 30m",
			cfg:  RunnerConfig{DefaultTimeout: 0},
			deps: Dependencies{
				Config:       cfg,
				ModelManager: mgr,
				Worktree:     wt,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := NewRunner(tt.cfg, tt.deps)

			if tt.wantErr {
				if err == nil {
					t.Errorf("NewRunner() error = nil, want error containing %q", tt.errMsg)
					return
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("NewRunner() error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("NewRunner() unexpected error = %v", err)
				return
			}

			if runner == nil {
				t.Error("NewRunner() returned nil runner")
			}
		})
	}
}

func TestRunExperiment_Validation(t *testing.T) {
	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Second},
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	tests := []struct {
		name    string
		exp     *Experiment
		wantErr string
	}{
		{
			name:    "nil experiment returns error",
			exp:     nil,
			wantErr: "experiment is nil",
		},
		{
			name:    "empty variants returns error",
			exp:     &Experiment{ID: "test", Name: "test"},
			wantErr: "experiment has no variants",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runner.RunExperiment(context.Background(), tt.exp)
			if err == nil {
				t.Errorf("RunExperiment() error = nil, want error containing %q", tt.wantErr)
				return
			}
			if err.Error() != tt.wantErr {
				t.Errorf("RunExperiment() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRunExperiment_CleanupWarningPreservesCompletedResult(t *testing.T) {
	cleanupErr := errors.New("failed to remove worktree; retained path /tmp/buckley-retained: dirty output preserved")
	runner, events := newCleanupWarningTestRunner(t, func(branch string, force bool) error {
		if force {
			t.Fatalf("cleanup used force=true for branch %s", branch)
		}
		return cleanupErr
	})
	exp := cleanupWarningTestExperiment()

	results, err := runner.RunExperiment(context.Background(), exp)
	if err != nil {
		t.Fatalf("RunExperiment() unexpected error = %v", err)
	}
	if exp.Status != ExperimentCompleted {
		t.Fatalf("experiment status = %q, want %q", exp.Status, ExperimentCompleted)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	result := results[0]
	if result == nil || !result.Success {
		t.Fatalf("result = %#v, want successful result", result)
	}
	if result.Output != "variant completed" {
		t.Fatalf("result output = %q, want executor output", result.Output)
	}
	if result.Metrics["prompt_tokens"] != 7 || result.Metrics["completion_tokens"] != 3 {
		t.Fatalf("metrics = %#v, want token metrics preserved", result.Metrics)
	}
	if result.TotalCost != 0.0123 {
		t.Fatalf("total cost = %v, want 0.0123", result.TotalCost)
	}

	warnings := cleanupWarningEvents(drainTelemetry(events))
	if len(warnings) != 1 {
		t.Fatalf("cleanup warnings = %d, want 1", len(warnings))
	}
	data := warnings[0].Data
	if data["experiment_id"] != exp.ID {
		t.Fatalf("warning experiment_id = %v, want %q", data["experiment_id"], exp.ID)
	}
	warning, ok := data["warning"].(string)
	if !ok {
		t.Fatalf("warning payload = %#v, want string", data["warning"])
	}
	if !strings.Contains(warning, "retained path /tmp/buckley-retained") {
		t.Fatalf("warning = %q, want retained path", warning)
	}
}

func TestRunExperiment_CleanCleanupDoesNotPublishWarning(t *testing.T) {
	runner, events := newCleanupWarningTestRunner(t, func(branch string, force bool) error {
		if force {
			t.Fatalf("cleanup used force=true for branch %s", branch)
		}
		return nil
	})
	exp := cleanupWarningTestExperiment()

	results, err := runner.RunExperiment(context.Background(), exp)
	if err != nil {
		t.Fatalf("RunExperiment() unexpected error = %v", err)
	}
	if exp.Status != ExperimentCompleted {
		t.Fatalf("experiment status = %q, want %q", exp.Status, ExperimentCompleted)
	}
	if len(results) != 1 || results[0] == nil || !results[0].Success {
		t.Fatalf("results = %#v, want one successful result", results)
	}
	if warnings := cleanupWarningEvents(drainTelemetry(events)); len(warnings) != 0 {
		t.Fatalf("cleanup warnings = %d, want 0: %#v", len(warnings), warnings)
	}
}

func TestRunExperimentPersistsExecutionIdentityThroughReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp-runner-identity",
			"model":"gpt-4o-release",
			"choices":[{"index":0,"message":{"role":"assistant","content":"runner identity output"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	db := setupTestDB(t)
	store := NewStore(db)
	runner, err := NewRunner(RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Minute}, Dependencies{
		Config:       cfg,
		ModelManager: mgr,
		Worktree: &mockWorktreeManager{createFn: func(branch string) (*worktree.Worktree, error) {
			return &worktree.Worktree{Branch: branch, Path: t.TempDir()}, nil
		}},
		Store: store,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	exp := &Experiment{
		ID:   "exp-runner-identity",
		Name: "identity",
		Task: Task{Prompt: "finish"},
		Variants: []Variant{{
			ID:   "variant-a",
			Name: "candidate",
			// Keep dollar admission enabled; this fixture needs a priced model to execute.
			ModelID:    "gpt-4o",
			ProviderID: "openai",
		}},
		Criteria: []SuccessCriterion{{Name: "contains output", Type: CriterionContains, Target: "runner identity output", Weight: 1}},
	}
	results, err := runner.RunExperiment(context.Background(), exp)
	if err != nil {
		t.Fatalf("RunExperiment: %v", err)
	}
	if len(results) != 1 || len(results[0].ModelExecutions) != 1 {
		t.Fatalf("results = %+v, want one execution identity", results)
	}
	if got := results[0].ModelExecutions[0]; got.RequestedModel != "gpt-4o" || got.SelectedModel != "gpt-4o" || got.ProviderID != "openai" || got.ResponseModel != "gpt-4o-release" || got.ResponseID != "resp-runner-identity" {
		t.Fatalf("result identity = %+v", got)
	}
	runs, err := store.ListRuns(exp.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || len(runs[0].ModelExecutions) != 1 {
		t.Fatalf("stored runs = %+v, want persisted identity", runs)
	}
	if err := store.ReplaceEvaluations(runs[0].ID, []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1}}); err != nil {
		t.Fatalf("ReplaceEvaluations: %v", err)
	}
	report, err := NewComparator(store).Compare(exp)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Variants) != 1 || len(report.Variants[0].ModelExecutions) != 1 {
		t.Fatalf("report variants = %+v, want identity in report DTO", report.Variants)
	}
	if got := formatExecutionIdentityDetail(report.Variants[0].ModelExecutions); !strings.Contains(got, "response_id=resp-runner-identity") || !strings.Contains(got, "response_model=gpt-4o-release") {
		t.Fatalf("execution summary = %q, want exact response identity", got)
	}
}

func TestRunExperimentPreflightsAllVariantsBeforePersistenceOrExecution(t *testing.T) {
	tests := []struct {
		name string
		exp  func() *Experiment
	}{
		{
			name: "unsupported json in second variant",
			exp: func() *Experiment {
				return &Experiment{
					ID:   "exp-invalid-second-json",
					Name: "invalid",
					Task: Task{Prompt: "prompt"},
					Variants: []Variant{
						{ID: "variant-ok", Name: "ok", ModelID: "model-a"},
						{ID: "variant-bad", Name: "bad", ModelID: "model-b", CustomConfig: map[string]any{"bad": func() {}}},
					},
				}
			},
		},
		{
			name: "nan temperature in second variant",
			exp: func() *Experiment {
				nan := math.NaN()
				return &Experiment{
					ID:   "exp-invalid-second-nan",
					Name: "invalid",
					Task: Task{Prompt: "prompt"},
					Variants: []Variant{
						{ID: "variant-ok", Name: "ok", ModelID: "model-a"},
						{ID: "variant-bad", Name: "bad", ModelID: "model-b", Temperature: &nan},
					},
				}
			},
		},
		{
			name: "infinite criterion weight",
			exp: func() *Experiment {
				return &Experiment{
					ID:       "exp-invalid-criteria-inf",
					Name:     "invalid",
					Task:     Task{Prompt: "prompt"},
					Variants: []Variant{{ID: "variant-ok", Name: "ok", ModelID: "model-a"}, {ID: "variant-ok-2", Name: "ok2", ModelID: "model-b"}},
					Criteria: []SuccessCriterion{{Name: "bad", Type: CriterionTestPass, Target: "go test", Weight: math.Inf(1)}},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeDB := setupTestDB(t)
			store := NewStore(storeDB)
			exp := tt.exp()
			creates := 0
			executes := 0
			wt := &mockWorktreeManager{createFn: func(branch string) (*worktree.Worktree, error) {
				creates++
				return &worktree.Worktree{Branch: branch, Path: "/tmp/worktree/" + branch}, nil
			}}
			hub := telemetry.NewHub()
			events, unsubscribe := hub.Subscribe()
			t.Cleanup(unsubscribe)
			t.Cleanup(hub.Close)
			runner := &Runner{
				cfg:       RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Second},
				telemetry: hub,
				parallel: parallel.NewOrchestrator(wt, countingExperimentExecutor{count: &executes}, parallel.Config{
					MaxAgents:       1,
					TaskQueueSize:   10,
					ResultQueueSize: 10,
				}),
				store: store,
			}

			_, err := runner.RunExperiment(context.Background(), exp)
			if err == nil {
				t.Fatalf("RunExperiment error = nil, want preflight error")
			}
			if exp.Status == ExperimentRunning {
				t.Fatalf("experiment status = %q, want not running after preflight failure", exp.Status)
			}
			if creates != 0 || executes != 0 {
				t.Fatalf("work launched despite preflight failure: worktrees=%d executes=%d", creates, executes)
			}
			stored, loadErr := store.GetExperiment(exp.ID)
			if loadErr != nil {
				t.Fatalf("GetExperiment: %v", loadErr)
			}
			if stored != nil {
				t.Fatalf("experiment row persisted despite preflight failure: %#v", stored)
			}
			runs, listErr := store.ListRuns(exp.ID)
			if listErr != nil {
				t.Fatalf("ListRuns: %v", listErr)
			}
			if len(runs) != 0 {
				t.Fatalf("running rows persisted despite preflight failure: %#v", runs)
			}
			if gotEvents := drainTelemetry(events); len(gotEvents) != 0 {
				t.Fatalf("published events despite preflight failure: %#v", gotEvents)
			}
		})
	}
}

func newCleanupWarningTestRunner(t *testing.T, removeFn func(branch string, force bool) error) (*Runner, <-chan telemetry.Event) {
	t.Helper()
	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)
	t.Cleanup(hub.Close)

	wt := &mockWorktreeManager{removeFn: removeFn}
	orchestrator := parallel.NewOrchestrator(wt, successfulExperimentExecutor{}, parallel.Config{
		MaxAgents:       1,
		TaskQueueSize:   10,
		ResultQueueSize: 10,
	})
	return &Runner{
		cfg: RunnerConfig{
			MaxConcurrent:  1,
			DefaultTimeout: time.Second,
			CleanupOnDone:  true,
		},
		telemetry: hub,
		parallel:  orchestrator,
	}, events
}

func cleanupWarningTestExperiment() *Experiment {
	return &Experiment{
		ID:   "exp-cleanup",
		Name: "Cleanup warning",
		Task: Task{Prompt: "produce output"},
		Variants: []Variant{
			{
				ID:      "variant-one",
				Name:    "one",
				ModelID: "model/a",
			},
		},
	}
}

func drainTelemetry(events <-chan telemetry.Event) []telemetry.Event {
	var drained []telemetry.Event
	for {
		select {
		case event := <-events:
			drained = append(drained, event)
		default:
			return drained
		}
	}
}

func cleanupWarningEvents(events []telemetry.Event) []telemetry.Event {
	var warnings []telemetry.Event
	for _, event := range events {
		if event.Type != telemetry.EventDebug {
			continue
		}
		if event.Data["kind"] == "experiment.cleanup_warning" {
			warnings = append(warnings, event)
		}
	}
	return warnings
}

func TestJoinTools(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{
			name:   "empty slice returns empty string",
			values: nil,
			want:   "",
		},
		{
			name:   "single tool",
			values: []string{"read_file"},
			want:   "read_file",
		},
		{
			name:   "multiple tools",
			values: []string{"read_file", "write_file", "execute"},
			want:   "read_file,write_file,execute",
		},
		{
			name:   "trims whitespace",
			values: []string{"  read_file  ", "  write_file  "},
			want:   "read_file,write_file",
		},
		{
			name:   "skips empty values",
			values: []string{"read_file", "", "  ", "write_file"},
			want:   "read_file,write_file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinTools(tt.values)
			if got != tt.want {
				t.Errorf("joinTools() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCopyContext(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]string
	}{
		{
			name:  "nil input returns empty map",
			input: nil,
		},
		{
			name:  "empty input returns empty map",
			input: map[string]string{},
		},
		{
			name:  "copies all values",
			input: map[string]string{"key1": "value1", "key2": "value2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := copyContext(tt.input)

			if got == nil {
				t.Error("copyContext() returned nil, want empty map")
				return
			}

			if len(got) != len(tt.input) {
				t.Errorf("copyContext() length = %d, want %d", len(got), len(tt.input))
			}

			for k, v := range tt.input {
				if got[k] != v {
					t.Errorf("copyContext()[%q] = %q, want %q", k, got[k], v)
				}
			}

			// Verify it's a copy, not the same map
			if tt.input != nil && len(tt.input) > 0 {
				for k := range got {
					got[k] = "modified"
					if tt.input[k] == "modified" {
						t.Error("copyContext() did not create a copy")
					}
					break
				}
			}
		})
	}
}

func TestCloneExperimentDeepSnapshotsMutableInputs(t *testing.T) {
	systemPrompt := "original system"
	temp := 0.3
	maxTokens := 100
	exp := &Experiment{
		ID: "exp-snapshot",
		Task: Task{
			Prompt:  "original prompt",
			Context: map[string]string{"mode": "original"},
			Files:   []string{"a.go"},
			Scope:   []string{"pkg/a/..."},
		},
		Variants: []Variant{{
			ID:           "variant-1",
			Name:         "variant",
			ModelID:      "model-a",
			SystemPrompt: &systemPrompt,
			Temperature:  &temp,
			MaxTokens:    &maxTokens,
			ToolsAllowed: []string{"read"},
			CustomConfig: map[string]any{"nested": map[string]any{"value": "original"}},
			Files:        []string{"variant-a.go"},
			Scope:        []string{"pkg/variant/..."},
		}},
		Criteria: []SuccessCriterion{{Name: "tests", Type: CriterionTestPass, Target: "go test", Weight: 1}},
	}
	snapshot, err := cloneExperiment(exp)
	if err != nil {
		t.Fatalf("cloneExperiment: %v", err)
	}
	exp.Task.Context["mode"] = "changed"
	exp.Task.Files[0] = "changed.go"
	exp.Variants[0].ToolsAllowed[0] = "write"
	exp.Variants[0].CustomConfig["nested"].(map[string]any)["value"] = "changed"
	systemPrompt = "changed system"
	temp = 0.9
	maxTokens = 200
	exp.Criteria[0].Name = "changed"

	if snapshot.Task.Context["mode"] != "original" || snapshot.Task.Files[0] != "a.go" {
		t.Fatalf("task snapshot aliased caller mutation: %#v", snapshot.Task)
	}
	if *snapshot.Variants[0].SystemPrompt != "original system" || *snapshot.Variants[0].Temperature != 0.3 || *snapshot.Variants[0].MaxTokens != 100 {
		t.Fatalf("variant pointer snapshot aliased caller mutation: %#v", snapshot.Variants[0])
	}
	if snapshot.Variants[0].ToolsAllowed[0] != "read" {
		t.Fatalf("tools snapshot aliased caller mutation: %#v", snapshot.Variants[0].ToolsAllowed)
	}
	nested := snapshot.Variants[0].CustomConfig["nested"].(map[string]any)
	if nested["value"] != "original" {
		t.Fatalf("custom config snapshot aliased caller mutation: %#v", snapshot.Variants[0].CustomConfig)
	}
	if snapshot.Criteria[0].Name != "tests" {
		t.Fatalf("criteria snapshot aliased caller mutation: %#v", snapshot.Criteria)
	}
}

func TestCloneExperimentRejectsUnsupportedCustomConfig(t *testing.T) {
	exp := &Experiment{
		Task: Task{Prompt: "prompt"},
		Variants: []Variant{{
			ID:           "variant-1",
			Name:         "variant",
			ModelID:      "model-a",
			CustomConfig: map[string]any{"bad": func() {}},
		}},
	}
	if _, err := cloneExperiment(exp); err == nil {
		t.Fatalf("cloneExperiment unsupported custom config error = nil, want error")
	}
}

func TestMetricValue(t *testing.T) {
	tests := []struct {
		name    string
		metrics map[string]int
		key     string
		want    int
	}{
		{
			name:    "nil metrics returns 0",
			metrics: nil,
			key:     "any",
			want:    0,
		},
		{
			name:    "missing key returns 0",
			metrics: map[string]int{"other": 10},
			key:     "missing",
			want:    0,
		},
		{
			name:    "existing key returns value",
			metrics: map[string]int{"tokens": 100},
			key:     "tokens",
			want:    100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metricValue(tt.metrics, tt.key)
			if got != tt.want {
				t.Errorf("metricValue() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFindVariant(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{
			{ID: "v1", Name: "variant-1"},
			{ID: "v2", Name: "variant-2"},
			{ID: "v3", Name: "variant-3"},
		},
	}

	tests := []struct {
		name string
		exp  *Experiment
		id   string
		want string
	}{
		{
			name: "nil experiment returns nil",
			exp:  nil,
			id:   "v1",
			want: "",
		},
		{
			name: "found variant",
			exp:  exp,
			id:   "v2",
			want: "variant-2",
		},
		{
			name: "not found returns nil",
			exp:  exp,
			id:   "v99",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findVariant(tt.exp, tt.id)
			if tt.want == "" {
				if got != nil {
					t.Errorf("findVariant() = %v, want nil", got)
				}
			} else {
				if got == nil || got.Name != tt.want {
					t.Errorf("findVariant() = %v, want variant with Name %q", got, tt.want)
				}
			}
		})
	}
}

func TestPublishMethods_NilSafety(t *testing.T) {
	// Test that all publish methods are nil-safe
	var nilRunner *Runner

	// These should not panic
	t.Run("publishExperimentStart nil runner", func(t *testing.T) {
		nilRunner.publishExperimentStart(&Experiment{})
	})

	t.Run("publishVariantEvent nil runner", func(t *testing.T) {
		nilRunner.publishVariantEvent("test", &Experiment{}, &Variant{}, nil)
	})

	t.Run("publishVariantResult nil runner", func(t *testing.T) {
		nilRunner.publishVariantResult(&Experiment{}, &parallel.AgentResult{})
	})

	t.Run("publishExperimentEnd nil runner", func(t *testing.T) {
		nilRunner.publishExperimentEnd(&Experiment{})
	})

	// Test with nil telemetry
	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Second},
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
			Telemetry:    nil, // explicitly nil
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	t.Run("publishExperimentStart nil telemetry", func(t *testing.T) {
		runner.publishExperimentStart(&Experiment{})
	})

	t.Run("publishVariantEvent nil telemetry", func(t *testing.T) {
		runner.publishVariantEvent("test", &Experiment{}, &Variant{}, nil)
	})

	t.Run("publishVariantResult nil telemetry", func(t *testing.T) {
		runner.publishVariantResult(&Experiment{}, &parallel.AgentResult{})
	})

	t.Run("publishExperimentEnd nil telemetry", func(t *testing.T) {
		runner.publishExperimentEnd(&Experiment{})
	})

	// Test with nil experiment
	t.Run("publishExperimentStart nil experiment", func(t *testing.T) {
		runner.publishExperimentStart(nil)
	})

	t.Run("publishExperimentEnd nil experiment", func(t *testing.T) {
		runner.publishExperimentEnd(nil)
	})
}

func TestNotifyMethods_NilSafety(t *testing.T) {
	// Test that all notify methods are nil-safe
	var nilRunner *Runner
	ctx := context.Background()

	// These should not panic
	t.Run("notifyExperimentStart nil runner", func(t *testing.T) {
		nilRunner.notifyExperimentStart(ctx, &Experiment{})
	})

	t.Run("notifyVariantStart nil runner", func(t *testing.T) {
		nilRunner.notifyVariantStart(ctx, &Experiment{}, &Variant{})
	})

	t.Run("notifyVariantResult nil runner", func(t *testing.T) {
		nilRunner.notifyVariantResult(ctx, &Experiment{}, &parallel.AgentResult{})
	})

	t.Run("notifyExperimentEnd nil runner", func(t *testing.T) {
		nilRunner.notifyExperimentEnd(ctx, &Experiment{}, nil)
	})

	// Test with nil notify manager
	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Second},
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
			Notify:       nil, // explicitly nil
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	t.Run("notifyExperimentStart nil notify", func(t *testing.T) {
		runner.notifyExperimentStart(ctx, &Experiment{})
	})

	t.Run("notifyVariantStart nil notify", func(t *testing.T) {
		runner.notifyVariantStart(ctx, &Experiment{}, &Variant{})
	})

	t.Run("notifyVariantResult nil notify", func(t *testing.T) {
		runner.notifyVariantResult(ctx, &Experiment{}, &parallel.AgentResult{})
	})

	t.Run("notifyExperimentEnd nil notify", func(t *testing.T) {
		runner.notifyExperimentEnd(ctx, &Experiment{}, nil)
	})

	// Test with nil experiment/variant/result
	t.Run("notifyExperimentStart nil experiment", func(t *testing.T) {
		runner.notifyExperimentStart(ctx, nil)
	})

	t.Run("notifyVariantStart nil variant", func(t *testing.T) {
		runner.notifyVariantStart(ctx, &Experiment{}, nil)
	})

	t.Run("notifyVariantResult nil result", func(t *testing.T) {
		runner.notifyVariantResult(ctx, &Experiment{}, nil)
	})
}

func TestVariantName(t *testing.T) {
	tests := []struct {
		name    string
		variant *Variant
		want    string
	}{
		{
			name:    "nil variant returns empty",
			variant: nil,
			want:    "",
		},
		{
			name:    "variant with name",
			variant: &Variant{Name: "my-variant"},
			want:    "my-variant",
		},
		{
			name:    "variant with empty name uses ID",
			variant: &Variant{ID: "var-123", Name: ""},
			want:    "var-123",
		},
		{
			name:    "variant with both uses name",
			variant: &Variant{ID: "var-123", Name: "custom-name"},
			want:    "custom-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := variantName(tt.variant)
			if got != tt.want {
				t.Errorf("variantName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPersistResult(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{MaxConcurrent: 1, DefaultTimeout: time.Minute},
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
			Store:        store,
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	// Create experiment
	exp := &Experiment{
		ID:   "exp-persist",
		Name: "persist-test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "variant-1", ModelID: "gpt-4"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	runIDs := make(map[string]string)
	startTimes := make(map[string]time.Time)

	// Test successful result
	t.Run("successful result", func(t *testing.T) {
		reasoning := 12
		result := &parallel.AgentResult{
			TaskID:   "var-1",
			Success:  true,
			Output:   "completed successfully",
			Duration: 5 * time.Second,
			Branch:   "experiment/exp-persist/var-1",
			Files:    []string{"file1.go", "file2.go"},
			Metrics: map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": 200,
				"tool_calls":        5,
			},
			TotalCost:   0.05,
			CostUnknown: true,
			Usage: &transparency.TokenUsage{
				Input:                100,
				Output:               200,
				ReportedTotal:        300,
				ReportedReasoning:    &reasoning,
				UsageEvidencePresent: true,
			},
		}

		err := runner.persistResult(context.Background(), exp, result, runIDs, startTimes)
		if err != nil {
			t.Errorf("persistResult() error = %v", err)
		}

		// Verify run was saved
		runID := runIDs["var-1"]
		if runID == "" {
			t.Error("persistResult() did not generate run ID")
		}
		result.Usage.Input = 999
		*result.Usage.ReportedReasoning = 99
		run, err := store.GetRun(runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Metrics.Usage == nil || run.Metrics.Usage.Input != 100 || run.Metrics.Usage.ReportedReasoning == nil || *run.Metrics.Usage.ReportedReasoning != 12 || !run.Metrics.CostUnknown {
			t.Fatalf("stored usage = %+v costUnknown=%v, want cloned usage evidence", run.Metrics.Usage, run.Metrics.CostUnknown)
		}
	})

	// Test failed result
	t.Run("failed result", func(t *testing.T) {
		result := &parallel.AgentResult{
			TaskID:   "var-1",
			Success:  false,
			Output:   "failed",
			Error:    errors.New("something went wrong"),
			Duration: 2 * time.Second,
		}

		// Need a new run ID since we're saving again
		delete(runIDs, "var-1")

		err := runner.persistResult(context.Background(), exp, result, runIDs, startTimes)
		if err != nil {
			t.Errorf("persistResult() error = %v", err)
		}
	})
}

func TestRunnerConfig_Defaults(t *testing.T) {
	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{}, // all zeros
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	if runner.cfg.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4", runner.cfg.MaxConcurrent)
	}

	if runner.cfg.DefaultTimeout != 30*time.Minute {
		t.Errorf("DefaultTimeout = %v, want 30m", runner.cfg.DefaultTimeout)
	}
}
