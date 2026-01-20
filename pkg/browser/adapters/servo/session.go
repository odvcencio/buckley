package servo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	browserdpb "github.com/odvcencio/buckley/pkg/browser/adapters/servo/proto"
)

// Session manages a browserd-backed session.
type Session struct {
	id     string
	cfg    browser.SessionConfig
	mu     sync.Mutex
	closed bool
	client *client
	cmd    *exec.Cmd

	socketPath       string
	waitDone         chan struct{}
	connectTimeout   time.Duration
	operationTimeout time.Duration
	maxReconnects    int
	reconnectAttempt int
}

// ID returns the session identifier.
func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

// Navigate requests navigation to a URL.
func (s *Session) Navigate(ctx context.Context, url string) (*browser.Observation, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.withOperationTimeout(ctx)
	defer cancel()

	req := &browserdpb.Request{
		SessionId: s.id,
		Payload: &browserdpb.Request_Navigate{
			Navigate: &browserdpb.NavigateRequest{Url: url},
		},
	}
	resp, err := s.sendWithReconnect(ctx, req)
	if err != nil {
		return nil, browser.WrapBrowserdError("navigate", "navigation failed", err)
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	payload, ok := resp.Payload.(*browserdpb.Response_Navigate)
	if !ok || payload.Navigate == nil {
		return nil, browser.NewBrowserdError("invalid_response", "navigate: unexpected response")
	}
	return fromProtoObservation(payload.Navigate.Observation), nil
}

// Observe returns a snapshot of the current browser state.
func (s *Session) Observe(ctx context.Context, opts browser.ObserveOptions) (*browser.Observation, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.withOperationTimeout(ctx)
	defer cancel()

	req := &browserdpb.Request{
		SessionId: s.id,
		Payload: &browserdpb.Request_Observe{
			Observe: &browserdpb.ObserveRequest{Options: toProtoObserveOptions(opts)},
		},
	}
	resp, err := s.sendWithReconnect(ctx, req)
	if err != nil {
		return nil, browser.WrapBrowserdError("observe", "observe failed", err)
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	payload, ok := resp.Payload.(*browserdpb.Response_Observe)
	if !ok || payload.Observe == nil {
		return nil, browser.NewBrowserdError("invalid_response", "observe: unexpected response")
	}
	return fromProtoObservation(payload.Observe.Observation), nil
}

// Act sends a user action to the browser runtime.
func (s *Session) Act(ctx context.Context, action browser.Action) (*browser.ActionResult, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.withOperationTimeout(ctx)
	defer cancel()

	req := &browserdpb.Request{
		SessionId: s.id,
		Payload: &browserdpb.Request_Act{
			Act: &browserdpb.ActRequest{Action: toProtoAction(action)},
		},
	}
	resp, err := s.sendWithReconnect(ctx, req)
	if err != nil {
		return nil, browser.WrapBrowserdError("act", "action failed", err)
	}
	if err := responseError(resp); err != nil {
		return nil, err
	}
	payload, ok := resp.Payload.(*browserdpb.Response_Act)
	if !ok || payload.Act == nil {
		return nil, browser.NewBrowserdError("invalid_response", "act: unexpected response")
	}
	return fromProtoActionResult(payload.Act.Result), nil
}

// Stream subscribes to browser events (frames, diffs, hit-test maps).
func (s *Session) Stream(ctx context.Context, opts browser.StreamOptions) (<-chan browser.StreamEvent, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized := opts
	if !normalized.IncludeFrames &&
		!normalized.IncludeDOMDiffs &&
		!normalized.IncludeAccessibilityDiffs &&
		!normalized.IncludeHitTest {
		normalized.IncludeFrames = true
	}
	if normalized.TargetFPS == 0 {
		normalized.TargetFPS = s.cfg.FrameRate
	}

	streamConn, err := dialBrowserd(ctx, s.socketPath, s.connectTimeout)
	if err != nil {
		return nil, err
	}
	streamClient := newClient(streamConn)

	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req := &browserdpb.Request{
		SessionId: s.id,
		Payload: &browserdpb.Request_StreamSubscribe{
			StreamSubscribe: &browserdpb.StreamSubscribeRequest{
				Options: toProtoStreamOptions(normalized),
			},
		},
	}
	resp, err := streamClient.send(subCtx, req)
	if err != nil {
		_ = streamClient.close()
		return nil, err
	}
	if err := responseError(resp); err != nil {
		_ = streamClient.close()
		return nil, err
	}
	payload, ok := resp.Payload.(*browserdpb.Response_StreamSubscribe)
	if !ok || payload.StreamSubscribe == nil || !payload.StreamSubscribe.Subscribed {
		_ = streamClient.close()
		return nil, browser.NewBrowserdError("stream_subscribe", "stream subscription failed")
	}

	events := make(chan browser.StreamEvent)
	go func() {
		defer close(events)
		defer func() {
			_ = streamClient.close()
		}()

		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = streamClient.close()
			case <-done:
			}
		}()
		defer close(done)

		for {
			// Set a read deadline based on context
			if deadline, ok := ctx.Deadline(); ok {
				_ = streamClient.conn.SetReadDeadline(deadline)
			}
			env, err := readEnvelope(streamClient.conn)
			if err != nil {
				return
			}
			switch msg := env.Message.(type) {
			case *browserdpb.Envelope_Event:
				event := fromProtoStreamEvent(msg.Event)
				if event == nil {
					continue
				}
				select {
				case events <- *event:
				case <-ctx.Done():
					return
				}
			case *browserdpb.Envelope_Response:
				continue
			default:
				continue
			}
		}
	}()

	return events, nil
}

// Close releases session resources.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	client := s.client
	cmd := s.cmd
	socketPath := s.socketPath
	waitDone := s.waitDone
	s.mu.Unlock()

	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = client.send(ctx, &browserdpb.Request{
			SessionId: s.id,
			Payload: &browserdpb.Request_CloseSession{
				CloseSession: &browserdpb.CloseSessionRequest{},
			},
		})
		cancel()
		_ = client.close()
	}

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if waitDone != nil {
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
	}
	if socketPath != "" {
		_ = os.Remove(socketPath)
	}
	return nil
}

func (s *Session) ensureOpen() error {
	if s == nil {
		return browser.ErrSessionClosed
	}
	if s.client == nil {
		return browser.ErrUnavailable
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return browser.ErrSessionClosed
	}
	return nil
}

// reconnect attempts to re-establish the connection to browserd.
func (s *Session) reconnect(ctx context.Context) error {
	if s == nil {
		return browser.ErrSessionClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return browser.ErrSessionClosed
	}
	if s.reconnectAttempt >= s.maxReconnects {
		s.mu.Unlock()
		return browser.ErrReconnectFailed
	}
	s.reconnectAttempt++
	oldClient := s.client
	socketPath := s.socketPath
	connectTimeout := s.connectTimeout
	s.mu.Unlock()

	if oldClient != nil {
		_ = oldClient.close()
	}

	conn, err := dialBrowserd(ctx, socketPath, connectTimeout)
	if err != nil {
		return browser.WrapBrowserdError("reconnect", "failed to reconnect", err)
	}
	newClient := newClient(conn)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = newClient.close()
		return browser.ErrSessionClosed
	}
	s.client = newClient
	s.reconnectAttempt = 0
	s.mu.Unlock()
	return nil
}

// sendWithReconnect sends a request with automatic reconnection on connection errors.
func (s *Session) sendWithReconnect(ctx context.Context, req *browserdpb.Request) (*browserdpb.Response, error) {
	resp, err := s.client.send(ctx, req)
	if err == nil {
		return resp, nil
	}
	if isTimeoutError(err) {
		return nil, fmt.Errorf("%w: %v", browser.ErrOperationTimeout, err)
	}
	if !browser.IsRetryableError(err) && !isNetworkError(err) {
		return nil, err
	}
	if err := s.reconnect(ctx); err != nil {
		return nil, err
	}
	return s.client.send(ctx, req)
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "use of closed")
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func responseError(resp *browserdpb.Response) error {
	if resp == nil {
		return browser.NewBrowserdError("empty_response", "missing response")
	}
	if resp.Error == nil {
		return nil
	}
	code := resp.Error.Code
	if code == "" {
		code = "unknown"
	}
	message := resp.Error.Message
	if message == "" {
		message = "operation failed"
	}
	return browser.NewBrowserdError(code, message)
}

// withOperationTimeout applies the session's operation timeout to a context if
// the context doesn't already have a deadline.
func (s *Session) withOperationTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	timeout := s.operationTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}
