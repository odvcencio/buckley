package orchestrator

import (
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/personality"
)

type failingReviewer struct{}

func (f *failingReviewer) Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error) {
	return nil, errors.New("boom")
}

func (f *failingReviewer) SetPersonaProvider(provider *personality.PersonaProvider) {}

type incompleteReviewer struct{}

func (r *incompleteReviewer) Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error) {
	return nil, NewIncompleteReviewError("public draft", "length", errors.New("raw provider sentinel"))
}

func (r *incompleteReviewer) SetPersonaProvider(provider *personality.PersonaProvider) {}

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

func TestExecutorReviewBalancedPropagatesIncompleteReview(t *testing.T) {
	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "balanced"},
		},
		reviewer: &incompleteReviewer{},
	}

	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	var incomplete *IncompleteReviewError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected balanced review to propagate incomplete review, got %v", err)
	}
	if err.Error() != "review response incomplete" {
		t.Fatalf("error = %q, want stable incomplete review text", err.Error())
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
