package subagent

import (
	"context"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func BenchmarkManagerStatus(b *testing.B) {
	started := make(chan struct{})
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, _ func(int)) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	defer manager.Close()
	run, err := manager.Spawn("worker", "", "benchmark", 0)
	if err != nil {
		b.Fatal(err)
	}
	<-started
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := manager.Status(run.ID); !ok {
			b.Fatal("subagent disappeared")
		}
	}
}

func BenchmarkManagerDeliver(b *testing.B) {
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, _ func(int), commands <-chan CommandDelivery) (string, error) {
		for {
			select {
			case delivery := <-commands:
				delivery.Acknowledge(nil)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}), 1)
	defer manager.Close()
	run, err := manager.Spawn("worker", "", "benchmark", 0)
	if err != nil {
		b.Fatal(err)
	}
	message := agentcoord.Message{RunID: run.ID, To: run.ID, From: "parent", Content: "continue"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := manager.Deliver(context.Background(), run.ID, message); err != nil {
			b.Fatal(err)
		}
	}
}
