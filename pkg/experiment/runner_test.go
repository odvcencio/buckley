package experiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/parallel"
	"github.com/odvcencio/buckley/pkg/worktree"
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
			TotalCost: 0.05,
		}

		err := runner.persistResult(exp, result, runIDs, startTimes)
		if err != nil {
			t.Errorf("persistResult() error = %v", err)
		}

		// Verify run was saved
		runID := runIDs["var-1"]
		if runID == "" {
			t.Error("persistResult() did not generate run ID")
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

		err := runner.persistResult(exp, result, runIDs, startTimes)
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
