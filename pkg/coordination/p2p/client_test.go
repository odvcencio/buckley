package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/coordination/reliability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewP2PClient(t *testing.T) {
	breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{
		MaxFailures:      3,
		Timeout:          5 * time.Second,
		SuccessThreshold: 2,
	})

	client := NewP2PClient("localhost:50053", breaker)
	require.NotNil(t, client)
	assert.Equal(t, "localhost:50053", client.endpoint)
	assert.NotNil(t, client.breaker)
}

func TestP2PClientSendMessage(t *testing.T) {
	t.Run("successful send through circuit breaker", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{
			MaxFailures:      3,
			Timeout:          5 * time.Second,
			SuccessThreshold: 2,
		})

		client := NewP2PClient("localhost:50053", breaker)
		client.conn = &mockConnection{shouldFail: false}

		ctx := context.Background()
		err := client.SendMessage(ctx, []byte("test message"))
		require.NoError(t, err)

		// Verify breaker is still closed
		assert.Equal(t, reliability.CircuitClosed, breaker.State())
	})

	t.Run("circuit breaker opens after failures", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{
			MaxFailures:      3,
			Timeout:          5 * time.Second,
			SuccessThreshold: 2,
		})

		client := NewP2PClient("localhost:50053", breaker)
		client.conn = &mockConnection{shouldFail: true}

		ctx := context.Background()

		// Make requests until circuit opens
		var lastErr error
		for i := 0; i < 5; i++ {
			lastErr = client.SendMessage(ctx, []byte("test message"))
			if errors.Is(lastErr, reliability.ErrCircuitOpen) {
				break
			}
		}

		// Circuit should be open now
		assert.Equal(t, reliability.CircuitOpen, breaker.State())
		assert.Error(t, lastErr)
	})

	t.Run("circuit breaker protects against failures", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{
			MaxFailures:      2,
			Timeout:          5 * time.Second,
			SuccessThreshold: 2,
		})

		client := NewP2PClient("localhost:50053", breaker)
		client.conn = &mockConnection{shouldFail: true}

		ctx := context.Background()

		// First two failures should go through
		err1 := client.SendMessage(ctx, []byte("msg1"))
		assert.Error(t, err1)
		assert.NotEqual(t, reliability.ErrCircuitOpen, err1)

		err2 := client.SendMessage(ctx, []byte("msg2"))
		assert.Error(t, err2)
		assert.NotEqual(t, reliability.ErrCircuitOpen, err2)

		// Circuit should be open now
		assert.Equal(t, reliability.CircuitOpen, breaker.State())

		// Next request should be blocked by circuit breaker
		err3 := client.SendMessage(ctx, []byte("msg3"))
		assert.Error(t, err3)
		assert.True(t, errors.Is(err3, reliability.ErrCircuitOpen), "expected circuit open error, got: %v", err3)
	})

	t.Run("nil connection returns error", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{})
		client := NewP2PClient("localhost:50053", breaker)
		// Don't set client.conn

		ctx := context.Background()
		err := client.SendMessage(ctx, []byte("test"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not connected")
	})
}

func TestP2PClientConnect(t *testing.T) {
	t.Run("connection attempt", func(t *testing.T) {
		t.Skip("Requires actual gRPC server - tested in integration tests")
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{})
		client := NewP2PClient("localhost:50053", breaker)

		ctx := context.Background()
		// Note: This will fail in tests without actual server
		err := client.Connect(ctx)
		// We don't assert no error here because we don't have a real server
		// In real usage, this would connect to an actual gRPC server
		_ = err
	})
}

func TestP2PClientClose(t *testing.T) {
	t.Run("close with active connection", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{})
		client := NewP2PClient("localhost:50053", breaker)
		client.conn = &mockConnection{}

		err := client.Close()
		assert.NoError(t, err)
	})

	t.Run("close without connection", func(t *testing.T) {
		breaker := reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{})
		client := NewP2PClient("localhost:50053", breaker)

		err := client.Close()
		assert.NoError(t, err)
	})
}

// mockConnection is a mock implementation for testing
type mockConnection struct {
	shouldFail bool
	closed     bool
}

func (m *mockConnection) SendMessage(ctx context.Context, data []byte) error {
	if m.shouldFail {
		return errors.New("connection error")
	}
	return nil
}

func (m *mockConnection) Close() error {
	m.closed = true
	return nil
}
