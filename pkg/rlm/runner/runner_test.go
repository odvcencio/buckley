package runner

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/rlm/configadapter"
)

func TestNew(t *testing.T) {
	t.Run("creates runner with nil dependencies", func(t *testing.T) {
		runner := New(nil, nil, nil, nil, nil, nil)
		require.NotNil(t, runner)
		assert.Nil(t, runner.store)
		assert.Nil(t, runner.models)
		assert.Nil(t, runner.registry)
		assert.Nil(t, runner.cfg)
		assert.Nil(t, runner.planner) // Not created without store/models/cfg
	})

	t.Run("creates runner with config", func(t *testing.T) {
		cfg := config.DefaultConfig()
		runner := New(nil, nil, nil, cfg, nil, nil)
		require.NotNil(t, runner)
		assert.Equal(t, cfg, runner.cfg)
	})
}

func TestRunner_SetTelemetry(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)
	runner.SetTelemetry(nil)
	assert.Nil(t, runner.telemetry)
}

func TestRunner_SetBus(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)
	runner.SetBus(nil)
	assert.Nil(t, runner.bus)
}

func TestRunner_GetCurrentPlan(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	t.Run("returns nil when no plan loaded", func(t *testing.T) {
		assert.Nil(t, runner.GetCurrentPlan())
	})

	t.Run("returns plan after setting", func(t *testing.T) {
		plan := &orchestrator.Plan{ID: "test-plan"}
		runner.mu.Lock()
		runner.currentPlan = plan
		runner.mu.Unlock()

		result := runner.GetCurrentPlan()
		assert.Equal(t, "test-plan", result.ID)
	})
}

func TestRunner_PlanFeature_NilPlanner(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	_, err := runner.PlanFeature("feature", "description")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "planner not initialized")
}

func TestRunner_LoadPlan_NilPlanStore(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	_, err := runner.LoadPlan("plan-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "plan store not configured")
}

func TestRunner_ExecutePlan_NoPlanLoaded(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	err := runner.ExecutePlan()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no plan loaded")
}

func TestRunner_ExecuteTask_NoPlanLoaded(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	err := runner.ExecuteTask("task-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no plan loaded")
}

func TestRunner_ListPlans_NilPlanStore(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	_, err := runner.ListPlans()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "plan store not configured")
}

func TestRunner_ResumeFeature_NilPlanStore(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	err := runner.ResumeFeature("plan-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "plan store not configured")
}

func TestRunner_initRuntime_NilModels(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	err := runner.initRuntime()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model manager required")
}

func TestRunnerRLMConfigNilOrZeroKeepsDefaults(t *testing.T) {
	defaults := rlm.DefaultConfig()

	assert.Equal(t, defaults, runnerRLMConfig(nil))
	assert.Equal(t, defaults, runnerRLMConfig(&config.Config{}))
}

func TestRunnerRLMConfigMatchesSharedAdapter(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{name: "defaults"},
		{
			name: "false compatibility progress option",
			cfg: &config.Config{RLM: config.RLMConfig{
				Coordinator: config.RLMCoordinatorConfig{Model: "coordinator", StreamPartials: false},
				Scratchpad:  config.RLMScratchpadConfig{PersistArtifacts: false, PersistDecisions: false},
			}},
		},
		{
			name: "empty list override",
			cfg: &config.Config{RLM: config.RLMConfig{Tiers: map[string]config.RLMTierConfig{
				"light": {Models: []string{}, Prefer: []string{}, Requires: []string{}},
			}}},
		},
		{
			name: "tier pins caps and requirements",
			cfg: &config.Config{RLM: config.RLMConfig{Tiers: map[string]config.RLMTierConfig{
				"reasoning": {
					Model:             "reasoning-pin",
					MaxCostPerMillion: 18.5,
					MinContextWindow:  196000,
					Requires:          []string{"extended_thinking", "reasoning"},
				},
			}}},
		},
		{
			name: "all active fields",
			cfg: &config.Config{RLM: config.RLMConfig{
				Coordinator: config.RLMCoordinatorConfig{
					Model: "coordinator", MaxIterations: 17, MaxTokensBudget: 12345,
					MaxWallTime: 42 * time.Second, ConfidenceThreshold: 0.42,
				},
				SubAgent: config.RLMSubAgentConfig{Model: "worker", MaxConcurrent: 9, Timeout: 8 * time.Second},
				Scratchpad: config.RLMScratchpadConfig{
					MaxEntriesMemory: 77, MaxRawBytesMemory: 88, EvictionPolicy: "fifo", DefaultTTL: 13 * time.Second,
				},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, configadapter.Resolve(tt.cfg), runnerRLMConfig(tt.cfg))
		})
	}
}

func TestRunnerRLMConfigAppliesEveryActiveRLMField(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Coordinator.Model = "coordinator-model"
	cfg.RLM.Coordinator.MaxIterations = 17
	cfg.RLM.Coordinator.MaxTokensBudget = 12345
	cfg.RLM.Coordinator.MaxWallTime = 42 * time.Second
	cfg.RLM.Coordinator.ConfidenceThreshold = 0.42
	cfg.RLM.Coordinator.StreamPartials = false
	cfg.RLM.SubAgent.Model = "worker-model"
	cfg.RLM.SubAgent.MaxConcurrent = 9
	cfg.RLM.SubAgent.Timeout = 8 * time.Second
	cfg.RLM.Scratchpad.MaxEntriesMemory = 77
	cfg.RLM.Scratchpad.MaxRawBytesMemory = 88
	cfg.RLM.Scratchpad.EvictionPolicy = "fifo"
	cfg.RLM.Scratchpad.DefaultTTL = 13 * time.Second
	cfg.RLM.Scratchpad.PersistArtifacts = false
	cfg.RLM.Scratchpad.PersistDecisions = false
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light": {
			Model:             "configured-light",
			Provider:          "openrouter",
			Models:            []string{"configured-light", "configured-fallback"},
			MaxCostPerMillion: 1.25,
			MinContextWindow:  12000,
			Prefer:            []string{"cost", "quality"},
			Requires:          []string{"extended_thinking"},
		},
	}

	got := runnerRLMConfig(cfg)

	assert.Equal(t, "coordinator-model", got.Coordinator.Model)
	assert.Equal(t, 17, got.Coordinator.MaxIterations)
	assert.Equal(t, 12345, got.Coordinator.MaxTokensBudget)
	assert.Equal(t, 42*time.Second, got.Coordinator.MaxWallTime)
	assert.Equal(t, 0.42, got.Coordinator.ConfidenceThreshold)
	assert.False(t, got.Coordinator.StreamPartials)

	assert.Equal(t, "worker-model", got.SubAgent.Model)
	assert.Equal(t, 9, got.SubAgent.MaxConcurrent)
	assert.Equal(t, 8*time.Second, got.SubAgent.Timeout)

	assert.Equal(t, 77, got.Scratchpad.MaxEntriesMemory)
	assert.Equal(t, int64(88), got.Scratchpad.MaxRawBytesMemory)
	assert.Equal(t, "fifo", got.Scratchpad.EvictionPolicy)
	assert.Equal(t, 13*time.Second, got.Scratchpad.DefaultTTL)
	assert.False(t, got.Scratchpad.PersistArtifacts)
	assert.False(t, got.Scratchpad.PersistDecisions)

	light := got.Tiers[rlm.WeightLight]
	assert.Equal(t, "configured-light", light.Model)
	assert.Equal(t, "openrouter", light.Provider)
	assert.Equal(t, []string{"configured-light", "configured-fallback"}, light.Models)
	assert.Equal(t, 1.25, light.MaxCostPerMillion)
	assert.Equal(t, 12000, light.MinContextWindow)
	assert.Equal(t, []string{"cost", "quality"}, light.Prefer)
	assert.Equal(t, []string{"extended_thinking"}, light.Requires)
	assert.Contains(t, got.Tiers, rlm.WeightReasoning)

	source := cfg.RLM.Tiers["light"]
	source.Models[0] = "mutated-source"
	source.Prefer[0] = "mutated-source"
	source.Requires[0] = "mutated-source"
	cfg.RLM.Tiers["light"] = source
	assert.Equal(t, []string{"configured-light", "configured-fallback"}, got.Tiers[rlm.WeightLight].Models)
	assert.Equal(t, []string{"cost", "quality"}, got.Tiers[rlm.WeightLight].Prefer)
	assert.Equal(t, []string{"extended_thinking"}, got.Tiers[rlm.WeightLight].Requires)
}

func TestRunnerRLMConfigActiveBooleanFieldsOverrideDefaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Coordinator.Model = "active"
	cfg.RLM.Coordinator.StreamPartials = false
	cfg.RLM.Scratchpad.PersistArtifacts = false
	cfg.RLM.Scratchpad.PersistDecisions = false

	got := runnerRLMConfig(cfg)

	assert.False(t, got.Coordinator.StreamPartials)
	assert.False(t, got.Scratchpad.PersistArtifacts)
	assert.False(t, got.Scratchpad.PersistDecisions)
}

func TestRunnerRLMConfigTiersOnlyKeepsDefaultTrueBooleans(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light": {Models: []string{"configured-light"}},
	}

	got := runnerRLMConfig(cfg)

	assert.True(t, got.Coordinator.StreamPartials)
	assert.True(t, got.Scratchpad.PersistArtifacts)
	assert.True(t, got.Scratchpad.PersistDecisions)
	assert.Equal(t, []string{"configured-light"}, got.Tiers[rlm.WeightLight].Models)
}

func TestRunnerRLMConfigConfiguredLightTierSelectsModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light": {
			Models:            []string{"unknown-price", "configured-light", "expensive"},
			MaxCostPerMillion: 2.00,
			MinContextWindow:  16000,
			Prefer:            []string{"cost"},
		},
	}
	got := runnerRLMConfig(cfg)
	router, err := rlm.NewModelRouterWithCatalog(&model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "unknown-price", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 0, Completion: 0}, PricingKnown: false},
		{ID: "configured-light", ContextLength: 16000, Pricing: model.ModelPricing{Prompt: 1.0, Completion: 1.5}, PricingKnown: true},
		{ID: "expensive", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 9.0, Completion: 9.0}, PricingKnown: true},
	}}, got, rlm.RouterOptions{})
	require.NoError(t, err)

	modelID, err := router.Select(rlm.WeightLight)
	require.NoError(t, err)
	assert.Equal(t, "configured-light", modelID)
}

func TestRunner_ensureRuntime_AlreadyInitialized(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	// Even with nil models, ensureRuntime should fail gracefully
	err := runner.ensureRuntime()
	assert.Error(t, err)
}

func TestRunner_taskTypeToWeight(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	tests := []struct {
		taskType orchestrator.TaskType
		expected string
	}{
		{orchestrator.TaskTypeImplementation, "medium"},
		{orchestrator.TaskTypeAnalysis, "light"},
		{orchestrator.TaskTypeValidation, "light"},
		{orchestrator.TaskType("unknown"), "medium"},
	}

	for _, tt := range tests {
		t.Run(string(tt.taskType), func(t *testing.T) {
			assert.Equal(t, tt.expected, runner.taskTypeToWeight(tt.taskType))
		})
	}
}

func TestRunner_partitionTasks(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	t.Run("empty tasks", func(t *testing.T) {
		independent, dependent := runner.partitionTasks(nil)
		assert.Empty(t, independent)
		assert.Empty(t, dependent)
	})

	t.Run("all independent", func(t *testing.T) {
		tasks := []orchestrator.Task{
			{ID: "task-1", Dependencies: nil},
			{ID: "task-2", Dependencies: nil},
		}
		independent, dependent := runner.partitionTasks(tasks)
		assert.Len(t, independent, 2)
		assert.Empty(t, dependent)
	})

	t.Run("with dependencies", func(t *testing.T) {
		tasks := []orchestrator.Task{
			{ID: "task-1", Dependencies: nil},
			{ID: "task-2", Dependencies: []string{"task-1"}},
		}
		independent, dependent := runner.partitionTasks(tasks)
		assert.Len(t, independent, 1)
		assert.Equal(t, "task-1", independent[0].ID)
		assert.Len(t, dependent, 1)
		assert.Equal(t, "task-2", dependent[0].ID)
	})

	t.Run("dependency on completed task is independent", func(t *testing.T) {
		runner.mu.Lock()
		runner.currentPlan = &orchestrator.Plan{
			Tasks: []orchestrator.Task{
				{ID: "task-0", Status: orchestrator.TaskCompleted},
			},
		}
		runner.mu.Unlock()

		tasks := []orchestrator.Task{
			{ID: "task-1", Dependencies: []string{"task-0"}}, // task-0 is completed
		}
		independent, dependent := runner.partitionTasks(tasks)
		assert.Len(t, independent, 1)
		assert.Empty(t, dependent)
	})
}

func TestRunner_updateTaskStatus(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)

	t.Run("nil plan does nothing", func(t *testing.T) {
		runner.updateTaskStatus(nil, "task-1", orchestrator.TaskCompleted)
		// No panic
	})

	t.Run("updates existing task", func(t *testing.T) {
		plan := &orchestrator.Plan{
			Tasks: []orchestrator.Task{
				{ID: "task-1", Status: orchestrator.TaskPending},
				{ID: "task-2", Status: orchestrator.TaskPending},
			},
		}

		runner.updateTaskStatus(plan, "task-1", orchestrator.TaskCompleted)
		assert.Equal(t, orchestrator.TaskCompleted, plan.Tasks[0].Status)
		assert.Equal(t, orchestrator.TaskPending, plan.Tasks[1].Status)
	})

	t.Run("nonexistent task does nothing", func(t *testing.T) {
		plan := &orchestrator.Plan{
			Tasks: []orchestrator.Task{
				{ID: "task-1", Status: orchestrator.TaskPending},
			},
		}

		runner.updateTaskStatus(plan, "nonexistent", orchestrator.TaskCompleted)
		assert.Equal(t, orchestrator.TaskPending, plan.Tasks[0].Status)
	})
}

func TestRunner_executeTaskBatch_EmptyTasks(t *testing.T) {
	runner := New(nil, nil, nil, nil, nil, nil)
	plan := &orchestrator.Plan{}

	err := runner.executeTaskBatch(plan, nil)
	assert.NoError(t, err)
}

// Mock plan store for testing
type mockPlanStore struct {
	plans map[string]*orchestrator.Plan
}

func newMockPlanStore() *mockPlanStore {
	return &mockPlanStore{
		plans: make(map[string]*orchestrator.Plan),
	}
}

func (m *mockPlanStore) SavePlan(plan *orchestrator.Plan) error {
	m.plans[plan.ID] = plan
	return nil
}

func (m *mockPlanStore) LoadPlan(id string) (*orchestrator.Plan, error) {
	if plan, ok := m.plans[id]; ok {
		return plan, nil
	}
	return nil, nil
}

func (m *mockPlanStore) ListPlans() ([]orchestrator.Plan, error) {
	var plans []orchestrator.Plan
	for _, p := range m.plans {
		plans = append(plans, *p)
	}
	return plans, nil
}

func (m *mockPlanStore) ReadLog(planID string, logKind string, limit int) ([]string, string, error) {
	return nil, "", nil
}

func TestRunner_WithMockPlanStore(t *testing.T) {
	planStore := newMockPlanStore()

	t.Run("list empty plans", func(t *testing.T) {
		runner := New(nil, nil, nil, nil, nil, planStore)
		plans, err := runner.ListPlans()
		require.NoError(t, err)
		assert.Empty(t, plans)
	})

	t.Run("load plan", func(t *testing.T) {
		planStore.plans["plan-1"] = &orchestrator.Plan{ID: "plan-1", FeatureName: "Test Feature"}

		runner := New(nil, nil, nil, nil, nil, planStore)
		plan, err := runner.LoadPlan("plan-1")
		require.NoError(t, err)
		require.NotNil(t, plan)
		assert.Equal(t, "plan-1", plan.ID)
		assert.Equal(t, "Test Feature", plan.FeatureName)

		// Current plan should be set
		assert.Equal(t, plan, runner.GetCurrentPlan())
	})

	t.Run("resume feature", func(t *testing.T) {
		planStore.plans["plan-2"] = &orchestrator.Plan{ID: "plan-2"}

		runner := New(nil, nil, nil, nil, nil, planStore)
		err := runner.ResumeFeature("plan-2")
		require.NoError(t, err)
		assert.Equal(t, "plan-2", runner.GetCurrentPlan().ID)
	})
}

func TestRunner_ExecuteTask_TaskNotFound(t *testing.T) {
	planStore := newMockPlanStore()
	planStore.plans["plan-1"] = &orchestrator.Plan{
		ID: "plan-1",
		Tasks: []orchestrator.Task{
			{ID: "task-1"},
		},
	}

	runner := New(nil, nil, nil, nil, nil, planStore)
	_, _ = runner.LoadPlan("plan-1")

	// Try to execute nonexistent task - will fail at runtime init
	err := runner.ExecuteTask("nonexistent")
	assert.Error(t, err)
	// Error could be "task not found" or "initialize runtime" depending on order
}

func TestRunner_ExecutePlan_AllTasksComplete(t *testing.T) {
	planStore := newMockPlanStore()
	planStore.plans["plan-1"] = &orchestrator.Plan{
		ID: "plan-1",
		Tasks: []orchestrator.Task{
			{ID: "task-1", Status: orchestrator.TaskCompleted},
			{ID: "task-2", Status: orchestrator.TaskCompleted},
		},
	}

	runner := New(nil, nil, nil, nil, nil, planStore)
	_, _ = runner.LoadPlan("plan-1")

	// Runtime check happens before pending task check, so this will fail
	// without a model manager - that's expected behavior
	err := runner.ExecutePlan()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "initialize runtime")
}
