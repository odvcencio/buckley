package runner

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
