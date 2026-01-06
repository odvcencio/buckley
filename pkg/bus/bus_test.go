package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryBus_PublishSubscribe(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	received := make(chan *Message, 1)

	sub, err := bus.Subscribe(ctx, "test.subject", func(msg *Message) []byte {
		received <- msg
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	err = bus.Publish(ctx, "test.subject", []byte("hello"))
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case msg := <-received:
		if string(msg.Data) != "hello" {
			t.Errorf("Expected 'hello', got %q", string(msg.Data))
		}
		if msg.Subject != "test.subject" {
			t.Errorf("Expected subject 'test.subject', got %q", msg.Subject)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestMemoryBus_Wildcard(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	var received atomic.Int32

	// Subscribe to wildcard pattern
	sub, err := bus.Subscribe(ctx, "buckley.agent.*", func(msg *Message) []byte {
		received.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish to matching subjects
	bus.Publish(ctx, "buckley.agent.abc", []byte("1"))
	bus.Publish(ctx, "buckley.agent.xyz", []byte("2"))
	bus.Publish(ctx, "buckley.other.abc", []byte("3")) // Should not match

	time.Sleep(100 * time.Millisecond)

	if received.Load() != 2 {
		t.Errorf("Expected 2 messages, got %d", received.Load())
	}
}

func TestMemoryBus_WildcardGreaterThan(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	var received atomic.Int32

	// Subscribe with > wildcard (matches multiple tokens)
	sub, err := bus.Subscribe(ctx, "buckley.>", func(msg *Message) []byte {
		received.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	bus.Publish(ctx, "buckley.agent.abc", []byte("1"))
	bus.Publish(ctx, "buckley.task.123.events", []byte("2"))
	bus.Publish(ctx, "other.thing", []byte("3")) // Should not match

	time.Sleep(100 * time.Millisecond)

	if received.Load() != 2 {
		t.Errorf("Expected 2 messages, got %d", received.Load())
	}
}

func TestMemoryBus_RequestReply(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()

	// Set up responder
	sub, err := bus.Subscribe(ctx, "echo", func(msg *Message) []byte {
		return append([]byte("echo: "), msg.Data...)
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Make request
	reply, err := bus.Request(ctx, "echo", []byte("hello"), time.Second)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if string(reply) != "echo: hello" {
		t.Errorf("Expected 'echo: hello', got %q", string(reply))
	}
}

func TestMemoryBus_RequestTimeout(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()

	// No subscriber, should timeout
	_, err := bus.Request(ctx, "nonexistent", []byte("hello"), 100*time.Millisecond)
	if err != ErrNoResponders {
		t.Errorf("Expected ErrNoResponders, got %v", err)
	}
}

func TestMemoryBus_MultipleSubscribers(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	var count atomic.Int32

	// Multiple subscribers to same subject
	for i := 0; i < 3; i++ {
		sub, _ := bus.Subscribe(ctx, "fanout", func(msg *Message) []byte {
			count.Add(1)
			return nil
		})
		defer sub.Unsubscribe()
	}

	bus.Publish(ctx, "fanout", []byte("broadcast"))
	time.Sleep(100 * time.Millisecond)

	if count.Load() != 3 {
		t.Errorf("Expected 3 subscribers to receive message, got %d", count.Load())
	}
}

func TestMemoryBus_Unsubscribe(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	var received atomic.Int32

	sub, _ := bus.Subscribe(ctx, "test", func(msg *Message) []byte {
		received.Add(1)
		return nil
	})

	bus.Publish(ctx, "test", []byte("1"))
	time.Sleep(50 * time.Millisecond)

	sub.Unsubscribe()

	bus.Publish(ctx, "test", []byte("2"))
	time.Sleep(50 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("Expected 1 message after unsubscribe, got %d", received.Load())
	}
}

func TestMemoryQueue_PushPull(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	queue := bus.Queue("test-queue")

	// Push tasks
	for i := 0; i < 5; i++ {
		err := queue.Push(ctx, []byte{byte(i)})
		if err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	length, _ := queue.Len(ctx)
	if length != 5 {
		t.Errorf("Expected queue length 5, got %d", length)
	}

	// Pull tasks
	for i := 0; i < 5; i++ {
		task, err := queue.Pull(ctx)
		if err != nil {
			t.Fatalf("Pull failed: %v", err)
		}
		if task.Data[0] != byte(i) {
			t.Errorf("Expected task data %d, got %d", i, task.Data[0])
		}
		queue.Ack(ctx, task.ID)
	}
}

func TestMemoryQueue_Nack(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	queue := bus.Queue("nack-queue")

	queue.Push(ctx, []byte("task1"))

	// Pull and nack
	task, _ := queue.Pull(ctx)
	queue.Nack(ctx, task.ID)

	// Should be able to pull again
	task2, err := queue.Pull(ctx)
	if err != nil {
		t.Fatalf("Second pull failed: %v", err)
	}
	if string(task2.Data) != "task1" {
		t.Errorf("Expected same task after nack")
	}
}

func TestMemoryQueue_ConcurrentWorkers(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()

	ctx := context.Background()
	queue := bus.Queue("concurrent-queue")

	taskCount := 100
	for i := 0; i < taskCount; i++ {
		queue.Push(ctx, []byte{byte(i)})
	}

	var processed atomic.Int32
	var wg sync.WaitGroup

	// Spin up workers
	workerCount := 5
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				task, err := queue.Pull(ctx)
				cancel()
				if err != nil {
					return
				}
				processed.Add(1)
				queue.Ack(ctx, task.ID)
			}
		}()
	}

	wg.Wait()

	if processed.Load() != int32(taskCount) {
		t.Errorf("Expected %d processed tasks, got %d", taskCount, processed.Load())
	}
}

func TestMatchSubject(t *testing.T) {
	tests := []struct {
		pattern string
		subject string
		want    bool
	}{
		{"foo", "foo", true},
		{"foo", "bar", false},
		{"foo.bar", "foo.bar", true},
		{"foo.bar", "foo.baz", false},
		{"foo.*", "foo.bar", true},
		{"foo.*", "foo.bar.baz", false},
		{"foo.>", "foo.bar", true},
		{"foo.>", "foo.bar.baz", true},
		{"*.bar", "foo.bar", true},
		{"*.bar", "baz.bar", true},
		{"*.bar", "foo.baz", false},
		{"buckley.agent.*", "buckley.agent.abc", true},
		{"buckley.agent.*", "buckley.agent", false},
		{"buckley.>", "buckley.agent.abc.xyz", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.subject, func(t *testing.T) {
			got := matchSubject(tt.pattern, tt.subject)
			if got != tt.want {
				t.Errorf("matchSubject(%q, %q) = %v, want %v", tt.pattern, tt.subject, got, tt.want)
			}
		})
	}
}

func TestMemoryBus_ClosedOperations(t *testing.T) {
	bus := NewMemoryBus()
	bus.Close()

	ctx := context.Background()

	if err := bus.Publish(ctx, "test", []byte("data")); err != ErrClosed {
		t.Errorf("Expected ErrClosed on publish, got %v", err)
	}

	if _, err := bus.Subscribe(ctx, "test", nil); err != ErrClosed {
		t.Errorf("Expected ErrClosed on subscribe, got %v", err)
	}

	if _, err := bus.Request(ctx, "test", nil, time.Second); err != ErrClosed {
		t.Errorf("Expected ErrClosed on request, got %v", err)
	}
}
