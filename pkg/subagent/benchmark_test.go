package subagent

import (
	"context"
	"testing"
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
