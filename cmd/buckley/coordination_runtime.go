package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	coordination "github.com/odvcencio/buckley/pkg/coordination/coordinator"
	coordevents "github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

type coordinationRuntime struct {
	eventStore  coordevents.EventStore
	coordinator *coordination.Coordinator
	close       func()
}

func initCoordinationRuntime(cfg *config.Config) (*coordinationRuntime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("coordination config is required")
	}

	eventStore, closer, err := buildCoordinationEventStore(cfg)
	if err != nil {
		return nil, err
	}

	coord, err := coordination.NewCoordinator(coordination.DefaultConfig(), eventStore)
	if err != nil {
		if closer != nil {
			closer()
		}
		return nil, fmt.Errorf("init coordinator: %w", err)
	}

	return &coordinationRuntime{
		eventStore:  eventStore,
		coordinator: coord,
		close:       closer,
	}, nil
}

func (r *coordinationRuntime) Close() {
	if r == nil || r.close == nil {
		return
	}
	r.close()
}

func buildCoordinationEventStore(cfg *config.Config) (coordevents.EventStore, func(), error) {
	storeType := strings.ToLower(strings.TrimSpace(cfg.ACP.EventStore))
	if storeType == "" {
		storeType = "sqlite"
	}

	switch storeType {
	case "nats":
		opts := coordevents.NATSOptions{
			URL:            cfg.ACP.NATS.URL,
			Username:       cfg.ACP.NATS.Username,
			Password:       cfg.ACP.NATS.Password,
			Token:          cfg.ACP.NATS.Token,
			TLS:            cfg.ACP.NATS.TLS,
			StreamPrefix:   cfg.ACP.NATS.StreamPrefix,
			SnapshotBucket: cfg.ACP.NATS.SnapshotBucket,
			ConnectTimeout: cfg.ACP.NATS.ConnectTimeout,
			RequestTimeout: cfg.ACP.NATS.RequestTimeout,
		}
		store, err := coordevents.NewNATSEventStore(opts)
		if err != nil {
			return nil, nil, fmt.Errorf("init NATS event store: %w", err)
		}
		return store, store.Close, nil
	case "memory", "mem", "inmemory":
		store := coordevents.NewInMemoryStore()
		return store, nil, nil
	default:
		dbPath, err := resolveCoordinationEventsDBPath()
		if err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, nil, fmt.Errorf("init SQLite event store: ensure directory: %w", err)
		}
		store, err := coordevents.NewSQLiteEventStore(dbPath)
		if err != nil {
			return nil, nil, fmt.Errorf("init SQLite event store: %w", err)
		}
		return store, func() { _ = store.Close() }, nil
	}
}

func startTelemetryPersistence(ctx context.Context, hub *telemetry.Hub, store coordevents.EventStore) func() {
	if hub == nil || store == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	events, unsubscribe := hub.Subscribe()

	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				coordEvent := coordinationEventFromTelemetry(event)
				streamID := coordEvent.StreamID
				if strings.TrimSpace(streamID) == "" {
					streamID = "system"
				}
				if err := store.Append(ctx, streamID, []coordevents.Event{coordEvent}); err != nil {
					// Don't warn on context cancellation (expected during shutdown)
					if ctx.Err() == nil {
						fmt.Fprintf(os.Stderr, "warning: persist telemetry event: %v\n", err)
					}
				}
			}
		}
	}()

	return func() {
		cancel()
	}
}

func coordinationEventFromTelemetry(event telemetry.Event) coordevents.Event {
	metadata := map[string]string{}
	if event.SessionID != "" {
		metadata["session_id"] = event.SessionID
	}
	if event.PlanID != "" {
		metadata["plan_id"] = event.PlanID
	}
	if event.TaskID != "" {
		metadata["task_id"] = event.TaskID
	}
	if id := telemetryString(event.Data, "experiment_id"); id != "" {
		metadata["experiment_id"] = id
	}

	data := make(map[string]interface{}, len(event.Data))
	for k, v := range event.Data {
		data[k] = v
	}

	return coordevents.Event{
		StreamID:  coordinationStreamID(event),
		Type:      string(event.Type),
		Data:      data,
		Metadata:  metadata,
		Timestamp: event.Timestamp,
	}
}

func coordinationStreamID(event telemetry.Event) string {
	if id := strings.TrimSpace(event.SessionID); id != "" {
		return "session:" + id
	}
	if id := strings.TrimSpace(event.PlanID); id != "" {
		return "plan:" + id
	}
	if id := strings.TrimSpace(event.TaskID); id != "" {
		return "task:" + id
	}
	if id := telemetryString(event.Data, "experiment_id"); id != "" {
		return "experiment:" + id
	}
	return "system"
}

func telemetryString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	raw, ok := data[key]
	if !ok {
		return ""
	}
	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
