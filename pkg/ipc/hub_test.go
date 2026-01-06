package ipc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

type fakeConn struct {
	writeCount *atomic.Int32
	closeCount *atomic.Int32
}

func (f *fakeConn) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	f.writeCount.Add(1)
	return ctx.Err()
}

func (f *fakeConn) Close(_ websocket.StatusCode, _ string) error {
	f.closeCount.Add(1)
	return nil
}

func (f *fakeConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return websocket.MessageText, nil, ctx.Err()
}

func TestHubBroadcastFiltersAndDropsSlowClients(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fast client accepting all
	fast := &fakeConn{writeCount: &atomic.Int32{}, closeCount: &atomic.Int32{}}
	c1 := hub.register(fast, nil)

	// Filtered client only mission events
	filtered := &fakeConn{writeCount: &atomic.Int32{}, closeCount: &atomic.Int32{}}
	c2 := hub.register(filtered, func(ev Event) bool { return ev.Type == "mission.test" })

	// Slow client with tiny buffer should be dropped
	slow := &client{
		conn:   &fakeConn{writeCount: &atomic.Int32{}, closeCount: &atomic.Int32{}},
		send:   make(chan Event, 1),
		filter: nil,
	}
	hub.mu.Lock()
	hub.clients[slow] = struct{}{}
	hub.mu.Unlock()

	go func() {
		_ = c1.writeLoop(ctx)
	}()
	go func() {
		_ = c2.writeLoop(ctx)
	}()

	hub.Broadcast(Event{Type: "mission.test", Timestamp: time.Now()})
	hub.Broadcast(Event{Type: "other", Timestamp: time.Now()})

	time.Sleep(50 * time.Millisecond)

	if got := fast.writeCount.Load(); got == 0 {
		t.Fatalf("expected fast client to receive events")
	}
	if got := filtered.writeCount.Load(); got == 0 {
		t.Fatalf("expected filtered client to receive mission events")
	}
	// Slow client buffer should have overflowed and removed client
	hub.mu.RLock()
	_, stillPresent := hub.clients[slow]
	hub.mu.RUnlock()
	if stillPresent {
		t.Fatalf("expected slow client to be removed")
	}
}
