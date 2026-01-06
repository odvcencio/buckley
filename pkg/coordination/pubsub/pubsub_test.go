package pubsub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublishAndReceive tests basic publish and subscribe functionality
func TestPublishAndReceive(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	received := make(chan interface{}, 1)
	handler := func(msg interface{}) {
		received <- msg
	}

	// Subscribe to a topic
	sub, err := ps.Subscribe(ctx, "test.topic", handler)
	require.NoError(t, err)
	require.NotNil(t, sub)
	defer ps.Unsubscribe(ctx, sub)

	// Publish a message
	testMsg := map[string]string{"data": "hello"}
	err = ps.Publish(ctx, "test.topic", testMsg)
	require.NoError(t, err)

	// Verify message received
	select {
	case msg := <-received:
		assert.Equal(t, testMsg, msg)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

// TestMultipleSubscribersToSameTopic tests that multiple subscribers receive the same message
func TestMultipleSubscribersToSameTopic(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	// Create multiple subscribers
	received1 := make(chan interface{}, 1)
	received2 := make(chan interface{}, 1)
	received3 := make(chan interface{}, 1)

	handler1 := func(msg interface{}) { received1 <- msg }
	handler2 := func(msg interface{}) { received2 <- msg }
	handler3 := func(msg interface{}) { received3 <- msg }

	sub1, err := ps.Subscribe(ctx, "broadcast.topic", handler1)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub1)

	sub2, err := ps.Subscribe(ctx, "broadcast.topic", handler2)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub2)

	sub3, err := ps.Subscribe(ctx, "broadcast.topic", handler3)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub3)

	// Publish a message
	testMsg := "broadcast message"
	err = ps.Publish(ctx, "broadcast.topic", testMsg)
	require.NoError(t, err)

	// Verify all subscribers received the message
	timeout := time.After(1 * time.Second)
	select {
	case msg := <-received1:
		assert.Equal(t, testMsg, msg)
	case <-timeout:
		t.Fatal("timeout waiting for subscriber 1")
	}

	select {
	case msg := <-received2:
		assert.Equal(t, testMsg, msg)
	case <-timeout:
		t.Fatal("timeout waiting for subscriber 2")
	}

	select {
	case msg := <-received3:
		assert.Equal(t, testMsg, msg)
	case <-timeout:
		t.Fatal("timeout waiting for subscriber 3")
	}
}

// TestWildcardTopicSubscriptions tests wildcard patterns in topic subscriptions
func TestWildcardTopicSubscriptions(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	received := make(chan interface{}, 10)
	handler := func(msg interface{}) {
		received <- msg
	}

	// Subscribe to wildcard topic
	sub, err := ps.Subscribe(ctx, "task.progress.*", handler)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub)

	// Publish to matching topics
	err = ps.Publish(ctx, "task.progress.plan1", "msg1")
	require.NoError(t, err)

	err = ps.Publish(ctx, "task.progress.plan2", "msg2")
	require.NoError(t, err)

	// Publish to non-matching topic
	err = ps.Publish(ctx, "task.completed.plan1", "msg3")
	require.NoError(t, err)

	// Should receive 2 messages (wildcard matches)
	timeout := time.After(500 * time.Millisecond)
	count := 0
	for count < 2 {
		select {
		case msg := <-received:
			count++
			assert.Contains(t, []interface{}{"msg1", "msg2"}, msg)
		case <-timeout:
			t.Fatalf("timeout, received only %d messages", count)
		}
	}

	// No more messages should be received
	select {
	case msg := <-received:
		t.Fatalf("unexpected message: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Good, no more messages
	}
}

// TestWildcardMultipleSegments tests wildcard matching with multiple segments
func TestWildcardMultipleSegments(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	received := make(chan interface{}, 10)
	handler := func(msg interface{}) {
		received <- msg
	}

	// Subscribe to wildcard topic with multiple segments
	sub, err := ps.Subscribe(ctx, "task.*.plan1.*", handler)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub)

	// Should match
	err = ps.Publish(ctx, "task.progress.plan1.task1", "msg1")
	require.NoError(t, err)

	err = ps.Publish(ctx, "task.completed.plan1.task2", "msg2")
	require.NoError(t, err)

	// Should not match
	err = ps.Publish(ctx, "task.progress.plan2.task1", "msg3")
	require.NoError(t, err)

	err = ps.Publish(ctx, "agent.started.plan1.task1", "msg4")
	require.NoError(t, err)

	// Should receive 2 messages
	timeout := time.After(500 * time.Millisecond)
	count := 0
	for count < 2 {
		select {
		case msg := <-received:
			count++
			assert.Contains(t, []interface{}{"msg1", "msg2"}, msg)
		case <-timeout:
			t.Fatalf("timeout, received only %d messages", count)
		}
	}

	// No more messages
	select {
	case msg := <-received:
		t.Fatalf("unexpected message: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Good
	}
}

// TestUnsubscribeCleanup tests that unsubscribing stops message delivery
func TestUnsubscribeCleanup(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	received := make(chan interface{}, 10)
	handler := func(msg interface{}) {
		received <- msg
	}

	// Subscribe
	sub, err := ps.Subscribe(ctx, "test.topic", handler)
	require.NoError(t, err)

	// Publish message 1
	err = ps.Publish(ctx, "test.topic", "msg1")
	require.NoError(t, err)

	// Verify received
	select {
	case msg := <-received:
		assert.Equal(t, "msg1", msg)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for msg1")
	}

	// Unsubscribe
	err = ps.Unsubscribe(ctx, sub)
	require.NoError(t, err)

	// Publish message 2
	err = ps.Publish(ctx, "test.topic", "msg2")
	require.NoError(t, err)

	// Should not receive msg2
	select {
	case msg := <-received:
		t.Fatalf("unexpected message after unsubscribe: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Good, no message received
	}
}

// TestConcurrentPublishAndSubscribe tests thread safety
func TestConcurrentPublishAndSubscribe(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	var wg sync.WaitGroup
	var msgCount atomic.Int64

	// Create 10 subscribers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			handler := func(msg interface{}) {
				msgCount.Add(1)
			}

			sub, err := ps.Subscribe(ctx, "concurrent.test", handler)
			require.NoError(t, err)
			defer ps.Unsubscribe(ctx, sub)

			// Keep subscription alive
			time.Sleep(500 * time.Millisecond)
		}(i)
	}

	// Wait for subscribers to be ready
	time.Sleep(100 * time.Millisecond)

	// Publish 10 messages concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(msg int) {
			defer wg.Done()
			err := ps.Publish(ctx, "concurrent.test", msg)
			require.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Should have received 10 subscribers * 10 messages = 100 total
	assert.Equal(t, int64(100), msgCount.Load())
}

// TestMessageOrdering tests that messages are delivered in order within a topic
func TestMessageOrdering(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	received := make(chan interface{}, 100)
	handler := func(msg interface{}) {
		received <- msg
	}

	sub, err := ps.Subscribe(ctx, "ordered.topic", handler)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub)

	// Publish messages in order
	for i := 0; i < 10; i++ {
		err = ps.Publish(ctx, "ordered.topic", i)
		require.NoError(t, err)
	}

	// Verify order
	for i := 0; i < 10; i++ {
		select {
		case msg := <-received:
			assert.Equal(t, i, msg, "messages should be in order")
		case <-time.After(1 * time.Second):
			t.Fatalf("timeout waiting for message %d", i)
		}
	}
}

// TestBufferedMessageDelivery tests that the buffer handles bursts
func TestBufferedMessageDelivery(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	receivedCount := atomic.Int64{}
	handler := func(msg interface{}) {
		// Slow consumer
		time.Sleep(10 * time.Millisecond)
		receivedCount.Add(1)
	}

	sub, err := ps.Subscribe(ctx, "buffered.topic", handler)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub)

	// Publish burst of messages
	messageCount := 50
	for i := 0; i < messageCount; i++ {
		err = ps.Publish(ctx, "buffered.topic", i)
		require.NoError(t, err)
	}

	// Wait for all messages to be processed
	assert.Eventually(t, func() bool {
		return receivedCount.Load() == int64(messageCount)
	}, 2*time.Second, 50*time.Millisecond)
}

// TestContextCancellation tests that canceled context stops message delivery
func TestContextCancellation(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx, cancel := context.WithCancel(context.Background())

	received := make(chan interface{}, 10)
	handler := func(msg interface{}) {
		received <- msg
	}

	sub, err := ps.Subscribe(ctx, "cancel.topic", handler)
	require.NoError(t, err)

	// Publish message 1
	err = ps.Publish(ctx, "cancel.topic", "msg1")
	require.NoError(t, err)

	// Verify received
	select {
	case msg := <-received:
		assert.Equal(t, "msg1", msg)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for msg1")
	}

	// Cancel context
	cancel()
	time.Sleep(100 * time.Millisecond)

	// Publish message 2
	err = ps.Publish(context.Background(), "cancel.topic", "msg2")
	require.NoError(t, err)

	// Should not receive msg2 (subscription canceled)
	select {
	case msg := <-received:
		t.Fatalf("unexpected message after cancel: %v", msg)
	case <-time.After(200 * time.Millisecond):
		// Good, no message received
	}

	// Cleanup
	ps.Unsubscribe(context.Background(), sub)
}

// TestTopicPatterns tests the specific topic patterns from ACP spec
func TestTopicPatterns(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	testCases := []struct {
		name          string
		subscription  string
		publishTopics []string
		shouldMatch   []bool
	}{
		{
			name:          "task progress wildcard",
			subscription:  "task.progress.*.*",
			publishTopics: []string{"task.progress.plan1.task1", "task.progress.plan2.task2", "task.completed.plan1.task1"},
			shouldMatch:   []bool{true, true, false},
		},
		{
			name:          "agent event wildcard",
			subscription:  "agent.*.*",
			publishTopics: []string{"agent.started.agent1", "agent.stopped.agent2", "tool.executed.agent1"},
			shouldMatch:   []bool{true, true, false},
		},
		{
			name:          "telemetry category",
			subscription:  "telemetry.*",
			publishTopics: []string{"telemetry.performance", "telemetry.errors", "agent.telemetry.performance"},
			shouldMatch:   []bool{true, true, false},
		},
		{
			name:          "tool event wildcard",
			subscription:  "tool.*.*",
			publishTopics: []string{"tool.started.agent1", "tool.completed.agent2", "tool.failed.agent3", "agent.tool.started"},
			shouldMatch:   []bool{true, true, true, false},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan interface{}, len(tc.publishTopics))
			handler := func(msg interface{}) {
				received <- msg
			}

			sub, err := ps.Subscribe(ctx, tc.subscription, handler)
			require.NoError(t, err)
			defer ps.Unsubscribe(ctx, sub)

			// Publish to all topics
			for i, topic := range tc.publishTopics {
				err = ps.Publish(ctx, topic, i)
				require.NoError(t, err)
			}

			// Count expected matches
			expectedMatches := 0
			for _, match := range tc.shouldMatch {
				if match {
					expectedMatches++
				}
			}

			// Verify correct number of messages received
			timeout := time.After(500 * time.Millisecond)
			receivedMsgs := 0
			for receivedMsgs < expectedMatches {
				select {
				case <-received:
					receivedMsgs++
				case <-timeout:
					t.Fatalf("timeout, expected %d messages but got %d", expectedMatches, receivedMsgs)
				}
			}

			// No more messages should arrive
			select {
			case msg := <-received:
				t.Fatalf("unexpected extra message: %v", msg)
			case <-time.After(100 * time.Millisecond):
				// Good
			}
		})
	}
}

// TestEmptyTopic tests handling of empty topic names
func TestEmptyTopic(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	handler := func(msg interface{}) {}

	// Subscribe with empty topic should fail
	sub, err := ps.Subscribe(ctx, "", handler)
	assert.Error(t, err)
	assert.Nil(t, sub)

	// Publish to empty topic should fail
	err = ps.Publish(ctx, "", "msg")
	assert.Error(t, err)
}

// TestNilHandler tests handling of nil handler
func TestNilHandler(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	sub, err := ps.Subscribe(ctx, "test.topic", nil)
	assert.Error(t, err)
	assert.Nil(t, sub)
}

// TestDoubleUnsubscribe tests that double unsubscribe is safe
func TestDoubleUnsubscribe(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	handler := func(msg interface{}) {}

	sub, err := ps.Subscribe(ctx, "test.topic", handler)
	require.NoError(t, err)

	// First unsubscribe
	err = ps.Unsubscribe(ctx, sub)
	require.NoError(t, err)

	// Second unsubscribe should not error
	err = ps.Unsubscribe(ctx, sub)
	assert.NoError(t, err)
}

// TestSubscriptionMethods tests the Subscription interface methods
func TestSubscriptionMethods(t *testing.T) {
	ps := NewInMemoryPubSub()
	ctx := context.Background()

	handler := func(msg interface{}) {}

	sub, err := ps.Subscribe(ctx, "test.topic.pattern", handler)
	require.NoError(t, err)
	defer ps.Unsubscribe(ctx, sub)

	// Verify ID is not empty
	assert.NotEmpty(t, sub.ID())

	// Verify topic matches
	assert.Equal(t, "test.topic.pattern", sub.Topic())
}
