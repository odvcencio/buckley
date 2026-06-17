//go:build integration && manual

package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestHeadlessQwenFlashMultiTurnConversation(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY not set")
	}

	modelID := strings.TrimSpace(os.Getenv("BUCKLEY_QWEN_FLASH_MODEL"))
	if modelID == "" {
		modelID = "qwen/qwen3.6-flash"
	}

	cfg := config.DefaultConfig()
	cfg.Models.Execution = modelID
	cfg.Models.Planning = modelID
	cfg.Models.Review = modelID
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.Reasoning = "off"
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = apiKey

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("model manager initialize: %v", err)
	}

	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	sessionID := "qwen-flash-multiturn"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         sessionID,
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	runner, err := headless.NewRunner(headless.RunnerConfig{
		Session:      &storage.Session{ID: sessionID},
		ModelManager: mgr,
		Store:        store,
		Config:       cfg,
		SystemPrompt: "You are a concise test assistant. Preserve conversation facts exactly. Do not use tools.",
	})
	if err != nil {
		t.Fatalf("headless.NewRunner: %v", err)
	}
	t.Cleanup(runner.Stop)

	marker := "qwen-flash-memory-marker-7319"
	sendAndWaitForAssistant(t, runner, store, sessionID, 1, "Remember this marker for the next turn: "+marker+". Reply with only: stored")
	sendAndWaitForAssistant(t, runner, store, sessionID, 2, "What marker did I ask you to remember? Reply with only the marker.")

	messages, err := store.GetAllMessages(sessionID)
	if err != nil {
		t.Fatalf("GetAllMessages: %v", err)
	}
	last := lastAssistantContent(messages)
	if !strings.Contains(last, marker) {
		t.Fatalf("expected final assistant message to contain %q, got %q", marker, last)
	}
}

func sendAndWaitForAssistant(t *testing.T, runner *headless.Runner, store *storage.Store, sessionID string, wantAssistantCount int, content string) {
	t.Helper()
	if err := runner.HandleSessionCommand(command.SessionCommand{
		SessionID: sessionID,
		Type:      "input",
		Content:   content,
	}); err != nil {
		t.Fatalf("HandleSessionCommand: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := store.GetAllMessages(sessionID)
		if err != nil {
			t.Fatalf("GetAllMessages: %v", err)
		}
		if countAssistantMessages(messages) >= wantAssistantCount {
			return
		}
		if runner.State() == headless.StateError {
			t.Fatalf("runner entered error state before assistant response")
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for assistant message %d", wantAssistantCount)
}

func countAssistantMessages(messages []storage.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == "assistant" {
			count++
		}
	}
	return count
}

func lastAssistantContent(messages []storage.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Content
		}
	}
	return ""
}
