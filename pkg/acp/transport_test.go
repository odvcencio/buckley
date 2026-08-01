package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// driveTransportReadLoop mimics the response-routing half of Agent.Serve's
// read loop (see agent.go), without needing a full Agent: it reads
// messages from t and, for anything shaped like a response, dispatches it
// to a pending SendRequest caller. Notifications and requests are handed
// to onOther instead. It stops when ReadMessage returns an error (e.g. the
// writer side of the pipe closes).
func driveTransportReadLoop(t *Transport, onOther func(json.RawMessage)) {
	for {
		msg, err := t.ReadMessage()
		if err != nil {
			return
		}
		if IsResponseMessage(msg) {
			var resp Response
			if err := json.Unmarshal(msg, &resp); err == nil {
				t.dispatchResponse(&resp)
			}
			continue
		}
		if onOther != nil {
			onOther(msg)
		}
	}
}

// TestTransport_SendRequest_CorrelatesResponse locks the core of M3: a
// request sent via SendRequest is answered by the response carrying the
// same id, even though the id is chosen internally by the transport (the
// caller never sees or supplies it).
func TestTransport_SendRequest_CorrelatesResponse(t *testing.T) {
	t.Parallel()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	transport := NewTransport(fromClientR, toClientW)
	defer toClientW.Close()
	defer fromClientW.Close()

	// Something must read transport's incoming stream and route responses
	// to the pending SendRequest -- in production that's Agent.Serve.
	go driveTransportReadLoop(transport, nil)

	// Fake client: echo back whatever id it was asked, with a canned result.
	go func() {
		client := NewTransport(toClientR, fromClientW)
		for {
			msg, err := client.ReadMessage()
			if err != nil {
				return
			}
			var req Request
			if err := json.Unmarshal(msg, &req); err != nil || req.Method == "" {
				continue
			}
			_ = client.SendResponse(req.ID, map[string]string{"echo": req.Method})
		}
	}()

	resp, err := transport.SendRequest(context.Background(), "session/request_permission", map[string]string{"sessionId": "s1"}, 5*time.Second)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error result: %+v", resp.Error)
	}
	data, _ := json.Marshal(resp.Result)
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["echo"] != "session/request_permission" {
		t.Fatalf("result = %v, want echo of the method", got)
	}
}

// TestTransport_SendRequest_TimesOut asserts SendRequest returns an error
// (rather than blocking forever) when the client never responds, and that
// a late response arriving after the timeout does not panic or leak.
func TestTransport_SendRequest_TimesOut(t *testing.T) {
	t.Parallel()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	transport := NewTransport(fromClientR, toClientW)
	defer toClientW.Close()
	defer fromClientW.Close()

	// Drain what Buckley sends but never answer.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := toClientR.Read(buf); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	_, err := transport.SendRequest(context.Background(), "session/request_permission", map[string]string{"sessionId": "s1"}, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SendRequest took %s, want it to return promptly after the timeout", elapsed)
	}

	// Pending map must be cleaned up after a timeout, not leaked.
	transport.pendingMu.Lock()
	n := len(transport.pending)
	transport.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending requests = %d after timeout, want 0", n)
	}
}

// TestTransport_SendRequest_HonorsContextCancellation asserts a cancelled
// ctx unblocks SendRequest even when timeout is unset (turn cancellation).
func TestTransport_SendRequest_HonorsContextCancellation(t *testing.T) {
	t.Parallel()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	transport := NewTransport(fromClientR, toClientW)
	defer toClientW.Close()
	defer fromClientW.Close()

	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := toClientR.Read(buf); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := transport.SendRequest(ctx, "session/request_permission", nil, 0)
	if err == nil {
		t.Fatal("expected a context-cancellation error")
	}
}

// TestTransport_SendRequest_ConcurrentRequestsDoNotCrossRespond drives
// several concurrent SendRequest calls against one fake client and asserts
// each caller gets back exactly the response addressed to its own request,
// proving the pending-response map is concurrency-safe and correctly keyed.
func TestTransport_SendRequest_ConcurrentRequestsDoNotCrossRespond(t *testing.T) {
	t.Parallel()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	transport := NewTransport(fromClientR, toClientW)
	defer toClientW.Close()
	defer fromClientW.Close()

	go driveTransportReadLoop(transport, nil)

	go func() {
		client := NewTransport(toClientR, fromClientW)
		for {
			msg, err := client.ReadMessage()
			if err != nil {
				return
			}
			var req Request
			if err := json.Unmarshal(msg, &req); err != nil || req.Method == "" {
				continue
			}
			var params map[string]string
			_ = json.Unmarshal(req.Params, &params)
			_ = client.SendResponse(req.ID, map[string]string{"sessionId": params["sessionId"]})
		}
	}()

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		sessionID := "s" + string(rune('a'+i))
		go func(sessionID string) {
			resp, err := transport.SendRequest(context.Background(), "session/request_permission", map[string]string{"sessionId": sessionID}, 5*time.Second)
			if err != nil {
				errs <- err
				return
			}
			data, _ := json.Marshal(resp.Result)
			var got map[string]string
			if err := json.Unmarshal(data, &got); err != nil {
				errs <- err
				return
			}
			if got["sessionId"] != sessionID {
				errs <- errFmtSessionMismatch(sessionID, got["sessionId"])
				return
			}
			errs <- nil
		}(sessionID)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// TestTransport_InterleavedNotificationsDoNotDisruptPendingRequests proves
// notifications (and inbound requests) arriving while a SendRequest is
// outstanding do not get misrouted as its response, and don't block the
// correlated response from arriving.
func TestTransport_InterleavedNotificationsDoNotDisruptPendingRequests(t *testing.T) {
	t.Parallel()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	transport := NewTransport(fromClientR, toClientW)
	defer toClientW.Close()
	defer fromClientW.Close()

	var otherMessages []json.RawMessage
	done := make(chan struct{})
	go driveTransportReadLoop(transport, func(msg json.RawMessage) {
		otherMessages = append(otherMessages, msg)
	})

	go func() {
		defer close(done)
		client := NewTransport(toClientR, fromClientW)
		for {
			msg, err := client.ReadMessage()
			if err != nil {
				return
			}
			var req Request
			if err := json.Unmarshal(msg, &req); err != nil || req.Method == "" {
				continue
			}
			// Send two unrelated notifications before answering, to prove
			// they don't get consumed as the pending response.
			_ = client.SendNotification("session/update", map[string]string{"sessionId": "s1"})
			_ = client.SendNotification("session/update", map[string]string{"sessionId": "s2"})
			_ = client.SendResponse(req.ID, map[string]string{"outcome": "selected"})
			return
		}
	}()

	resp, err := transport.SendRequest(context.Background(), "session/request_permission", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if resp.Result == nil {
		t.Fatal("expected a result despite interleaved notifications")
	}
	<-done
}

func errFmtSessionMismatch(want, got string) error {
	return &sessionMismatchError{want: want, got: got}
}

type sessionMismatchError struct{ want, got string }

func (e *sessionMismatchError) Error() string {
	return "session id mismatch: want " + e.want + " got " + e.got
}
