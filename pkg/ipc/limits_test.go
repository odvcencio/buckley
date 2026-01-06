package ipc

import "testing"

func TestLimitsConstants(t *testing.T) {
	// Verify constants are defined and reasonable
	tests := []struct {
		name  string
		value int64
		min   int64
	}{
		{"maxConnectReadBytes", int64(maxConnectReadBytes), 1 << 20}, // At least 1MB
		{"maxConnectRequestBytes", int64(maxConnectRequestBytes), 1 << 20},
		{"maxEventStreamClients", int64(maxEventStreamClients), 1},
		{"maxPTYClients", int64(maxPTYClients), 1},
		{"maxGRPCSubscribersTotal", int64(maxGRPCSubscribersTotal), 1},
		{"maxGRPCSubscribersPerPrincipal", int64(maxGRPCSubscribersPerPrincipal), 1},
		{"maxWSReadBytesEventStream", int64(maxWSReadBytesEventStream), 1 << 10},
		{"maxWSReadBytesPTY", int64(maxWSReadBytesPTY), 1 << 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value < tt.min {
				t.Errorf("%s = %d, expected at least %d", tt.name, tt.value, tt.min)
			}
		})
	}
}

func TestHTTPDecodeConstants(t *testing.T) {
	// Verify HTTP decode constants are reasonable
	if maxBodyBytesTiny <= 0 {
		t.Errorf("maxBodyBytesTiny should be positive, got %d", maxBodyBytesTiny)
	}
	if maxBodyBytesSmall <= maxBodyBytesTiny {
		t.Errorf("maxBodyBytesSmall (%d) should be greater than maxBodyBytesTiny (%d)",
			maxBodyBytesSmall, maxBodyBytesTiny)
	}
	if maxBodyBytesCommand <= maxBodyBytesSmall {
		t.Errorf("maxBodyBytesCommand (%d) should be greater than maxBodyBytesSmall (%d)",
			maxBodyBytesCommand, maxBodyBytesSmall)
	}
}
