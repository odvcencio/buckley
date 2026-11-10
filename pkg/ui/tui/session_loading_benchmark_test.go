package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/storage"
)

func BenchmarkControllerSessionLoading(b *testing.B) {
	workDir := b.TempDir()
	b.Setenv("HOME", b.TempDir())
	store, err := storage.New(filepath.Join(b.TempDir(), "sessions.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("session-%d", i)
		if err := store.CreateSession(&storage.Session{ID: id, ProjectPath: workDir, Status: storage.SessionStatusActive, CreatedAt: time.Now(), LastActive: time.Now()}); err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 128; j++ {
			if err := store.SaveMessage(&storage.Message{SessionID: id, Role: "tool", Content: strings.Repeat("x", 64*1024), Timestamp: time.Now()}); err != nil {
				b.Fatal(err)
			}
		}
	}
	cfg := ControllerConfig{Config: config.DefaultConfig(), Store: store}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
		if err != nil {
			b.Fatal(err)
		}
		var historyBytes int64
		for _, sess := range sessions {
			for _, msg := range sess.Conversation.Messages {
				if text, ok := msg.Content.(string); ok {
					historyBytes += int64(len(text))
				}
			}
			if sess.HookCloser != nil {
				_ = sess.HookCloser.Close()
			}
			_ = sess.ToolRegistry.Close()
		}
		if len(sessions[current].Conversation.Messages) != 128 {
			b.Fatal("selected session lost history")
		}
		b.ReportMetric(float64(historyBytes), "history-B")
	}
}
