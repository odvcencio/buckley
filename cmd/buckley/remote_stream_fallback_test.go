package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestRemoteStreamEventsFallsBackToWebSocket(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		evt := remoteEvent{
			Type:      "session.updated",
			SessionID: r.URL.Query().Get("sessionId"),
			Timestamp: time.Now().UTC(),
		}
		payload, _ := json.Marshal(evt)
		_ = conn.Write(r.Context(), websocket.MessageText, payload)

		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := newRemoteClient(remoteBaseOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("newRemoteClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan remoteEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.streamEvents(ctx, "s1", events)
	}()

	select {
	case evt := <-events:
		if evt.Type != "session.updated" {
			t.Fatalf("event type=%q want %q", evt.Type, "session.updated")
		}
		if evt.SessionID != "s1" {
			t.Fatalf("event session=%q want %q", evt.SessionID, "s1")
		}
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for websocket fallback event")
	}

	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("streamEvents error: %v", err)
	}
}
