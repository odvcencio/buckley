package experiment

import (
	"context"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func TestReplay_ClockRollbackKeepsOriginalUserPrompt(t *testing.T) {
	store := setupReplayStore(t)
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	seedReplaySession(t, store, "rollback-source", []storage.Message{
		{Role: "system", Content: "synthetic instructions", Timestamp: base},
		{Role: "user", Content: "original request", Timestamp: base.Add(time.Hour)},
		{Role: "assistant", Content: "original result", Timestamp: base.Add(time.Hour + time.Minute)},
		{Role: "user", Content: "later request after clock rollback", Timestamp: base.Add(-time.Hour)},
	})
	executor := &capturingReplayExecutor{}
	replayer := newReplayTestReplayer(t, store, executor)
	run, err := replayer.Replay(context.Background(), ReplayConfig{SourceSessionID: "rollback-source", NewModelID: "new-model", AllowLiveTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || executor.calls != 1 || executor.prompt != "original request" {
		t.Fatalf("run=%+v calls=%d prompt=%q", run, executor.calls, executor.prompt)
	}
}
