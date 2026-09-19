package main

import (
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestRunExperimentReplayRejectsWithoutLiveToolsBeforeDependencies(t *testing.T) {
	original := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = original })

	var initCalls int
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		initCalls++
		return nil, nil, nil, errors.New("init should not run")
	}

	for _, args := range [][]string{
		{"source-session", "-m", "new-model"},
		{"-m", "new-model", "source-session"},
	} {
		err := runExperimentReplay(args)
		if !errors.Is(err, experiment.ErrLiveReplayRequiresOptIn) ||
			!strings.Contains(err.Error(), "--live-tools") ||
			!strings.Contains(err.Error(), "live rerun of the first stored user prompt on the current baseline") {
			t.Fatalf("runExperimentReplay(%v) error = %v, want live-tools opt-in message", args, err)
		}
	}
	if initCalls != 0 {
		t.Fatalf("init calls = %d, want zero before --live-tools opt-in", initCalls)
	}
}

func TestRunExperimentReplayRejectsDeterministicBeforeDependencies(t *testing.T) {
	original := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = original })

	var initCalls int
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		initCalls++
		return nil, nil, nil, errors.New("init should not run")
	}

	err := runExperimentReplay([]string{"source-session", "-m", "new-model", "--deterministic-tools", "--live-tools"})
	if !errors.Is(err, experiment.ErrDeterministicReplayUnsupported) ||
		!strings.Contains(err.Error(), "deterministic tool replay is not implemented") {
		t.Fatalf("runExperimentReplay() error = %v, want deterministic unsupported", err)
	}
	if initCalls != 0 {
		t.Fatalf("init calls = %d, want zero for unsupported deterministic replay", initCalls)
	}
}

func TestRunExperimentReplayLiveToolsPassesEarlyGate(t *testing.T) {
	original := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = original })

	sentinel := errors.New("init reached")
	var initCalls int
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		initCalls++
		return nil, nil, nil, sentinel
	}

	err := runExperimentReplay([]string{"--live-tools", "-m", "new-model", "source-session"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runExperimentReplay() error = %v, want init sentinel", err)
	}
	if initCalls != 1 {
		t.Fatalf("init calls = %d, want one after live-tools opt-in", initCalls)
	}
}

func TestRunExperimentReplayRejectsTrailingArgsBeforeDependencies(t *testing.T) {
	original := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = original })

	var initCalls int
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		initCalls++
		return nil, nil, nil, errors.New("init should not run")
	}

	for _, args := range [][]string{
		{"source-session", "--live-tools", "-m", "new-model", "extra"},
		{"--live-tools", "-m", "new-model", "source-session", "extra"},
	} {
		err := runExperimentReplay(args)
		if err == nil || !strings.Contains(err.Error(), "unexpected trailing argument: extra") {
			t.Fatalf("runExperimentReplay(%v) error = %v, want trailing arg rejection", args, err)
		}
	}
	if initCalls != 0 {
		t.Fatalf("init calls = %d, want zero after parse rejection", initCalls)
	}
}
