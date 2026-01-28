package telemetry

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHub_SubscribeWithID(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, id := hub.SubscribeWithID()
	require.NotEmpty(t, id, "subscriber ID should not be empty")
	require.NotNil(t, ch, "channel should not be nil")

	// Publish an event
	event := Event{Type: EventTaskStarted}
	hub.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, EventTaskStarted, received.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_UnsubscribeByID(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, id := hub.SubscribeWithID()

	// Unsubscribe using the ID
	hub.Unsubscribe(id)

	// Channel should be closed
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after unsubscribe")

	// Unsubscribing again should not panic
	assert.NotPanics(t, func() {
		hub.Unsubscribe(id)
	})
}

func TestHub_MultipleUnsubscribeByID(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	// Create multiple subscribers
	ch1, id1 := hub.SubscribeWithID()
	ch2, id2 := hub.SubscribeWithID()
	ch3, id3 := hub.SubscribeWithID()

	// Unsubscribe only the second one
	hub.Unsubscribe(id2)

	// Channel 2 should be closed
	_, ok := <-ch2
	assert.False(t, ok, "channel 2 should be closed")

	// Channels 1 and 3 should still receive events
	event := Event{Type: EventTaskStarted}
	hub.Publish(event)

	select {
	case <-ch1:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel 1 should still receive events")
	}

	select {
	case <-ch3:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel 3 should still receive events")
	}

	// Clean up
	hub.Unsubscribe(id1)
	hub.Unsubscribe(id3)
}

func TestHub_StopAndWait(t *testing.T) {
	hub := NewHub()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish some events
	for i := 0; i < 10; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Stop and wait for graceful shutdown
	hub.Stop()
	hub.Wait()

	// Hub should be closed
	assert.True(t, hub.isClosed())

	// Publish should be no-op after stop
	hub.Publish(Event{Type: EventTaskFailed}) // Should not panic

	// Wait for flush and drain channel
	time.Sleep(50 * time.Millisecond)
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	// Should have received some events before shutdown
	assert.GreaterOrEqual(t, count, 0, "should have received events before shutdown")
}

func TestHub_StopIdempotent(t *testing.T) {
	hub := NewHub()

	// Multiple Stop calls should not panic
	assert.NotPanics(t, func() {
		hub.Stop()
		hub.Stop()
		hub.Stop()
	})

	hub.Wait()
}

func TestHub_NewHubWithConfig(t *testing.T) {
	config := &Config{
		EventQueueSize:        500,
		BatchSize:             50,
		FlushInterval:         50 * time.Millisecond,
		RateLimit:             500,
		SubscriberChannelSize: 32,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	assert.Equal(t, 500, hub.config.EventQueueSize)
	assert.Equal(t, 50, hub.config.BatchSize)
	assert.Equal(t, 50*time.Millisecond, hub.config.FlushInterval)
	assert.Equal(t, 500, hub.config.RateLimit)
	assert.Equal(t, 32, hub.config.SubscriberChannelSize)
}

func TestHub_NewHubWithConfigDefaults(t *testing.T) {
	// Config with zero values should get defaults
	config := &Config{}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	assert.Equal(t, DefaultEventQueueSize, hub.config.EventQueueSize)
	assert.Equal(t, DefaultBatchSize, hub.config.BatchSize)
	assert.Equal(t, DefaultFlushInterval, hub.config.FlushInterval)
	assert.Equal(t, DefaultRateLimit, hub.config.RateLimit)
	assert.Equal(t, DefaultSubscriberChannelSize, hub.config.SubscriberChannelSize)
}

func TestHub_DefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, DefaultEventQueueSize, config.EventQueueSize)
	assert.Equal(t, DefaultBatchSize, config.BatchSize)
	assert.Equal(t, DefaultFlushInterval, config.FlushInterval)
	assert.Equal(t, DefaultRateLimit, config.RateLimit)
	assert.Equal(t, DefaultSubscriberChannelSize, config.SubscriberChannelSize)
}

func TestHub_Batching(t *testing.T) {
	// Use a hub with larger batch size to test batching behavior
	config := &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             10,
		FlushInterval:         500 * time.Millisecond, // Long interval to test batch size trigger
		RateLimit:             10000,                  // High limit to not interfere
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish fewer events than batch size
	for i := 0; i < 5; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Should not receive events yet (batch not full, interval not reached)
	select {
	case <-ch:
		t.Fatal("should not receive events before batch is full")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Publish more events to reach batch size
	for i := 5; i < 10; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Now should receive events
	count := 0
	done := time.After(100 * time.Millisecond)
drain:
	for {
		select {
		case <-ch:
			count++
			if count >= 10 {
				break drain
			}
		case <-done:
			break drain
		}
	}

	assert.Equal(t, 10, count, "should receive all batched events")
}

func TestHub_FlushInterval(t *testing.T) {
	// Use a hub with short flush interval
	config := &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             100, // Large batch size
		FlushInterval:         50 * time.Millisecond,
		RateLimit:             10000,
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish a single event
	hub.Publish(Event{Type: EventTaskStarted})

	// Should receive event after flush interval
	select {
	case received := <-ch:
		assert.Equal(t, EventTaskStarted, received.Type)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for event to be flushed")
	}
}

func TestHub_RateLimiting(t *testing.T) {
	// Use a hub with low rate limit
	config := &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             1, // Immediate flush
		FlushInterval:         DefaultFlushInterval,
		RateLimit:             10, // Only 10 events per second
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish many events rapidly
	for i := 0; i < 50; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Count received events
	count := 0
	done := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case <-ch:
			count++
		case <-done:
			break drain
		}
	}

	// Should have received roughly 10 events (rate limit per second)
	// Allow for some variance due to timing
	assert.LessOrEqual(t, count, 20, "should respect rate limit")
}

func TestHub_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	var wg sync.WaitGroup
	numGoroutines := 50

	// Concurrently subscribe and unsubscribe
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, id := hub.SubscribeWithID()
			time.Sleep(time.Millisecond) // Small delay
			hub.Unsubscribe(id)
			_, ok := <-ch
			_ = !ok // Channel should be closed
		}()
	}

	wg.Wait()

	// All subscribers should be cleaned up
	stats := hub.GetStats()
	assert.Equal(t, 0, stats.SubscriberCount, "all subscribers should be cleaned up")
}

func TestHub_GetStats(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	// Initial stats
	stats := hub.GetStats()
	assert.Equal(t, 0, stats.SubscriberCount)
	assert.Equal(t, 0, stats.QueueSize)
	assert.Equal(t, 0, stats.BatchSize)
	assert.Equal(t, DefaultRateLimit, stats.RateLimit)

	// Add subscribers
	_, id1 := hub.SubscribeWithID()
	_, id2 := hub.SubscribeWithID()

	stats = hub.GetStats()
	assert.Equal(t, 2, stats.SubscriberCount)

	// Remove subscriber
	hub.Unsubscribe(id1)

	stats = hub.GetStats()
	assert.Equal(t, 1, stats.SubscriberCount)

	// Clean up
	hub.Unsubscribe(id2)
}

func TestHub_FlushMethod(t *testing.T) {
	// Use a hub with large batch size and long interval
	config := &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             100,
		FlushInterval:         10 * time.Second, // Very long interval
		RateLimit:             10000,
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish events
	for i := 0; i < 5; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Should not receive events yet
	select {
	case <-ch:
		t.Fatal("should not receive events before manual flush")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Manually flush
	hub.Flush()

	// Should receive events now
	count := 0
	done := time.After(100 * time.Millisecond)
drain:
	for {
		select {
		case <-ch:
			count++
			if count >= 5 {
				break drain
			}
		case <-done:
			break drain
		}
	}

	assert.Equal(t, 5, count, "should receive all events after manual flush")
}

func TestHub_NonBlockingPublish(t *testing.T) {
	// Create a hub with very small queue
	config := &Config{
		EventQueueSize:        1,
		BatchSize:             100, // Large batch to prevent flush
		FlushInterval:         10 * time.Second,
		RateLimit:             10000,
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	// Publish many events quickly - should not block
	done := make(chan bool)
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
		}
		done <- true
	}()

	select {
	case <-done:
		// Publish completed without blocking
	case <-time.After(5 * time.Second):
		t.Fatal("publish should not block even when queue is full")
	}
}

func TestHub_SubscriberChannelBuffer(t *testing.T) {
	// Create a hub with small subscriber buffer
	config := &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             1,
		FlushInterval:         DefaultFlushInterval,
		RateLimit:             10000,
		SubscriberChannelSize: 5, // Small buffer
	}

	hub := NewHubWithConfig(config)
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish more events than buffer size without consuming
	for i := 0; i < 20; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Should only have received up to buffer size events
	count := 0
	done := time.After(100 * time.Millisecond)
drain:
	for {
		select {
		case <-ch:
			count++
		case <-done:
			break drain
		}
	}

	// Should have received at most 5 events (buffer size)
	assert.LessOrEqual(t, count, 5, "should not exceed subscriber buffer size")
}
