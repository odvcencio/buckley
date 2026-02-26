package tui

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

type hookableStrategy struct {
	progress execution.ProgressReporter
	notifier execution.ToastNotifier
}

func (s *hookableStrategy) Execute(context.Context, execution.ExecutionRequest) (*execution.ExecutionResult, error) {
	return &execution.ExecutionResult{}, nil
}

func (s *hookableStrategy) Name() string {
	return "hookable"
}

func (s *hookableStrategy) SupportsStreaming() bool {
	return false
}

func (s *hookableStrategy) SetProgressReporter(reporter execution.ProgressReporter) {
	s.progress = reporter
}

func (s *hookableStrategy) SetToastNotifier(notifier execution.ToastNotifier) {
	s.notifier = notifier
}

func TestAttachStrategyUIHooks_SetsAdapters(t *testing.T) {
	strategy := &hookableStrategy{}
	progressMgr := progress.NewProgressManager()
	toastMgr := toast.NewToastManager()

	attachStrategyUIHooks(strategy, progressMgr, toastMgr)

	if strategy.progress == nil {
		t.Fatal("expected progress reporter adapter")
	}
	if strategy.notifier == nil {
		t.Fatal("expected toast notifier adapter")
	}
}

func TestAttachStrategyUIHooks_NilInputs(t *testing.T) {
	strategy := &hookableStrategy{}

	attachStrategyUIHooks(strategy, nil, nil)

	if strategy.progress != nil {
		t.Fatal("expected nil progress reporter when manager is nil")
	}
	if strategy.notifier != nil {
		t.Fatal("expected nil toast notifier when manager is nil")
	}
}
