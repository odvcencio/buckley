package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestRunExperimentReplay_Result(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		response   string
		wantStatus experiment.RunStatus
		wantError  string
	}{
		{"completed", http.StatusOK, `{"id":"synthetic-replay","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"fixture-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`, experiment.RunCompleted, ""},
		{"rejected", http.StatusBadRequest, `{"error":{"message":"synthetic replay rejected","type":"invalid_request_error"}}`, experiment.RunFailed, "synthetic replay rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initTempGitRepo(t)
			t.Chdir(repo)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				_, _ = io.WriteString(w, tc.response)
			}))
			t.Cleanup(server.Close)
			store, err := storage.New(filepath.Join(t.TempDir(), "replay.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			now := time.Now()
			if err := store.CreateSession(&storage.Session{ID: "source", Status: storage.SessionStatusPaused, CreatedAt: now, LastActive: now}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveMessage(&storage.Message{SessionID: "source", Role: "user", Content: "Reply fixture-ok only.", Timestamp: now}); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Providers.OpenRouter.Enabled = false
			cfg.Providers.OpenAI.Enabled = true
			cfg.Providers.OpenAI.APIKey = "synthetic-key"
			cfg.Providers.OpenAI.BaseURL = server.URL
			cfg.Models.DefaultProvider = "openai"
			cfg.Models.Execution = "gpt-4o"
			cfg.Experiment.Enabled = true
			cfg.Experiment.WorktreeRoot = t.TempDir()
			cfg.Experiment.CleanupOnDone = true
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			original := initDependenciesFn
			t.Cleanup(func() { initDependenciesFn = original })
			initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
				return cfg, mgr, store, nil
			}
			err = runExperimentReplay([]string{"source", "--model", "gpt-4o", "--provider", "openai", "--live-tools"})
			if requests.Load() != 1 {
				t.Fatalf("provider calls = %d, want one", requests.Load())
			}
			experiments := experiment.NewStoreFromStorage(store)
			stored, loadErr := experiments.ListExperiments(1, "")
			if loadErr != nil || len(stored) != 1 {
				t.Fatalf("stored experiments = %+v, error = %v", stored, loadErr)
			}
			runs, loadErr := experiments.ListRuns(stored[0].ID)
			if loadErr != nil || len(runs) != 1 || runs[0].Status != tc.wantStatus {
				t.Fatalf("stored runs = %+v, error = %v, want %s", runs, loadErr, tc.wantStatus)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("replay error = %v, want %q", err, tc.wantError)
				}
			} else if err != nil || runs[0].Output != "fixture-ok" {
				t.Fatalf("replay error = %v, output = %q", err, runs[0].Output)
			}
		})
	}
}

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
