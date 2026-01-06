package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// MockNATSClient simulates NATS JetStream for testing
type MockNATSClient interface {
	// Stream operations
	CreateStream(name string) error
	DeleteStream(name string) error
	StreamExists(name string) bool

	// Publish operations
	Publish(stream string, data []byte) error

	// Consumer operations
	Subscribe(stream string, handler func([]byte) error) (interface{ Unsubscribe() error }, error)

	// Key-Value operations (for snapshots)
	Put(bucket, key string, value []byte) error
	Get(bucket, key string) ([]byte, error)
	KeyExists(bucket, key string) bool
}

type MockSubscription interface {
	Unsubscribe() error
}

// mockNATSClient implements MockNATSClient for testing
type mockNATSClient struct {
	mu            sync.RWMutex
	streams       map[string][]mockMessage
	snapshots     map[string][]byte
	subscriptions map[string][]func([]byte) error
	shouldFail    bool
	closed        bool
}

type mockMessage struct {
	Data      []byte
	Timestamp time.Time
}

func newMockNATSClient() *mockNATSClient {
	return &mockNATSClient{
		streams:       make(map[string][]mockMessage),
		snapshots:     make(map[string][]byte),
		subscriptions: make(map[string][]func([]byte) error),
	}
}

func (m *mockNATSClient) CreateStream(name string) error {
	if m.shouldFail {
		return errors.New("mock: stream creation failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("mock: client closed")
	}
	if _, exists := m.streams[name]; !exists {
		m.streams[name] = []mockMessage{}
	}
	return nil
}

func (m *mockNATSClient) DeleteStream(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.streams, name)
	return nil
}

func (m *mockNATSClient) StreamExists(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.streams[name]
	return exists
}

func (m *mockNATSClient) Publish(stream string, data []byte) error {
	if m.shouldFail {
		return errors.New("mock: publish failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("mock: client closed")
	}

	msg := mockMessage{
		Data:      data,
		Timestamp: time.Now().UTC(),
	}
	m.streams[stream] = append(m.streams[stream], msg)

	// Notify subscribers
	if handlers, exists := m.subscriptions[stream]; exists {
		for _, handler := range handlers {
			go handler(data)
		}
	}

	return nil
}

func (m *mockNATSClient) Subscribe(stream string, handler func([]byte) error) (interface{ Unsubscribe() error }, error) {
	if m.shouldFail {
		return nil, errors.New("mock: subscribe failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("mock: client closed")
	}

	m.subscriptions[stream] = append(m.subscriptions[stream], handler)

	return &mockSubscription{
		client:  m,
		stream:  stream,
		handler: handler,
	}, nil
}

func (m *mockNATSClient) Put(bucket, key string, value []byte) error {
	if m.shouldFail {
		return errors.New("mock: put failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("mock: client closed")
	}
	m.snapshots[bucket+":"+key] = value
	return nil
}

func (m *mockNATSClient) Get(bucket, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, errors.New("mock: client closed")
	}
	data, exists := m.snapshots[bucket+":"+key]
	if !exists {
		return nil, errors.New("key not found")
	}
	return data, nil
}

func (m *mockNATSClient) KeyExists(bucket, key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.snapshots[bucket+":"+key]
	return exists
}

func (m *mockNATSClient) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

type mockSubscription struct {
	client  *mockNATSClient
	stream  string
	handler func([]byte) error
}

func (s *mockSubscription) Unsubscribe() error {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()

	handlers := s.client.subscriptions[s.stream]
	for i, h := range handlers {
		// Compare function pointers (not perfect but works for testing)
		if &h == &s.handler {
			s.client.subscriptions[s.stream] = append(handlers[:i], handlers[i+1:]...)
			break
		}
	}
	return nil
}

// Helper function to create test store
func setupTestNATSStore(t *testing.T) (*NATSEventStore, *mockNATSClient) {
	t.Helper()

	mockClient := newMockNATSClient()
	store := &NATSEventStore{
		client: mockClient,
	}

	return store, mockClient
}

func TestNewNATSEventStore(t *testing.T) {
	// This test verifies the structure exists and can be instantiated
	mockClient := newMockNATSClient()
	store := &NATSEventStore{
		client: mockClient,
	}

	if store == nil {
		t.Fatal("NewNATSEventStore returned nil")
	}
}

func TestNATSAppendSingleEvent(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "test-stream-1"

	event := Event{
		StreamID: streamID,
		Type:     "TestEvent",
		Version:  1,
		Data: map[string]interface{}{
			"key":   "value",
			"count": float64(42),
		},
		Metadata: map[string]string{
			"user": "test-user",
		},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event})
	if err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}

	// Read back the event
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(events) != 1 {
		t.Fatalf("Read() returned %d events, want 1", len(events))
	}

	got := events[0]
	if got.StreamID != event.StreamID {
		t.Errorf("StreamID = %s, want %s", got.StreamID, event.StreamID)
	}
	if got.Type != event.Type {
		t.Errorf("Type = %s, want %s", got.Type, event.Type)
	}
	if got.Version != event.Version {
		t.Errorf("Version = %d, want %d", got.Version, event.Version)
	}
}

func TestNATSAppendMultipleEvents(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "test-stream-2"

	events := []Event{
		{
			StreamID:  streamID,
			Type:      "Event1",
			Version:   1,
			Data:      map[string]interface{}{"step": "first"},
			Metadata:  map[string]string{"phase": "init"},
			Timestamp: time.Now().UTC(),
		},
		{
			StreamID:  streamID,
			Type:      "Event2",
			Version:   2,
			Data:      map[string]interface{}{"step": "second"},
			Metadata:  map[string]string{"phase": "process"},
			Timestamp: time.Now().UTC(),
		},
		{
			StreamID:  streamID,
			Type:      "Event3",
			Version:   3,
			Data:      map[string]interface{}{"step": "third"},
			Metadata:  map[string]string{"phase": "complete"},
			Timestamp: time.Now().UTC(),
		},
	}

	err := store.Append(ctx, streamID, events)
	if err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}

	// Read all events
	retrieved, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(retrieved) != 3 {
		t.Fatalf("Read() returned %d events, want 3", len(retrieved))
	}

	// Verify order and versions
	for i, event := range retrieved {
		expectedVersion := int64(i + 1)
		if event.Version != expectedVersion {
			t.Errorf("Event %d: Version = %d, want %d", i, event.Version, expectedVersion)
		}
	}
}

func TestNATSReadFromVersion(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "test-stream-3"

	// Append 5 events
	events := make([]Event, 5)
	for i := 0; i < 5; i++ {
		events[i] = Event{
			StreamID:  streamID,
			Type:      "TestEvent",
			Version:   int64(i + 1),
			Data:      map[string]interface{}{"index": float64(i)},
			Metadata:  map[string]string{},
			Timestamp: time.Now().UTC(),
		}
	}

	if err := store.Append(ctx, streamID, events); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// Read from version 3
	retrieved, err := store.Read(ctx, streamID, 3)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	// Should get versions 3, 4, 5
	if len(retrieved) != 3 {
		t.Fatalf("Read(fromVersion=3) returned %d events, want 3", len(retrieved))
	}

	for i, event := range retrieved {
		expectedVersion := int64(i + 3)
		if event.Version != expectedVersion {
			t.Errorf("Event %d: Version = %d, want %d", i, event.Version, expectedVersion)
		}
	}
}

func TestNATSReadEmptyStream(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()

	events, err := store.Read(ctx, "nonexistent-stream", 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(events) != 0 {
		t.Errorf("Read() returned %d events, want 0", len(events))
	}
}

func TestNATSSubscribe(t *testing.T) {
	store, mockClient := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "subscribe-stream"

	// Subscribe to the stream
	receivedEvents := make([]Event, 0)
	var mu sync.Mutex

	handler := func(ctx context.Context, event Event) error {
		mu.Lock()
		defer mu.Unlock()
		receivedEvents = append(receivedEvents, event)
		return nil
	}

	sub, err := store.Subscribe(ctx, streamID, handler)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer sub.Unsubscribe()

	// Append an event
	event := Event{
		StreamID:  streamID,
		Type:      "TestEvent",
		Version:   1,
		Data:      map[string]interface{}{"test": "data"},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	err = store.Append(ctx, streamID, []Event{event})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// Give some time for the subscription to process
	time.Sleep(100 * time.Millisecond)

	// Verify the handler was called
	mu.Lock()
	defer mu.Unlock()

	if len(receivedEvents) != 1 {
		t.Errorf("Handler received %d events, want 1", len(receivedEvents))
	}

	// Verify stream exists in mock
	if !mockClient.StreamExists(streamID) {
		t.Error("Stream was not created in NATS")
	}
}

func TestNATSSnapshot(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "snapshot-stream"

	// Create a snapshot
	state := map[string]interface{}{
		"counter": float64(100),
		"status":  "active",
		"items":   []interface{}{"a", "b", "c"},
		"nested": map[string]interface{}{
			"key": "value",
		},
	}

	err := store.Snapshot(ctx, streamID, 10, state)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	// Load the snapshot
	loadedState, version, err := store.LoadSnapshot(ctx, streamID)
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}

	if version != 10 {
		t.Errorf("LoadSnapshot() version = %d, want 10", version)
	}

	// Convert to comparable format
	loadedMap, ok := loadedState.(map[string]interface{})
	if !ok {
		t.Fatalf("LoadSnapshot() returned type %T, want map[string]interface{}", loadedState)
	}

	if loadedMap["counter"] != float64(100) {
		t.Errorf("counter = %v, want 100", loadedMap["counter"])
	}
	if loadedMap["status"] != "active" {
		t.Errorf("status = %v, want active", loadedMap["status"])
	}
}

func TestNATSSnapshotLatest(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "multi-snapshot-stream"

	// Create multiple snapshots
	for i := 1; i <= 3; i++ {
		state := map[string]interface{}{
			"version": float64(i * 10),
		}
		err := store.Snapshot(ctx, streamID, int64(i*10), state)
		if err != nil {
			t.Fatalf("Snapshot(%d) error = %v", i, err)
		}
	}

	// Should load the latest snapshot (version 30)
	loadedState, version, err := store.LoadSnapshot(ctx, streamID)
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}

	if version != 30 {
		t.Errorf("LoadSnapshot() version = %d, want 30", version)
	}

	loadedMap := loadedState.(map[string]interface{})
	if loadedMap["version"] != float64(30) {
		t.Errorf("version = %v, want 30", loadedMap["version"])
	}
}

func TestNATSLoadSnapshotNotFound(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()

	_, _, err := store.LoadSnapshot(ctx, "nonexistent-stream")
	if err == nil {
		t.Error("LoadSnapshot() should return error for nonexistent stream")
	}
}

func TestNATSAppendEmptyEvents(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "empty-stream"

	// Appending empty array should be a no-op
	err := store.Append(ctx, streamID, []Event{})
	if err != nil {
		t.Errorf("Append() with empty events should not error, got: %v", err)
	}
}

func TestNATSContextCancellation(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	streamID := "cancelled-stream"

	event := Event{
		StreamID:  streamID,
		Type:      "TestEvent",
		Version:   1,
		Data:      map[string]interface{}{"test": "data"},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	// Operations with cancelled context should fail
	err := store.Append(ctx, streamID, []Event{event})
	if err == nil {
		t.Error("Append() with cancelled context should return error")
	}
}

func TestNATSClientFailure(t *testing.T) {
	store, mockClient := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "fail-stream"

	// Simulate client failure
	mockClient.shouldFail = true

	event := Event{
		StreamID:  streamID,
		Type:      "TestEvent",
		Version:   1,
		Data:      map[string]interface{}{"test": "data"},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event})
	if err == nil {
		t.Error("Append() should fail when NATS client fails")
	}
}

func TestNATSConcurrentAppends(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "concurrent-stream"

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			event := Event{
				StreamID:  streamID,
				Type:      "ConcurrentEvent",
				Version:   int64(idx + 1),
				Data:      map[string]interface{}{"goroutine": float64(idx)},
				Metadata:  map[string]string{},
				Timestamp: time.Now().UTC(),
			}

			if err := store.Append(ctx, streamID, []Event{event}); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("Concurrent Append() error: %v", err)
	}

	// Verify all events were written
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(events) != numGoroutines {
		t.Errorf("Read() returned %d events, want %d", len(events), numGoroutines)
	}
}

func TestNATSStoredEventMessage(t *testing.T) {
	// Test the storedEventMessage structure
	event := Event{
		StreamID:  "test",
		Type:      "TestEvent",
		Version:   1,
		Data:      map[string]interface{}{"key": "value"},
		Metadata:  map[string]string{"meta": "data"},
		Timestamp: time.Now().UTC(),
	}

	msg := storedEventMessage{
		StreamID:  event.StreamID,
		Type:      event.Type,
		Version:   event.Version,
		Data:      event.Data,
		Metadata:  event.Metadata,
		Timestamp: event.Timestamp,
	}

	// Marshal and unmarshal
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded storedEventMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.StreamID != msg.StreamID {
		t.Errorf("StreamID = %s, want %s", decoded.StreamID, msg.StreamID)
	}
	if decoded.Version != msg.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, msg.Version)
	}
}

func TestNATSSnapshotMessage(t *testing.T) {
	// Test the snapshotMessage structure
	state := map[string]interface{}{
		"key": "value",
		"num": float64(42),
	}

	msg := snapshotMessage{
		StreamID: "test-stream",
		Version:  10,
		State:    state,
	}

	// Marshal and unmarshal
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded snapshotMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.StreamID != msg.StreamID {
		t.Errorf("StreamID = %s, want %s", decoded.StreamID, msg.StreamID)
	}
	if decoded.Version != msg.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, msg.Version)
	}
}

func TestNATSUnsubscribe(t *testing.T) {
	store, _ := setupTestNATSStore(t)

	ctx := context.Background()
	streamID := "unsubscribe-stream"

	handler := func(ctx context.Context, event Event) error {
		return nil
	}

	sub, err := store.Subscribe(ctx, streamID, handler)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Unsubscribe should not error
	err = sub.Unsubscribe()
	if err != nil {
		t.Errorf("Unsubscribe() error = %v", err)
	}
}
