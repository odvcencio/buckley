package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryEventStore(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	t.Run("append and read events", func(t *testing.T) {
		streamID := "test-stream"
		events := []Event{
			{
				StreamID:  streamID,
				Type:      "test.event",
				Version:   1,
				Data:      map[string]interface{}{"key": "value1"},
				Timestamp: time.Now(),
			},
			{
				StreamID:  streamID,
				Type:      "test.event",
				Version:   2,
				Data:      map[string]interface{}{"key": "value2"},
				Timestamp: time.Now(),
			},
		}

		err := store.Append(ctx, streamID, events)
		require.NoError(t, err)

		read, err := store.Read(ctx, streamID, 0)
		require.NoError(t, err)
		assert.Len(t, read, 2)
		assert.Equal(t, "value1", read[0].Data["key"])
		assert.Equal(t, "value2", read[1].Data["key"])
	})

	t.Run("read from specific version", func(t *testing.T) {
		streamID := "test-stream-2"
		events := []Event{
			{StreamID: streamID, Type: "test", Version: 1, Data: map[string]interface{}{"n": 1}, Timestamp: time.Now()},
			{StreamID: streamID, Type: "test", Version: 2, Data: map[string]interface{}{"n": 2}, Timestamp: time.Now()},
			{StreamID: streamID, Type: "test", Version: 3, Data: map[string]interface{}{"n": 3}, Timestamp: time.Now()},
		}

		err := store.Append(ctx, streamID, events)
		require.NoError(t, err)

		read, err := store.Read(ctx, streamID, 2)
		require.NoError(t, err)
		assert.Len(t, read, 1)
		assert.Equal(t, 3, read[0].Data["n"])
	})
}
