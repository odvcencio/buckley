package tui

import (
	"fmt"
	"sync"
	"testing"
)

func TestRunner_ConcurrentStateUpdates(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	const (
		goroutines = 8
		iterations = 150
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				runner.SetStatus(fmt.Sprintf("g%d-%d", g, i))
				runner.SetStreaming(i%2 == 0)
				runner.SetModelName(fmt.Sprintf("model-%d", g))
				runner.SetSessionID(fmt.Sprintf("session-%d", i%3))
				runner.SetTokenCount(i, float64(i)/100.0)
				runner.SetContextUsage(i*2, i*2+100, i*4+1000)
				runner.AddMessage(fmt.Sprintf("msg-%d-%d", g, i), "assistant")
				runner.AppendToLastMessage("!")
				runner.ShowThinkingIndicator()
				runner.RemoveThinkingIndicator()
				runner.StreamChunk("session-shared", "x")
				if i%10 == 0 {
					runner.StreamEnd("session-shared", "")
				}
			}
		}()
	}
	wg.Wait()

	// Final flush marker should be safe after concurrent activity.
	runner.StreamEnd("session-shared", "")

	messages := runner.state.ChatMessages.Get()
	if len(messages) == 0 {
		t.Fatal("expected messages after concurrent updates")
	}
}
