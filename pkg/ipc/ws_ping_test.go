package ipc

import (
	"context"
	"testing"
	"time"
)

func TestStartWSPingNilConn(t *testing.T) {
	// Should not panic with nil connection
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This should return immediately without starting a goroutine
	startWSPing(ctx, nil)

	// Give it a moment - if it tried to use the nil conn, we'd panic
	time.Sleep(10 * time.Millisecond)
}

func TestWSPingConstants(t *testing.T) {
	// Verify constants are reasonable
	if wsPingInterval < 10*time.Second {
		t.Errorf("wsPingInterval too short: %v", wsPingInterval)
	}
	if wsPingTimeout < 1*time.Second {
		t.Errorf("wsPingTimeout too short: %v", wsPingTimeout)
	}
	if wsPingTimeout >= wsPingInterval {
		t.Errorf("wsPingTimeout (%v) should be less than wsPingInterval (%v)", wsPingTimeout, wsPingInterval)
	}
}
