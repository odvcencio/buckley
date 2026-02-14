package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/term"
	"nhooyr.io/websocket"

	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
)

type ptyMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

func (c *remoteClient) openRemotePTY(ctx context.Context, sessionID, sessionToken, command string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("remote console requires an interactive TTY")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enable raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()
	rows, cols, err := term.GetSize(fd)
	if err != nil {
		rows, cols = 40, 120
	}
	ptyURL := c.ptyURL(sessionID, rows, cols, command)
	header := http.Header{}
	if c.basicHeader != "" {
		header.Set("Authorization", c.basicHeader)
	} else if c.ipcToken != "" {
		header.Set("Authorization", "Bearer "+c.ipcToken)
	}
	header.Set("X-Buckley-Session-Token", sessionToken)
	c.attachCookies(header, ptyURL)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, ptyURL, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if err != nil {
		return formatWebSocketDialError(resp, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "console closed")
	resize := func(r, c int) error {
		msg := ptyMessage{Type: "resize", Rows: r, Cols: c}
		payload, _ := json.Marshal(msg)
		return conn.Write(ctx, websocket.MessageText, payload)
	}
	if err := resize(rows, cols); err != nil {
		return fmt.Errorf("sending initial pty resize: %w", err)
	}
	sigCh := make(chan os.Signal, 1)
	registerTerminalResize(sigCh)
	defer unregisterTerminalResize(sigCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				r, c, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				_ = resize(r, c)
			}
		}
	}()
	inputErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				msg := ptyMessage{Type: "input", Data: base64.StdEncoding.EncodeToString(buf[:n])}
				payload, _ := json.Marshal(msg)
				if writeErr := conn.Write(ctx, websocket.MessageText, payload); writeErr != nil {
					inputErr <- writeErr
					return
				}
			}
			if err != nil {
				inputErr <- err
				return
			}
		}
	}()
	for {
		select {
		case err := <-inputErr:
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("pty input: %w", err)
		default:
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return fmt.Errorf("reading pty data: %w", err)
		}
		var msg ptyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "data":
			chunk, decodeErr := base64.StdEncoding.DecodeString(msg.Data)
			if decodeErr == nil {
				_, _ = os.Stdout.Write(chunk)
			}
		case "exit":
			return nil
		}
	}
}

var errConnectSubscribeUnavailable = errors.New("connect subscribe unavailable")

func formatConnectAuthError(err *connect.Error) error {
	if err == nil {
		return fmt.Errorf("remote stream unauthorized (%s)", remoteAuthHint)
	}
	msg := strings.TrimSpace(err.Message())
	if msg == "" {
		msg = strings.TrimSpace(err.Error())
	}
	if msg == "" {
		msg = "unauthorized"
	}
	return fmt.Errorf("remote stream unauthorized: %s (%s)", msg, remoteAuthHint)
}

func (c *remoteClient) streamEvents(ctx context.Context, sessionID string, events chan<- remoteEvent) error {
	if err := c.streamEventsConnect(ctx, sessionID, events); err != nil {
		if errors.Is(err, errConnectSubscribeUnavailable) {
			fmt.Fprintln(os.Stderr, "Connect Subscribe unavailable; falling back to WebSocket event stream.")
			return c.streamEventsWS(ctx, sessionID, events)
		}
		return fmt.Errorf("streaming events: %w", err)
	}
	return nil
}

func (c *remoteClient) streamEventsConnect(ctx context.Context, sessionID string, events chan<- remoteEvent) error {
	grpcClient := ipcpbconnect.NewBuckleyIPCClient(c.streamHTTPClient(), c.baseURL.String())
	backoff := 500 * time.Millisecond
	maxBackoff := 30 * time.Second
	connectedOnce := false

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("connect event stream cancelled: %w", err)
		}

		req := connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: strings.TrimSpace(sessionID)})
		if c.basicHeader != "" {
			req.Header().Set("Authorization", c.basicHeader)
		} else if c.ipcToken != "" {
			req.Header().Set("Authorization", "Bearer "+c.ipcToken)
		}

		stream, err := grpcClient.Subscribe(ctx, req)
		if err != nil {
			var cerr *connect.Error
			if errors.As(err, &cerr) {
				switch cerr.Code() {
				case connect.CodeUnauthenticated, connect.CodePermissionDenied:
					return formatConnectAuthError(cerr)
				case connect.CodeUnimplemented, connect.CodeNotFound:
					return errConnectSubscribeUnavailable
				}
			}
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "404") || strings.Contains(msg, "not found") {
				return errConnectSubscribeUnavailable
			}
			if connectedOnce {
				fmt.Fprintf(os.Stderr, "remote stream reconnect failed: %v (retrying in %s)\n", err, backoff)
			} else {
				fmt.Fprintf(os.Stderr, "remote stream connect failed: %v (retrying in %s)\n", err, backoff)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		connectedOnce = true
		backoff = 500 * time.Millisecond
		for stream.Receive() {
			msg := stream.Msg()
			if msg == nil {
				continue
			}
			switch msg.GetType() {
			case "server.hello", "server.keepalive":
				continue
			}

			var payload json.RawMessage
			if msg.GetPayload() != nil {
				if data, mErr := json.Marshal(msg.GetPayload().AsMap()); mErr == nil {
					payload = data
				}
			}
			ts := time.Time{}
			if msg.GetTimestamp() != nil {
				ts = msg.GetTimestamp().AsTime()
			}

			select {
			case events <- remoteEvent{
				Type:      msg.GetType(),
				SessionID: msg.GetSessionId(),
				Payload:   payload,
				Timestamp: ts,
			}:
			case <-ctx.Done():
				_ = stream.Close()
				return ctx.Err()
			}
		}

		err = stream.Err()
		_ = stream.Close()
		if err == nil {
			err = fmt.Errorf("connect stream ended")
		}
		var cerr *connect.Error
		if errors.As(err, &cerr) {
			switch cerr.Code() {
			case connect.CodeUnauthenticated, connect.CodePermissionDenied:
				return formatConnectAuthError(cerr)
			case connect.CodeUnimplemented:
				return errConnectSubscribeUnavailable
			}
		}
		if errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		fmt.Fprintf(os.Stderr, "remote stream disconnected: %v (reconnecting in %s)\n", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *remoteClient) streamEventsWS(ctx context.Context, sessionID string, events chan<- remoteEvent) error {
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{},
	}
	if c.basicHeader != "" {
		opts.HTTPHeader.Set("Authorization", c.basicHeader)
	} else if c.ipcToken != "" {
		opts.HTTPHeader.Set("Authorization", "Bearer "+c.ipcToken)
	}
	wsEndpoint := c.wsURL(sessionID)
	backoff := 500 * time.Millisecond
	maxBackoff := 30 * time.Second
	connectedOnce := false
	consecutiveErrors := 0
	maxConsecutiveErrors := 5

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("websocket event stream cancelled: %w", err)
		}

		// After too many consecutive errors, back off more aggressively
		if consecutiveErrors >= maxConsecutiveErrors {
			fmt.Fprintf(os.Stderr, "remote stream: %d consecutive errors, backing off for 60s\n", consecutiveErrors)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(60 * time.Second):
			}
			consecutiveErrors = 0
		}

		c.attachCookies(opts.HTTPHeader, wsEndpoint)
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, resp, err := websocket.Dial(dialCtx, wsEndpoint, opts)
		cancel()
		if err != nil {
			consecutiveErrors++
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			dialErr := formatWebSocketDialError(resp, err)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				return dialErr
			}
			if connectedOnce {
				fmt.Fprintf(os.Stderr, "remote stream reconnect failed: %v (retrying in %s)\n", dialErr, backoff)
			} else {
				fmt.Fprintf(os.Stderr, "remote stream connect failed: %v (retrying in %s)\n", dialErr, backoff)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reset error counter and backoff on successful connection
		connectedOnce = true
		consecutiveErrors = 0
		backoff = 500 * time.Millisecond
		conn.SetReadLimit(32 << 20)

		// Start keepalive ping goroutine
		pingDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-pingDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					err := conn.Ping(pingCtx)
					cancel()
					if err != nil {
						// Ping failed, let the read loop handle reconnection
						return
					}
				}
			}
		}()

		for {
			// Set read deadline for health monitoring
			readCtx, readCancel := context.WithTimeout(ctx, 90*time.Second)
			_, data, err := conn.Read(readCtx)
			readCancel()
			if err != nil {
				close(pingDone)
				_ = conn.Close(websocket.StatusNormalClosure, "client closed")
				if errors.Is(err, context.Canceled) {
					return ctx.Err()
				}
				consecutiveErrors++
				fmt.Fprintf(os.Stderr, "remote stream disconnected: %v (reconnecting in %s)\n", err, backoff)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				break
			}

			// Reset consecutive errors on successful read
			consecutiveErrors = 0

			var evt remoteEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				continue
			}
			select {
			case events <- evt:
			case <-ctx.Done():
				close(pingDone)
				_ = conn.Close(websocket.StatusNormalClosure, "client closed")
				return ctx.Err()
			}
		}
	}
}

func formatWebSocketDialError(resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
	body := formatIPCErrorBody(data)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if body != "" {
			return fmt.Errorf("websocket connection failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
		}
		return fmt.Errorf("websocket connection failed (%s): %s", resp.Status, remoteAuthHint)
	}

	if body != "" {
		return fmt.Errorf("websocket connection failed (%s): %s", resp.Status, body)
	}
	if err != nil {
		return fmt.Errorf("websocket connection failed (%s): %v", resp.Status, err)
	}
	return fmt.Errorf("websocket connection failed (%s)", resp.Status)
}
