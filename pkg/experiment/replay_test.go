package experiment

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
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
			cfg:     ReplayConfig{SourceSessionID: "nonexistent", NewModelID: "gpt-4"},
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
	})
	if err == nil {
		t.Error("Replay() on nil replayer should return error")
	}
	if err.Error() != "replayer unavailable" {
		t.Errorf("Replay() error = %q, want 'replayer unavailable'", err.Error())
	}
}
