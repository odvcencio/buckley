package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/coordination/coordinator"
	"m31labs.dev/buckley/pkg/coordination/events"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/rlm/configadapter"
	"m31labs.dev/buckley/pkg/storage"
)

func TestNewServer(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)

	srv, err := NewServer(coord, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	assert.NotNil(t, srv.coordinator)
}

func TestResolveRLMConfigAppliesConfiguredTiers(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light": {
			Model:             "acp-light",
			Provider:          "openrouter",
			Models:            []string{"acp-light", "acp-fallback"},
			MaxCostPerMillion: 2.5,
			MinContextWindow:  24000,
			Prefer:            []string{"quality", "cost"},
			Requires:          []string{"extended_thinking"},
		},
	}

	got := resolveRLMConfig(cfg)
	light := got.Tiers[rlm.WeightLight]
	assert.Equal(t, "acp-light", light.Model)
	assert.Equal(t, "openrouter", light.Provider)
	assert.Equal(t, []string{"acp-light", "acp-fallback"}, light.Models)
	assert.Equal(t, 2.5, light.MaxCostPerMillion)
	assert.Equal(t, 24000, light.MinContextWindow)
	assert.Equal(t, []string{"quality", "cost"}, light.Prefer)
	assert.Equal(t, []string{"extended_thinking"}, light.Requires)
	assert.Contains(t, got.Tiers, rlm.WeightReasoning)

	source := cfg.RLM.Tiers["light"]
	source.Models[0] = "mutated-source"
	source.Prefer[0] = "mutated-source"
	source.Requires[0] = "mutated-source"
	cfg.RLM.Tiers["light"] = source
	assert.Equal(t, []string{"acp-light", "acp-fallback"}, got.Tiers[rlm.WeightLight].Models)
	assert.Equal(t, []string{"quality", "cost"}, got.Tiers[rlm.WeightLight].Prefer)
	assert.Equal(t, []string{"extended_thinking"}, got.Tiers[rlm.WeightLight].Requires)
}

func TestResolveRLMConfigTiersOnlyKeepsDefaultTrueBooleans(t *testing.T) {
	cfg := &config.Config{}
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light": {Models: []string{"acp-light"}},
	}

	got := resolveRLMConfig(cfg)

	assert.True(t, got.Coordinator.StreamPartials)
	assert.True(t, got.Scratchpad.PersistArtifacts)
	assert.True(t, got.Scratchpad.PersistDecisions)
	assert.Equal(t, []string{"acp-light"}, got.Tiers[rlm.WeightLight].Models)
}

func TestResolveRLMConfigMatchesSharedAdapter(t *testing.T) {
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
			assert.Equal(t, configadapter.Resolve(tt.cfg), resolveRLMConfig(tt.cfg))
		})
	}
}

func TestRegisterAgentRPC(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	srv, _ := NewServer(coord, nil, nil, nil)
	ctx := context.Background()

	t.Run("successful registration", func(t *testing.T) {
		req := &acppb.RegisterAgentRequest{
			AgentId:      "test-agent",
			Type:         "builder",
			Endpoint:     "localhost:50053",
			Capabilities: []string{"read_files"},
			Metadata:     map[string]string{"version": "1.0"},
		}

		resp, err := srv.RegisterAgent(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, "test-agent", resp.Agent.Id)
		assert.NotEmpty(t, resp.SessionToken)
	})

	t.Run("missing agent_id", func(t *testing.T) {
		req := &acppb.RegisterAgentRequest{
			Endpoint: "localhost:50053",
		}

		_, err := srv.RegisterAgent(ctx, req)
		assert.Error(t, err)
	})
}

func TestApplyEdits(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Worktrees: config.WorktreeConfig{RootPath: root}}
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	filePath := filepath.Join(root, "foo.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello world\n"), 0o644))

	req := &acppb.ApplyEditsRequest{
		AgentId:   "zed",
		SessionId: "sess",
		Edits: []*acppb.TextEdit{
			{
				Uri: filePath,
				Range: &acppb.Range{
					Start: &acppb.Position{Line: 0, Character: 6},
					End:   &acppb.Position{Line: 0, Character: 11},
				},
				NewText: "Zed",
			},
		},
	}

	resp, err := srv.ApplyEdits(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Applied)
	assert.Contains(t, resp.Files, filePath)

	updated, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "hello Zed\n", string(updated))
}

func TestApplyEdits_DryRun(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Worktrees: config.WorktreeConfig{RootPath: root}}
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	filePath := filepath.Join(root, "bar.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("abc"), 0o644))

	req := &acppb.ApplyEditsRequest{
		AgentId:   "zed",
		SessionId: "sess",
		DryRun:    true,
		Edits: []*acppb.TextEdit{
			{
				Uri: filePath,
				Range: &acppb.Range{
					Start: &acppb.Position{Line: 0, Character: 0},
					End:   &acppb.Position{Line: 0, Character: 3},
				},
				NewText: "xyz",
			},
		},
	}

	resp, err := srv.ApplyEdits(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, resp.Applied)
	assert.Equal(t, "dry-run only (no files written)", resp.Message)

	contents, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "abc", string(contents))
}

func TestApplyEdits_PathEscape(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Worktrees: config.WorktreeConfig{RootPath: root}}
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	srv, err := NewServer(coord, nil, cfg, nil)
	require.NoError(t, err)

	req := &acppb.ApplyEditsRequest{
		AgentId:   "zed",
		SessionId: "sess",
		Edits: []*acppb.TextEdit{
			{
				Uri:     "../escaping.txt",
				NewText: "boom",
				Range:   nil,
			},
		},
	}

	_, err = srv.ApplyEdits(context.Background(), req)
	assert.Error(t, err)
}

func TestUpdateEditorState_WithTodos(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.New(dbPath)
	require.NoError(t, err)
	cfg := &config.Config{Worktrees: config.WorktreeConfig{RootPath: t.TempDir()}}
	eventStore := events.NewInMemoryStore()
	coord, _ := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	srv, err := NewServer(coord, nil, cfg, store)
	require.NoError(t, err)

	sessionID := "sess-1"
	require.NoError(t, store.EnsureSession(sessionID))
	require.NoError(t, store.CreateTodo(&storage.Todo{
		SessionID:  sessionID,
		Content:    "first",
		ActiveForm: "list",
		Status:     "in_progress",
		OrderIndex: 0,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))
	require.NoError(t, store.CreateTodo(&storage.Todo{
		SessionID:  sessionID,
		Content:    "second",
		ActiveForm: "list",
		Status:     "pending",
		OrderIndex: 1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))

	resp, err := srv.UpdateEditorState(context.Background(), &acppb.UpdateEditorStateRequest{
		AgentId:   "zed",
		SessionId: sessionID,
		Context:   &acppb.EditorContext{Document: &acppb.DocumentSnapshot{Uri: "file:///tmp/x", Content: "x"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.TodoState, "in_progress:")
}
