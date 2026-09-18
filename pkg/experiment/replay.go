package experiment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/storage"
)

// ReplayConfig specifies how to replay a session.
type ReplayConfig struct {
	SourceSessionID    string
	NewModelID         string
	NewProviderID      string
	NewSystemPrompt    *string
	NewTemperature     *float64
	DeterministicTools bool
	AllowLiveTools     bool
}

// ErrDeterministicReplayUnsupported is returned before runtime setup because
// Buckley does not yet have a deterministic tool replay engine.
var ErrDeterministicReplayUnsupported = errors.New("deterministic experiment replay is not supported")

// ErrLiveReplayRequiresOptIn is returned when a replay would rerun live tools
// and model execution without an explicit opt-in.
var ErrLiveReplayRequiresOptIn = errors.New("experiment replay requires --live-tools")

// Replayer replays a stored session with new configuration.
type Replayer struct {
	store  *storage.Store
	runner *Runner
}

// NewReplayer constructs a replayer.
func NewReplayer(store *storage.Store, runner *Runner) (*Replayer, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if runner == nil {
		return nil, errors.New("runner is required")
	}
	return &Replayer{store: store, runner: runner}, nil
}

// Replay re-executes a session with updated model configuration.
func (r *Replayer) Replay(ctx context.Context, cfg ReplayConfig) (*Run, error) {
	sourceID := strings.TrimSpace(cfg.SourceSessionID)
	if sourceID == "" {
		return nil, errors.New("source session id is required")
	}
	modelID := strings.TrimSpace(cfg.NewModelID)
	if modelID == "" {
		return nil, errors.New("new model id is required")
	}
	if cfg.DeterministicTools {
		return nil, ErrDeterministicReplayUnsupported
	}
	if !cfg.AllowLiveTools {
		return nil, ErrLiveReplayRequiresOptIn
	}
	if r == nil || r.store == nil || r.runner == nil {
		return nil, errors.New("replayer unavailable")
	}

	session, err := r.store.GetSession(sourceID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found: %s", sourceID)
	}

	messages, err := r.store.GetAllMessages(sourceID)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}

	var prompt string
	for _, msg := range messages {
		if msg.Role == "user" {
			if msg.IsTruncated {
				return nil, errors.New("first user prompt is truncated and cannot be replayed")
			}
			if strings.TrimSpace(msg.Content) == "" {
				return nil, errors.New("first user prompt is blank and cannot be replayed")
			}
			prompt = msg.Content
			break
		}
	}
	if prompt == "" {
		return nil, errors.New("no user prompts found in session")
	}

	exp := &Experiment{
		ID:   ulid.Make().String(),
		Name: fmt.Sprintf("Live replay %s with %s", shortReplaySourceID(sourceID), modelID),
		Task: Task{
			Prompt: prompt,
			Context: map[string]string{
				"source_session_id": sourceID,
			},
		},
		Variants: []Variant{{
			ID:           ulid.Make().String(),
			Name:         "replay",
			ModelID:      modelID,
			ProviderID:   cfg.NewProviderID,
			SystemPrompt: cfg.NewSystemPrompt,
			Temperature:  cfg.NewTemperature,
		}},
	}

	results, err := r.runner.RunExperiment(ctx, exp)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errors.New("no results returned")
	}

	expStore := NewStoreFromStorage(r.store)
	if expStore == nil {
		return nil, errors.New("experiment store unavailable")
	}
	runs, err := expStore.ListRuns(exp.ID)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, errors.New("no runs stored for replay")
	}

	return &runs[0], nil
}

func shortReplaySourceID(sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	runes := []rune(sourceID)
	if len(runes) <= 8 {
		return sourceID
	}
	return string(runes[:8])
}
