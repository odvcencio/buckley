package headless

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestRegistryLegacyInitialPromptDoesNotExposeDurableReceipt(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, Emitter: capture,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Principal: "alice", Project: root, Prompt: "legacy initial prompt",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.InitialReceipt != nil {
		t.Fatalf("legacy session returned durable receipt: %+v", info.InitialReceipt)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		events := append([]RunnerEvent(nil), capture.runnerEvents...)
		capture.mu.Unlock()
		for _, event := range events {
			if event.Type != EventCommandQueued {
				continue
			}
			commandID, _ := event.Data["commandId"].(string)
			if commandID == "" {
				t.Fatalf("legacy initial prompt reached queue without command ID: %+v", event)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("legacy initial prompt did not reach command queue")
}
