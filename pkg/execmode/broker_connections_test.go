package execmode

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBroker_RejectsOversizedHeaders(t *testing.T) {
	sink := &recordingSink{}
	broker, err := NewBroker(newWorkspace(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(filepath.Join(t.TempDir(), "caps.sock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", broker.SocketPath())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://caps/v1/files/read", strings.NewReader(`{"path":"greeting.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+broker.Token())
	req.Header.Set("X-Padding", strings.Repeat("x", 64<<10))
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if records := sink.byMethod(CapFilesRead); len(records) != 0 {
		t.Fatalf("oversized header reached capability: %+v", records)
	}
}

func TestBroker_IdleConnectionsReleaseCapacity(t *testing.T) {
	limit := DefaultCapabilityCallLimit + 2
	broker, err := NewBroker(newWorkspace(t), &recordingSink{}, WithCapabilityCallLimit(limit))
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(filepath.Join(t.TempDir(), "caps.sock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	for i := 0; i < limit; i++ {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", broker.SocketPath())
		}}
		t.Cleanup(transport.CloseIdleConnections)
		client := &http.Client{Transport: transport, Timeout: 7 * time.Second}
		req, err := http.NewRequest(http.MethodPost, "http://caps/v1/files/read", strings.NewReader(`{"path":"greeting.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+broker.Token())
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d stalled behind idle connections: %v", i, err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status=%d err=%v", i, response.StatusCode, readErr)
		}
	}
}
