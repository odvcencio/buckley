package ipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestMissionControlSmokeFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY smoke test is not supported on Windows")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	session := &storage.Session{
		ID:          "smoke-session",
		ProjectPath: tmpDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	appCfg := &config.Config{}
	server := NewServer(Config{
		BindAddress:    "127.0.0.1:4488",
		AllowedOrigins: []string{"*"},
		RequireToken:   true,
		AuthToken:      "unit-token",
		ProjectRoot:    tmpDir,
	}, store, nil, command.NewGateway(), nil, appCfg, nil, nil)
	server.commandLimiter = newRateLimiter(0)

	router := chi.NewRouter()
	router.Use(server.corsMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)

	protected := chi.NewRouter()
	protected.Use(server.authMiddleware)

	api := chi.NewRouter()
	api.Route("/auth", func(r chi.Router) {
		r.Get("/session", server.handleAuthSession)
	})
	api.Post("/sessions/{sessionID}/tokens", server.handleSessionToken)
	protected.Mount("/api", api)
	router.Mount("/", protected)

	router.Get("/ws/pty", server.handlePTY)

	server.grpcService = NewGRPCService(server)
	server.hub.AddForwarder(server.grpcService)
	grpcPath, grpcHandler := ipcpbconnect.NewBuckleyIPCHandler(
		server.grpcService,
		connect.WithReadMaxBytes(maxConnectReadBytes),
		connect.WithCompressMinBytes(1024),
	)
	router.With(server.authContextMiddleware).Mount(grpcPath, grpcHandler)

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	httpClient := ts.Client()

	authResp, err := doJSONRequest(
		httpClient,
		http.MethodGet,
		ts.URL+"/api/auth/session",
		nil,
		map[string]string{
			"Authorization":            "Bearer unit-token",
			"Connect-Protocol-Version": "1",
		},
	)
	if err != nil {
		t.Fatalf("auth session request failed: %v", err)
	}
	t.Cleanup(func() { _ = authResp.Body.Close() })
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("auth session status=%d", authResp.StatusCode)
	}

	var authBody struct {
		Principal struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"principal"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&authBody); err != nil {
		t.Fatalf("decode auth session response: %v", err)
	}
	if authBody.Principal.Scope == "" {
		t.Fatalf("expected principal scope in auth response")
	}

	authCookie := findCookie(authResp.Cookies(), sessionCookieName)
	if authCookie == nil || strings.TrimSpace(authCookie.Value) == "" {
		t.Fatalf("expected %s cookie in auth response", sessionCookieName)
	}

	listResp, err := doJSONRequest(
		httpClient,
		http.MethodPost,
		ts.URL+"/buckley.ipc.v1.BuckleyIPC/ListSessions",
		strings.NewReader(`{"limit": 10}`),
		map[string]string{
			"Authorization":            "Bearer unit-token",
			"Content-Type":             "application/json",
			"Connect-Protocol-Version": "1",
		},
	)
	if err != nil {
		t.Fatalf("list sessions request failed: %v", err)
	}
	t.Cleanup(func() { _ = listResp.Body.Close() })
	if listResp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(listResp.Body, 4096))
		t.Fatalf("list sessions status=%d body=%s", listResp.StatusCode, snippet)
	}
	var listBody struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
		TotalCount int `json:"totalCount"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list sessions response: %v", err)
	}
	if len(listBody.Sessions) == 0 {
		t.Fatalf("expected at least one session in list response")
	}

	streamTypes, streamLines, err := readConnectEventTypes(
		httpClient,
		ts.URL+"/buckley.ipc.v1.BuckleyIPC/Subscribe",
		`{"sessionId":"","eventTypes":["sessions.*","session.*"],"lastEventId":"","includeAgentEvents":false}`,
		map[string]string{
			"Authorization":            "Bearer unit-token",
			"Content-Type":             "application/connect+json",
			"Connect-Protocol-Version": "1",
			"Connect-Accept-Encoding":  "identity",
			"Accept":                   "application/connect+json",
			"Accept-Encoding":          "identity",
			"Cache-Control":            "no-cache",
			"Pragma":                   "no-cache",
			"X-Requested-With":         "MissionControlSmokeTest",
			"X-Mission-Control-Smoke":  "1",
		},
		8,
		5*time.Second,
	)
	if err != nil {
		t.Fatalf("subscribe stream failed: %v", err)
	}
	if !streamTypes["server.hello"] {
		t.Fatalf("expected server.hello in Subscribe stream, got=%v lines=%q", keys(streamTypes), streamLines)
	}
	if !streamTypes["sessions.snapshot"] {
		t.Fatalf("expected sessions.snapshot in Subscribe stream, got=%v lines=%q", keys(streamTypes), streamLines)
	}

	tokenResp, err := doJSONRequest(
		httpClient,
		http.MethodPost,
		ts.URL+"/api/sessions/"+url.PathEscape(session.ID)+"/tokens",
		strings.NewReader(`{}`),
		map[string]string{
			"Authorization": "Bearer unit-token",
			"Content-Type":  "application/json",
		},
	)
	if err != nil {
		t.Fatalf("issue session token failed: %v", err)
	}
	t.Cleanup(func() { _ = tokenResp.Body.Close() })
	if tokenResp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(tokenResp.Body, 4096))
		t.Fatalf("issue session token status=%d body=%s", tokenResp.StatusCode, snippet)
	}
	var tokenBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenBody); err != nil {
		t.Fatalf("decode session token response: %v", err)
	}
	if strings.TrimSpace(tokenBody.Token) == "" {
		t.Fatalf("expected non-empty session token")
	}

	ptyURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	ptyURL.Scheme = strings.Replace(ptyURL.Scheme, "http", "ws", 1)
	ptyURL.Path = "/ws/pty"
	q := ptyURL.Query()
	q.Set("sessionId", session.ID)
	q.Set("cwd", tmpDir)
	ptyURL.RawQuery = q.Encode()

	header := http.Header{}
	header.Add("Cookie", fmt.Sprintf("%s=%s", authCookie.Name, authCookie.Value))
	header.Add("Origin", ts.URL)

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, ptyURL.String(), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("pty websocket dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWrite()
	if err := writePTYMessage(writeCtx, conn, ptyMessage{Type: "auth", Data: tokenBody.Token}); err != nil {
		t.Fatalf("pty auth write failed: %v", err)
	}

	marker := "BUCKLEY_MISSION_CONTROL_SMOKE_OK"
	if err := writePTYMessage(writeCtx, conn, ptyMessage{Type: "input", Data: base64.StdEncoding.EncodeToString([]byte("echo " + marker + "\n"))}); err != nil {
		t.Fatalf("pty input write failed: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var output strings.Builder
	for time.Now().Before(deadline) {
		readCtx, cancelRead := context.WithTimeout(context.Background(), time.Until(deadline))
		msgType, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			t.Fatalf("pty read failed: %v", err)
		}
		if msgType != websocket.MessageText {
			continue
		}
		var msg ptyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "data":
			if msg.Data == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				continue
			}
			output.Write(decoded)
			if strings.Contains(output.String(), marker) {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 1*time.Second)
				_ = writePTYMessage(closeCtx, conn, ptyMessage{Type: "close"})
				cancelClose()
				return
			}
		case "error":
			t.Fatalf("pty error: %s", msg.Data)
		case "exit":
			t.Fatalf("pty exited early: %s output=%q", msg.Data, output.String())
		}
	}
	t.Fatalf("pty output did not contain marker %q; output=%q", marker, output.String())
}

func doJSONRequest(client *http.Client, method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func writePTYMessage(ctx context.Context, conn *websocket.Conn, msg ptyMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func readConnectEventTypes(client *http.Client, url, body string, headers map[string]string, maxFrames int, timeout time.Duration) (map[string]bool, []string, error) {
	framed := encodeConnectFrame([]byte(body))
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(framed))
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req = req.WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, nil, fmt.Errorf("stream status=%d body=%s", resp.StatusCode, snippet)
	}

	types := make(map[string]bool)
	framesSeen := make([]string, 0, maxFrames)
	defer resp.Body.Close()

	for i := 0; i < maxFrames; i++ {
		header := make([]byte, 5)
		if _, err := io.ReadFull(resp.Body, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return types, framesSeen, err
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length == 0 {
			continue
		}
		if length > 1<<20 {
			return types, framesSeen, fmt.Errorf("connect frame too large: %d bytes", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(resp.Body, payload); err != nil {
			return types, framesSeen, err
		}

		text := strings.TrimSpace(string(payload))
		if text == "" {
			continue
		}
		if len(framesSeen) < maxFrames {
			framesSeen = append(framesSeen, text)
		}

		eventType, streamErr := extractEventTypeFromConnectPayload(payload)
		if streamErr != nil {
			return types, framesSeen, streamErr
		}
		if eventType != "" {
			types[eventType] = true
			if types["server.hello"] && types["sessions.snapshot"] {
				break
			}
		}
	}
	return types, framesSeen, nil
}

func encodeConnectFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func extractEventTypeFromConnectPayload(payload []byte) (string, error) {
	type envelope struct {
		Result json.RawMessage `json:"result,omitempty"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Type string `json:"type,omitempty"`
	}

	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", nil
	}
	if env.Error != nil {
		msg := strings.TrimSpace(env.Error.Message)
		if msg == "" {
			msg = "stream error"
		}
		if strings.TrimSpace(env.Error.Code) != "" {
			msg = env.Error.Code + ": " + msg
		}
		return "", fmt.Errorf("%s", msg)
	}
	if len(env.Result) > 0 {
		var inner struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(env.Result, &inner); err == nil {
			return inner.Type, nil
		}
	}
	return env.Type, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
