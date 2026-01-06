package orchestrator

import (
	"errors"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/personality"
)

type failingReviewer struct{}

func (f *failingReviewer) Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error) {
	return nil, errors.New("boom")
}

func (f *failingReviewer) SetPersonaProvider(provider *personality.PersonaProvider) {}

func TestExecutorReviewBalancedSkipsOnReviewerError(t *testing.T) {
	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "balanced"},
		},
		reviewer: &failingReviewer{},
	}

	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err != nil {
		t.Fatalf("expected balanced review to skip errors, got %v", err)
	}
}

func TestExecutorReviewConservativePropagatesReviewerError(t *testing.T) {
	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "conservative"},
		},
		reviewer: &failingReviewer{},
	}

	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err == nil {
		t.Fatalf("expected conservative review to propagate errors")
	}
}
