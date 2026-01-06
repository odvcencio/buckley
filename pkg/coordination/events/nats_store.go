package events

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
)

// Compile-time check that NATSEventStore implements EventStore.
var _ EventStore = (*NATSEventStore)(nil)

// NATSEventStore implements EventStore using NATS JetStream.
// It supports real JetStream clients as well as the in-memory mock client used in tests.
type NATSEventStore struct {
	client         interface{} // nats.JetStreamContext for production, mock client in tests
	conn           *nats.Conn
	kv             nats.KeyValue
	streamPrefix   string
	snapshotBucket string
	requestTimeout time.Duration

	mu        sync.RWMutex
	streams   map[string][]storedEventMessage // mock-only cache
	snapshots map[string]snapshotMessage      // mock-only cache
	subs      map[string][]mockHandler        // mock-only handlers
}

// NATSOptions configures the JetStream-backed event store.
type NATSOptions struct {
	URL            string
	Username       string
	Password       string
	Token          string
	TLS            bool
	StreamPrefix   string
	SnapshotBucket string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

// storedEventMessage represents an event as stored in NATS.
type storedEventMessage struct {
	StreamID  string                 `json:"stream_id"`
	Type      string                 `json:"type"`
	Version   int64                  `json:"version"`
	Data      map[string]interface{} `json:"data"`
	Metadata  map[string]string      `json:"metadata"`
	Timestamp time.Time              `json:"timestamp"`
}

// snapshotMessage represents a snapshot as stored in NATS KV.
type snapshotMessage struct {
	StreamID string      `json:"stream_id"`
	Version  int64       `json:"version"`
	State    interface{} `json:"state"`
}

// natsSubscription wraps a NATS subscription.
type natsSubscription struct {
	sub interface{ Unsubscribe() error }
}

func (s *natsSubscription) Unsubscribe() error {
	if s.sub == nil {
		return nil
	}
	return s.sub.Unsubscribe()
}

// NewNATSEventStore creates a JetStream-backed event store.
func NewNATSEventStore(opts NATSOptions) (*NATSEventStore, error) {
	if strings.TrimSpace(opts.URL) == "" {
		opts.URL = nats.DefaultURL
	}
	if opts.StreamPrefix == "" {
		opts.StreamPrefix = "acp"
	}
	if opts.SnapshotBucket == "" {
		opts.SnapshotBucket = "acp_snapshots"
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}

	natsOpts := []nats.Option{
		nats.Timeout(opts.ConnectTimeout),
		nats.Name("buckley-acp"),
	}
	if opts.Username != "" || opts.Password != "" {
		natsOpts = append(natsOpts, nats.UserInfo(opts.Username, opts.Password))
	}
	if opts.Token != "" {
		natsOpts = append(natsOpts, nats.Token(opts.Token))
	}
	if opts.TLS {
		natsOpts = append(natsOpts, nats.Secure(&tlsConfig))
	}

	conn, err := nats.Connect(opts.URL, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}

	js, err := conn.JetStream()
	if err != nil {
		_ = conn.Drain()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	store := &NATSEventStore{
		client:         js,
		conn:           conn,
		streamPrefix:   opts.StreamPrefix,
		snapshotBucket: opts.SnapshotBucket,
		requestTimeout: opts.RequestTimeout,
		streams:        make(map[string][]storedEventMessage),
		snapshots:      make(map[string]snapshotMessage),
	}

	// Ensure snapshot bucket exists (lazy create otherwise).
	if _, err := js.KeyValue(store.snapshotBucket); err != nil {
		if _, createErr := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: store.snapshotBucket}); createErr != nil {
			return nil, fmt.Errorf("ensure snapshot bucket: %w", createErr)
		}
	}

	return store, nil
}

// NewNATSEventStoreWithClient lets tests provide a mock client.
func NewNATSEventStoreWithClient(client interface{}, opts NATSOptions) *NATSEventStore {
	if opts.StreamPrefix == "" {
		opts.StreamPrefix = "acp"
	}
	if opts.SnapshotBucket == "" {
		opts.SnapshotBucket = "acp_snapshots"
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}
	return &NATSEventStore{
		client:         client,
		streamPrefix:   opts.StreamPrefix,
		snapshotBucket: opts.SnapshotBucket,
		requestTimeout: opts.RequestTimeout,
		streams:        make(map[string][]storedEventMessage),
		snapshots:      make(map[string]snapshotMessage),
		subs:           make(map[string][]mockHandler),
	}
}

// Close closes the underlying NATS connection when present.
func (s *NATSEventStore) Close() {
	if s.conn != nil {
		_ = s.conn.Drain()
	}
}

// Append adds events to a stream.
func (s *NATSEventStore) Append(ctx context.Context, streamID string, evts []Event) error {
	if len(evts) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if js, ok := s.client.(nats.JetStreamContext); ok {
		return s.appendJetStream(ctx, js, streamID, evts)
	}
	return s.appendMock(ctx, streamID, evts)
}

// Read retrieves events from a stream starting at a version.
func (s *NATSEventStore) Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	if js, ok := s.client.(nats.JetStreamContext); ok {
		return s.readJetStream(ctx, js, streamID, fromVersion)
	}
	return s.readMock(ctx, streamID, fromVersion)
}

// Subscribe to events in a stream with real-time updates via NATS.
func (s *NATSEventStore) Subscribe(ctx context.Context, streamID string, handler EventHandler) (Subscription, error) {
	if js, ok := s.client.(nats.JetStreamContext); ok {
		return s.subscribeJetStream(ctx, js, streamID, handler)
	}
	return s.subscribeMock(ctx, streamID, handler)
}

// Snapshot saves a state snapshot using NATS Key-Value store.
func (s *NATSEventStore) Snapshot(ctx context.Context, streamID string, version int64, state interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	msg := snapshotMessage{
		StreamID: streamID,
		Version:  version,
		State:    state,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	if js, ok := s.client.(nats.JetStreamContext); ok {
		kv, err := js.KeyValue(s.snapshotBucket)
		if err != nil {
			if kv, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: s.snapshotBucket}); err != nil {
				return fmt.Errorf("create snapshot bucket: %w", err)
			}
		}
		_, err = kv.Put(streamID, data)
		return err
	}

	return s.snapshotMock(streamID, msg)
}

// LoadSnapshot retrieves the latest snapshot from NATS Key-Value store.
func (s *NATSEventStore) LoadSnapshot(ctx context.Context, streamID string) (state interface{}, version int64, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	if js, ok := s.client.(nats.JetStreamContext); ok {
		kv, err := js.KeyValue(s.snapshotBucket)
		if err != nil {
			return nil, 0, fmt.Errorf("snapshot bucket not found: %w", err)
		}
		entry, err := kv.Get(streamID)
		if err != nil {
			return nil, 0, fmt.Errorf("snapshot not found: %w", err)
		}

		var msg snapshotMessage
		if err := json.Unmarshal(entry.Value(), &msg); err != nil {
			return nil, 0, fmt.Errorf("unmarshal snapshot: %w", err)
		}

		return msg.State, msg.Version, nil
	}

	return s.loadSnapshotMock(streamID)
}

// appendJetStream handles append for real JetStream.
func (s *NATSEventStore) appendJetStream(ctx context.Context, js nats.JetStreamContext, streamID string, evts []Event) error {
	streamName := s.streamName(streamID)
	subject := s.streamSubject(streamID)

	if err := s.ensureStreamJS(ctx, js, streamName, subject); err != nil {
		return err
	}

	info, err := js.StreamInfo(streamName)
	if err != nil {
		return fmt.Errorf("stream info: %w", err)
	}
	lastSeq := info.State.LastSeq

	if lastSeq > math.MaxInt64 {
		return fmt.Errorf("jetstream last sequence %d exceeds int64", lastSeq)
	}

	for i := range evts {
		if i < 0 {
			return fmt.Errorf("invalid event index %d", i)
		}
		idx := uint64(i)
		seq := lastSeq + idx + 1
		if seq > math.MaxInt64 {
			return fmt.Errorf("jetstream sequence %d exceeds int64", seq)
		}
		version := int64(seq)
		evts[i].StreamID = streamID
		evts[i].Version = version
		if evts[i].Timestamp.IsZero() {
			evts[i].Timestamp = time.Now().UTC()
		}

		msg := storedEventMessage{
			StreamID:  streamID,
			Type:      evts[i].Type,
			Version:   version,
			Data:      evts[i].Data,
			Metadata:  evts[i].Metadata,
			Timestamp: evts[i].Timestamp,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}

		pubCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
		ack, err := js.Publish(subject, data, nats.Context(pubCtx), nats.ExpectLastSequence(lastSeq+idx))
		cancel()
		if err != nil {
			return fmt.Errorf("publish event: %w", err)
		}
		if ack.Sequence != seq {
			// Best effort sanity check; keep going but log mismatch.
			return fmt.Errorf("sequence mismatch: expected %d, got %d", version, ack.Sequence)
		}
	}

	return nil
}

// appendMock retains existing mock behavior for tests.
func (s *NATSEventStore) appendMock(ctx context.Context, streamID string, evts []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure stream exists
	if s.streams == nil {
		s.streams = make(map[string][]storedEventMessage)
	}
	if s.subs == nil {
		s.subs = make(map[string][]mockHandler)
	}
	currentLen := len(s.streams[streamID])

	if mc, ok := s.client.(interface {
		CreateStream(name string) error
		StreamExists(name string) bool
	}); ok {
		if !mc.StreamExists(streamID) {
			if err := mc.CreateStream(streamID); err != nil {
				return fmt.Errorf("mock create stream: %w", err)
			}
		}
	}

	for i := range evts {
		evts[i].StreamID = streamID
		evts[i].Version = int64(currentLen + i + 1)
		if evts[i].Timestamp.IsZero() {
			evts[i].Timestamp = time.Now().UTC()
		}
		msg := storedEventMessage{
			StreamID:  evts[i].StreamID,
			Type:      evts[i].Type,
			Version:   evts[i].Version,
			Data:      evts[i].Data,
			Metadata:  evts[i].Metadata,
			Timestamp: evts[i].Timestamp,
		}
		s.streams[streamID] = append(s.streams[streamID], msg)

		if mc, ok := s.client.(interface {
			Publish(stream string, data []byte) error
		}); ok {
			data, err := json.Marshal(msg)
			if err != nil {
				return fmt.Errorf("mock marshal event: %w", err)
			}
			if err := mc.Publish(streamID, data); err != nil {
				return fmt.Errorf("mock publish: %w", err)
			}
		}

		// Local mock subscribers
		for _, handler := range s.subs[streamID] {
			go handler(msg)
		}
	}
	return nil
}

func (s *NATSEventStore) readJetStream(ctx context.Context, js nats.JetStreamContext, streamID string, fromVersion int64) ([]Event, error) {
	streamName := s.streamName(streamID)
	subject := s.streamSubject(streamID)

	if err := s.ensureStreamJS(ctx, js, streamName, subject); err != nil {
		return nil, err
	}

	info, err := js.StreamInfo(streamName)
	if err != nil {
		return nil, fmt.Errorf("stream info: %w", err)
	}
	last := info.State.LastSeq
	start := uint64(1)
	if fromVersion > 1 {
		start = uint64(fromVersion)
	}

	var events []Event
	for seq := start; seq <= last; seq++ {
		msg, err := js.GetMsg(streamName, seq, nats.Context(ctx))
		if err != nil {
			return nil, fmt.Errorf("get msg %d: %w", seq, err)
		}
		var stored storedEventMessage
		if err := json.Unmarshal(msg.Data, &stored); err != nil {
			return nil, fmt.Errorf("unmarshal msg %d: %w", seq, err)
		}

		if seq > math.MaxInt64 {
			return nil, fmt.Errorf("jetstream sequence %d exceeds int64", seq)
		}
		version := int64(seq)
		if stored.Version > 0 {
			version = stored.Version
		}

		events = append(events, Event{
			StreamID:  stored.StreamID,
			Type:      stored.Type,
			Version:   version,
			Data:      stored.Data,
			Metadata:  stored.Metadata,
			Timestamp: stored.Timestamp,
		})
	}

	return events, nil
}

func (s *NATSEventStore) readMock(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	messages, exists := s.streams[streamID]
	if !exists {
		return []Event{}, nil
	}

	var evts []Event
	for _, msg := range messages {
		if msg.Version >= fromVersion {
			evts = append(evts, Event{
				StreamID:  msg.StreamID,
				Type:      msg.Type,
				Version:   msg.Version,
				Data:      msg.Data,
				Metadata:  msg.Metadata,
				Timestamp: msg.Timestamp,
			})
		}
	}

	sort.Slice(evts, func(i, j int) bool {
		return evts[i].Version < evts[j].Version
	})

	return evts, nil
}

func (s *NATSEventStore) subscribeJetStream(ctx context.Context, js nats.JetStreamContext, streamID string, handler EventHandler) (Subscription, error) {
	streamName := s.streamName(streamID)
	subject := s.streamSubject(streamID)
	if err := s.ensureStreamJS(ctx, js, streamName, subject); err != nil {
		return nil, err
	}

	sub, err := js.Subscribe(subject, func(msg *nats.Msg) {
		if msg == nil {
			return
		}

		md, _ := msg.Metadata()
		version := int64(0)
		if md != nil {
			if md.Sequence.Stream > math.MaxInt64 {
				return
			}
			// #nosec G115 -- guarded by max int64 check above.
			version = int64(md.Sequence.Stream)
		}

		var stored storedEventMessage
		if err := json.Unmarshal(msg.Data, &stored); err != nil {
			return
		}
		if version == 0 && stored.Version > 0 {
			version = stored.Version
		}
		event := Event{
			StreamID:  stored.StreamID,
			Type:      stored.Type,
			Version:   version,
			Data:      stored.Data,
			Metadata:  stored.Metadata,
			Timestamp: stored.Timestamp,
		}
		_ = handler(ctx, event)
		_ = msg.Ack()
	}, nats.DeliverNew(), nats.ManualAck())
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	return &natsSubscription{sub: sub}, nil
}

func (s *NATSEventStore) subscribeMock(ctx context.Context, streamID string, handler EventHandler) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// If client exposes subscribe, hook directly.
	if mc, ok := s.client.(interface {
		Subscribe(stream string, handler func([]byte) error) (interface{ Unsubscribe() error }, error)
	}); ok {
		wrapped := func(data []byte) error {
			var stored storedEventMessage
			if err := json.Unmarshal(data, &stored); err != nil {
				return err
			}
			event := Event{
				StreamID:  stored.StreamID,
				Type:      stored.Type,
				Version:   stored.Version,
				Data:      stored.Data,
				Metadata:  stored.Metadata,
				Timestamp: stored.Timestamp,
			}
			return handler(ctx, event)
		}
		sub, err := mc.Subscribe(streamID, wrapped)
		if err != nil {
			return nil, fmt.Errorf("mock subscribe: %w", err)
		}
		return &natsSubscription{sub: sub}, nil
	}

	// Fallback: store handler locally
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = make(map[string][]mockHandler)
	}
	s.subs[streamID] = append(s.subs[streamID], func(msg storedEventMessage) {
		_ = handler(ctx, Event{
			StreamID:  msg.StreamID,
			Type:      msg.Type,
			Version:   msg.Version,
			Data:      msg.Data,
			Metadata:  msg.Metadata,
			Timestamp: msg.Timestamp,
		})
	})
	return &mockLocalSubscription{store: s, streamID: streamID}, nil
}

func (s *NATSEventStore) snapshotMock(streamID string, msg snapshotMessage) error {
	if mc, ok := s.client.(interface {
		Put(bucket, key string, value []byte) error
	}); ok {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("mock marshal snapshot: %w", err)
		}
		return mc.Put(s.snapshotBucket, streamID, data)
	}

	if s.snapshots == nil {
		s.snapshots = make(map[string]snapshotMessage)
	}
	s.snapshots[streamID] = msg
	return nil
}

func (s *NATSEventStore) loadSnapshotMock(streamID string) (interface{}, int64, error) {
	if mc, ok := s.client.(interface {
		Get(bucket, key string) ([]byte, error)
	}); ok {
		data, err := mc.Get(s.snapshotBucket, streamID)
		if err != nil {
			return nil, 0, fmt.Errorf("snapshot not found: %w", err)
		}
		var msg snapshotMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, 0, err
		}
		return msg.State, msg.Version, nil
	}

	msg, exists := s.snapshots[streamID]
	if !exists {
		return nil, 0, fmt.Errorf("snapshot not found: %s", streamID)
	}
	return msg.State, msg.Version, nil
}

func (s *NATSEventStore) ensureStreamJS(ctx context.Context, js nats.JetStreamContext, streamName, subject string) error {
	_, err := js.StreamInfo(streamName)
	if err == nil {
		return nil
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
		Storage:  nats.FileStorage,
		Replicas: 1,
	})
	if err != nil {
		return fmt.Errorf("add stream: %w", err)
	}
	return nil
}

func (s *NATSEventStore) streamName(streamID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, streamID)
	return fmt.Sprintf("%s-%s", s.streamPrefix, safe)
}

func (s *NATSEventStore) streamSubject(streamID string) string {
	return fmt.Sprintf("%s.%s", s.streamPrefix, streamID)
}

type mockHandler func(storedEventMessage)

type mockLocalSubscription struct {
	store    *NATSEventStore
	streamID string
}

func (m *mockLocalSubscription) Unsubscribe() error {
	if m == nil || m.store == nil {
		return nil
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	handlers := m.store.subs[m.streamID]
	if len(handlers) == 0 {
		return nil
	}
	m.store.subs[m.streamID] = handlers[:0]
	return nil
}

// tlsConfig is a minimal TLS config for secure NATS connections.
var tlsConfig = tls.Config{
	MinVersion: tls.VersionTLS12,
}
