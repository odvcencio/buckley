package servo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	browserdpb "github.com/odvcencio/buckley/pkg/browser/adapters/servo/proto"
)

// Runtime is a Servo-backed browser runtime implementation.
type Runtime struct {
	cfg Config
}

// NewRuntime creates a Servo runtime adapter.
func NewRuntime(cfg Config) (*Runtime, error) {
	merged := cfg.withDefaults()
	if err := merged.Validate(); err != nil {
		return nil, err
	}
	return &Runtime{cfg: merged}, nil
}

// NewSession creates a browser session backed by browserd.
func (r *Runtime) NewSession(ctx context.Context, sessionCfg browser.SessionConfig) (browser.BrowserSession, error) {
	if r == nil {
		return nil, browser.ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(sessionCfg.SessionID) == "" {
		return nil, errors.New("session_id is required")
	}
	normalized := normalizeSessionConfig(sessionCfg, r.cfg.FrameRate)
	socketPath, err := resolveSocketPath(r.cfg.SocketDir, normalized.SessionID)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.cfg.BrowserdPath, "--socket", socketPath, "--session-id", normalized.SessionID)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start browserd: %w", err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	conn, err := dialBrowserd(ctx, socketPath, r.cfg.ConnectTimeout)
	if err != nil {
		_ = cmd.Process.Kill()
		<-waitDone
		return nil, err
	}
	sessionClient := newClient(conn)

	req := &browserdpb.Request{
		SessionId: normalized.SessionID,
		Payload: &browserdpb.Request_CreateSession{
			CreateSession: &browserdpb.CreateSessionRequest{
				Config: toProtoSessionConfig(normalized),
			},
		},
	}
	resp, err := sessionClient.send(ctx, req)
	if err != nil {
		_ = sessionClient.close()
		_ = cmd.Process.Kill()
		<-waitDone
		return nil, err
	}
	if resp.Error != nil {
		_ = sessionClient.close()
		_ = cmd.Process.Kill()
		<-waitDone
		return nil, fmt.Errorf("browserd error: %s", resp.Error.Message)
	}

	return &Session{
		id:             normalized.SessionID,
		cfg:            normalized,
		client:         sessionClient,
		cmd:            cmd,
		socketPath:     socketPath,
		waitDone:       waitDone,
		connectTimeout: r.cfg.ConnectTimeout,
	}, nil
}

// Close releases runtime resources.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	return nil
}

func resolveSocketPath(socketDir, sessionID string) (string, error) {
	dir := strings.TrimSpace(socketDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "buckley", "browserd")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create socket dir: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("%s.sock", sanitizeSessionID(sessionID))), nil
}

func sanitizeSessionID(sessionID string) string {
	out := strings.Builder{}
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			out.WriteRune(r)
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '-' || r == '_':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	if out.Len() == 0 {
		return "browser"
	}
	return out.String()
}

func normalizeSessionConfig(cfg browser.SessionConfig, frameRate int) browser.SessionConfig {
	merged := browser.DefaultSessionConfig()
	merged.SessionID = cfg.SessionID
	if cfg.InitialURL != "" {
		merged.InitialURL = cfg.InitialURL
	}
	if cfg.Viewport.Width != 0 {
		merged.Viewport.Width = cfg.Viewport.Width
	}
	if cfg.Viewport.Height != 0 {
		merged.Viewport.Height = cfg.Viewport.Height
	}
	if cfg.Viewport.DeviceScaleFactor != 0 {
		merged.Viewport.DeviceScaleFactor = cfg.Viewport.DeviceScaleFactor
	}
	if cfg.UserAgent != "" {
		merged.UserAgent = cfg.UserAgent
	}
	if cfg.Locale != "" {
		merged.Locale = cfg.Locale
	}
	if cfg.Timezone != "" {
		merged.Timezone = cfg.Timezone
	}
	if cfg.FrameRate != 0 {
		merged.FrameRate = cfg.FrameRate
	} else if frameRate != 0 {
		merged.FrameRate = frameRate
	}
	if len(cfg.NetworkAllowlist) > 0 {
		merged.NetworkAllowlist = cfg.NetworkAllowlist
	}
	if hasClipboardOverride(cfg.Clipboard) {
		merged.Clipboard = cfg.Clipboard
	}
	return merged
}

func hasClipboardOverride(policy browser.ClipboardPolicy) bool {
	if policy.Mode != "" {
		return true
	}
	if policy.AllowRead || policy.AllowWrite {
		return true
	}
	if policy.MaxBytes != 0 {
		return true
	}
	if len(policy.ReadAllowlist) > 0 {
		return true
	}
	return false
}

func dialBrowserd(ctx context.Context, socketPath string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	if timeout <= 0 {
		deadline = time.Now().Add(5 * time.Second)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout waiting for browserd")
	}
	return nil, fmt.Errorf("connect browserd: %w", lastErr)
}
