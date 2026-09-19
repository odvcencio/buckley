package experiment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
	"m31labs.dev/buckley/pkg/storage"
)

func TestNewReplayer(t *testing.T) {
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{},
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
		store   *storage.Store
		runner  *Runner
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid dependencies",
			store:   store,
			runner:  runner,
			wantErr: false,
		},
		{
			name:    "nil store returns error",
			store:   nil,
			runner:  runner,
			wantErr: true,
			errMsg:  "store is required",
		},
		{
			name:    "nil runner returns error",
			store:   store,
			runner:  nil,
			wantErr: true,
			errMsg:  "runner is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replayer, err := NewReplayer(tt.store, tt.runner)

			if tt.wantErr {
				if err == nil {
					t.Errorf("NewReplayer() error = nil, want error containing %q", tt.errMsg)
					return
				}
				if err.Error() != tt.errMsg {
					t.Errorf("NewReplayer() error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("NewReplayer() unexpected error = %v", err)
				return
			}

			if replayer == nil {
				t.Error("NewReplayer() returned nil replayer")
			}
		})
	}
}

func TestReplay_Validation(t *testing.T) {
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	cfg := &config.Config{}
	mgr := &model.Manager{}
	wt := &mockWorktreeManager{}

	runner, err := NewRunner(
		RunnerConfig{},
		Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Worktree:     wt,
		},
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	replayer, err := NewReplayer(store, runner)
	if err != nil {
		t.Fatalf("failed to create replayer: %v", err)
	}

	tests := []struct {
		name    string
		cfg     ReplayConfig
		wantErr string
	}{
		{
			name:    "empty source session id",
			cfg:     ReplayConfig{SourceSessionID: "", NewModelID: "gpt-4"},
			wantErr: "source session id is required",
		},
		{
			name:    "whitespace source session id",
			cfg:     ReplayConfig{SourceSessionID: "   ", NewModelID: "gpt-4"},
			wantErr: "source session id is required",
		},
		{
			name:    "empty model id",
			cfg:     ReplayConfig{SourceSessionID: "session-1", NewModelID: ""},
			wantErr: "new model id is required",
		},
		{
			name:    "whitespace model id",
			cfg:     ReplayConfig{SourceSessionID: "session-1", NewModelID: "   "},
			wantErr: "new model id is required",
		},
		{
			name:    "session not found",
			cfg:     ReplayConfig{SourceSessionID: "nonexistent", NewModelID: "gpt-4", AllowLiveTools: true},
			wantErr: "session not found: nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := replayer.Replay(context.Background(), tt.cfg)
			if err == nil {
				t.Errorf("Replay() error = nil, want error containing %q", tt.wantErr)
				return
			}
			if err.Error() != tt.wantErr {
				t.Errorf("Replay() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestReplay_NilReplayer(t *testing.T) {
	var replayer *Replayer

	_, err := replayer.Replay(context.Background(), ReplayConfig{
		SourceSessionID: "session-1",
		NewModelID:      "gpt-4",
		AllowLiveTools:  true,
	})
	if err == nil {
		t.Error("Replay() on nil replayer should return error")
	}
	if err.Error() != "replayer unavailable" {
		t.Errorf("Replay() error = %q, want 'replayer unavailable'", err.Error())
	}
}

func TestReplay_DeterministicUnsupportedBeforeDependencies(t *testing.T) {
	var replayer *Replayer
	_, err := replayer.Replay(context.Background(), ReplayConfig{
		SourceSessionID:    "session-1",
		NewModelID:         "gpt-4",
		DeterministicTools: true,
	})
	if !errors.Is(err, ErrDeterministicReplayUnsupported) {
		t.Fatalf("Replay() error = %v, want ErrDeterministicReplayUnsupported", err)
	}
}

func TestReplay_LiveModeRequiresExplicitOptInBeforeDependencies(t *testing.T) {
	var replayer *Replayer
	_, err := replayer.Replay(context.Background(), ReplayConfig{
		SourceSessionID: "session-1",
		NewModelID:      "gpt-4",
	})
	if !errors.Is(err, ErrLiveReplayRequiresOptIn) {
		t.Fatalf("Replay() error = %v, want ErrLiveReplayRequiresOptIn", err)
	}
}

func TestReplay_LiveModeUsesExactFirstUserPromptAndLineage(t *testing.T) {
	store := setupReplayStore(t)
	seedReplaySession(t, store, "source-session", []storage.Message{
		{Role: "system", Content: "ignore"},
		{Role: "user", Content: "  first prompt\n  keep indentation  \n"},
		{Role: "assistant", Content: "old incomplete answer", IsTruncated: true},
		{Role: "user", Content: "second prompt"},
	})
	executor := &capturingReplayExecutor{}
	replayer := newReplayTestReplayer(t, store, executor)

	run, err := replayer.Replay(context.Background(), ReplayConfig{
		SourceSessionID: "source-session",
		NewModelID:      "new-model",
		NewProviderID:   "new-provider",
		AllowLiveTools:  true,
	})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if run == nil || run.Status != RunCompleted {
		t.Fatalf("run = %#v, want completed run", run)
	}
	if executor.prompt != "  first prompt\n  keep indentation  \n" {
		t.Fatalf("prompt = %q, want exact first user prompt", executor.prompt)
	}
	if executor.context["source_session_id"] != "source-session" {
		t.Fatalf("context = %#v, want source session lineage", executor.context)
	}
	if _, found := executor.context["replay_mode"]; found {
		t.Fatalf("context = %#v, must not write replay_mode", executor.context)
	}
	if executor.modelID != "new-model" || executor.providerID != "new-provider" {
		t.Fatalf("variant model/provider = %q/%q, want new-model/new-provider", executor.modelID, executor.providerID)
	}
}

func TestReplay_ShortSourceIDDoesNotPanic(t *testing.T) {
	store := setupReplayStore(t)
	seedReplaySession(t, store, "abc", []storage.Message{{Role: "user", Content: "prompt"}})
	replayer := newReplayTestReplayer(t, store, &capturingReplayExecutor{})

	if _, err := replayer.Replay(context.Background(), ReplayConfig{
		SourceSessionID: "abc",
		NewModelID:      "new-model",
		AllowLiveTools:  true,
	}); err != nil {
		t.Fatalf("Replay() with short source ID error = %v", err)
	}
}

func TestReplay_RejectsMissingBlankOrTruncatedFirstUserPrompt(t *testing.T) {
	tests := []struct {
		name     string
		messages []storage.Message
		want     string
	}{
		{
			name:     "missing user prompt",
			messages: []storage.Message{{Role: "assistant", Content: "answer"}},
			want:     "no user prompts found in session",
		},
		{
			name:     "blank user prompt",
			messages: []storage.Message{{Role: "user", Content: " \n\t "}},
			want:     "first user prompt is blank and cannot be replayed",
		},
		{
			name:     "truncated user prompt",
			messages: []storage.Message{{Role: "user", Content: "partial", IsTruncated: true}},
			want:     "first user prompt is truncated and cannot be replayed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := setupReplayStore(t)
			seedReplaySession(t, store, "source-session", tt.messages)
			executor := &capturingReplayExecutor{}
			replayer := newReplayTestReplayer(t, store, executor)

			_, err := replayer.Replay(context.Background(), ReplayConfig{
				SourceSessionID: "source-session",
				NewModelID:      "new-model",
				AllowLiveTools:  true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Replay() error = %v, want %q", err, tt.want)
			}
			if executor.calls != 0 {
				t.Fatalf("executor calls = %d, want zero", executor.calls)
			}
		})
	}
}

type capturingReplayExecutor struct {
	calls      int
	prompt     string
	context    map[string]string
	modelID    string
	providerID string
}

func (e *capturingReplayExecutor) Execute(ctx context.Context, task *parallel.AgentTask, wtPath string) (*parallel.AgentResult, error) {
	e.calls++
	e.prompt = task.Prompt
	e.context = copyContext(task.Context)
	e.modelID = task.Context["model_id"]
	e.providerID = task.Context["provider"]
	return &parallel.AgentResult{
		Success: true,
		Output:  "replayed",
	}, nil
}

func setupReplayStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedReplaySession(t *testing.T, store *storage.Store, id string, messages []storage.Message) {
	t.Helper()
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          id,
		ProjectPath: "/tmp/replay-test",
		Model:       "old-model",
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusCompleted,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range messages {
		msg := messages[i]
		msg.SessionID = id
		if msg.Timestamp.IsZero() {
			msg.Timestamp = now.Add(time.Duration(i) * time.Second)
		}
		if err := store.SaveMessage(&msg); err != nil {
			t.Fatalf("SaveMessage %d: %v", i, err)
		}
	}
}

func newReplayTestReplayer(t *testing.T, store *storage.Store, executor parallel.TaskExecutor) *Replayer {
	t.Helper()
	wt := &mockWorktreeManager{}
	runner := &Runner{
		cfg: RunnerConfig{
			MaxConcurrent:  1,
			DefaultTimeout: time.Second,
			CleanupOnDone:  false,
		},
		parallel: parallel.NewOrchestrator(wt, executor, parallel.Config{
			MaxAgents:       1,
			TaskQueueSize:   10,
			ResultQueueSize: 10,
		}),
		store: NewStoreFromStorage(store),
	}
	replayer, err := NewReplayer(store, runner)
	if err != nil {
		t.Fatalf("NewReplayer: %v", err)
	}
	return replayer
}
