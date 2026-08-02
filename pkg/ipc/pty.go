package ipc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"
	"nhooyr.io/websocket"

	"m31labs.dev/buckley/pkg/storage"
)

type ptyMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

type ptyRequest struct {
	sessionID     string
	providedToken string
	release       func()
}

func (s *Server) handlePTY(w http.ResponseWriter, r *http.Request) {
	req, ok := s.preparePTYRequest(w, r)
	if !ok {
		return
	}
	defer req.release()

	// InsecureSkipVerify disables the library's built-in Origin check.
	// Origin validation is already performed above via isWebSocketOriginAllowed,
	// so the library's check would be redundant.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logPTY("pty websocket accept failed: %v", err)
		return
	}
	conn.SetReadLimit(maxWSReadBytesPTY)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	startWSPing(ctx, conn)

	if !s.authenticatePTYConnection(ctx, conn, req.sessionID, req.providedToken) {
		return
	}

	cmd := buildPTYCommand(r)
	ptmx, err := startPTY(cmd, r)
	if err != nil {
		s.writePTYErrorAndClose(ctx, conn, err, websocket.StatusInternalError, "start")
		return
	}
	defer func() {
		_ = ptmx.Close()
	}()

	outputDone := s.forwardPTYOutput(ctx, conn, ptmx)
	s.handlePTYInput(ctx, conn, ptmx)

	cancel()
	_ = ptmx.Close() // Unblock Read in output goroutine.
	<-outputDone
	_ = cmd.Wait() // Reap child process to prevent zombies.
	if cErr := conn.Close(websocket.StatusNormalClosure, "pty closed"); cErr != nil {
		s.logPTY("[debug] pty: websocket close error: %v", cErr)
	}
}

func (s *Server) preparePTYRequest(w http.ResponseWriter, r *http.Request) (*ptyRequest, bool) {
	principal, ok := s.authorizePTYAccess(w, r)
	if !ok {
		return nil, false
	}
	release, ok := s.acquirePTYConnection(w)
	if !ok {
		return nil, false
	}

	sessionID, ok := s.validatePTYSession(w, r, principal)
	if !ok {
		release()
		return nil, false
	}

	providedToken, ok := s.resolvePTYProvidedToken(w, r, sessionID)
	if !ok {
		release()
		return nil, false
	}

	return &ptyRequest{
		sessionID:     sessionID,
		providedToken: providedToken,
		release:       release,
	}, true
}

func (s *Server) authorizePTYAccess(w http.ResponseWriter, r *http.Request) (*requestPrincipal, bool) {
	principal, ok := s.authorize(r)
	if !ok {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if scopeRank[strings.ToLower(principal.Scope)] < scopeRank[strings.ToLower(storage.TokenScopeMember)] {
		httpError(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	if !s.isWebSocketOriginAllowed(r) {
		httpError(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return principal, true
}

func (s *Server) acquirePTYConnection(w http.ResponseWriter) (func(), bool) {
	if s.ptyConnLimiter != nil && !s.ptyConnLimiter.Acquire() {
		httpError(w, "too many connections", http.StatusTooManyRequests)
		return nil, false
	}
	release := func() {
		if s.ptyConnLimiter != nil {
			s.ptyConnLimiter.Release()
		}
	}
	return release, true
}

func (s *Server) validatePTYSession(w http.ResponseWriter, r *http.Request, principal *requestPrincipal) (string, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		httpError(w, "session id required", http.StatusBadRequest)
		return "", false
	}

	if s.store == nil {
		httpError(w, "storage unavailable", http.StatusServiceUnavailable)
		return "", false
	}

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		httpError(w, "failed to load session", http.StatusInternalServerError)
		return "", false
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		httpError(w, "session not found", http.StatusNotFound)
		return "", false
	}

	if !s.commandLimiter.Allow("pty:" + sessionID) {
		httpError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return "", false
	}
	return sessionID, true
}

func (s *Server) resolvePTYProvidedToken(w http.ResponseWriter, r *http.Request, sessionID string) (string, bool) {
	providedToken := strings.TrimSpace(r.Header.Get("X-Buckley-Session-Token"))
	if providedToken == "" && isLoopbackBindAddress(s.cfg.BindAddress) {
		providedToken = strings.TrimSpace(r.URL.Query().Get("session_token"))
	}
	if providedToken != "" && !s.validateSessionTokenValue(sessionID, providedToken) {
		httpError(w, "invalid session token", http.StatusUnauthorized)
		return "", false
	}
	return providedToken, true
}

func (s *Server) authenticatePTYConnection(ctx context.Context, conn *websocket.Conn, sessionID, providedToken string) bool {
	if providedToken != "" {
		return true
	}
	token, err := s.readPTYAuthToken(ctx, conn)
	if err != nil {
		s.writePTYErrorAndClose(ctx, conn, err, websocket.StatusPolicyViolation, "auth")
		return false
	}
	if !s.validateSessionTokenValue(sessionID, token) {
		err := errors.New("invalid session token")
		s.writePTYErrorAndClose(ctx, conn, err, websocket.StatusPolicyViolation, "token")
		return false
	}
	return true
}

func (s *Server) forwardPTYOutput(ctx context.Context, conn *websocket.Conn, ptmx *os.File) <-chan struct{} {
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 {
				chunk := buffer[:n]
				packet := ptyMessage{
					Type: "data",
					Data: base64.StdEncoding.EncodeToString(chunk),
				}
				s.writePTYPacket(ctx, conn, packet, "data")
			}
			if err != nil {
				exitStatus := exitCode(err)
				packet := ptyMessage{
					Type: "exit",
					Data: strconv.Itoa(exitStatus),
				}
				s.writePTYPacket(ctx, conn, packet, "exit")
				return
			}
		}
	}()
	return outputDone
}

func (s *Server) handlePTYInput(ctx context.Context, conn *websocket.Conn, ptmx *os.File) {
	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			continue
		}

		var msg ptyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		if closePTY := s.handlePTYMessage(ptmx, msg); closePTY {
			return
		}
	}
}

func (s *Server) handlePTYMessage(ptmx *os.File, msg ptyMessage) bool {
	switch msg.Type {
	case "input":
		bytes, ok := decodePTYInput(msg.Data)
		if !ok {
			return false
		}
		if _, wErr := ptmx.Write(bytes); wErr != nil {
			s.logPTY("[debug] pty: write to pty failed: %v", wErr)
		}
	case "resize":
		size, ok := ptyWinsize(msg)
		if !ok {
			return false
		}
		_ = pty.Setsize(ptmx, size)
	case "close":
		return true
	default:
		// ignore unknown message types
	}
	return false
}

func decodePTYInput(data string) ([]byte, bool) {
	if data == "" {
		return nil, false
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, false
	}
	return bytes, true
}

func ptyWinsize(msg ptyMessage) (*pty.Winsize, bool) {
	rows, okRows := intToUint16(msg.Rows)
	cols, okCols := intToUint16(msg.Cols)
	if !okRows || !okCols {
		return nil, false
	}
	return &pty.Winsize{Rows: rows, Cols: cols}, true
}

func (s *Server) writePTYErrorAndClose(ctx context.Context, conn *websocket.Conn, err error, status websocket.StatusCode, label string) {
	if wErr := conn.Write(ctx, websocket.MessageText, marshalPTYError(err)); wErr != nil {
		s.logPTY("[debug] pty: websocket write error (%s): %v", label, wErr)
	}
	conn.Close(status, err.Error())
}

func (s *Server) writePTYPacket(ctx context.Context, conn *websocket.Conn, packet ptyMessage, label string) {
	payload, err := json.Marshal(packet)
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if wErr := conn.Write(writeCtx, websocket.MessageText, payload); wErr != nil {
		s.logPTY("[debug] pty: websocket write error (%s): %v", label, wErr)
	}
}

func (s *Server) logPTY(format string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *Server) readPTYAuthToken(ctx context.Context, conn *websocket.Conn) (string, error) {
	if conn == nil {
		return "", errors.New("pty connection unavailable")
	}
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		msgType, data, err := conn.Read(authCtx)
		if err != nil {
			return "", errors.New("missing session token")
		}
		if msgType != websocket.MessageText {
			continue
		}
		var msg ptyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Type)) {
		case "auth":
			token := strings.TrimSpace(msg.Data)
			if token == "" {
				return "", errors.New("missing session token")
			}
			return token, nil
		case "close":
			return "", errors.New("pty closed")
		default:
			// ignore until auth is received
		}
	}
}

func buildPTYCommand(r *http.Request) *exec.Cmd {
	query := r.URL.Query()
	rawCommand := strings.TrimSpace(query.Get("cmd"))
	var cmd *exec.Cmd

	if rawCommand == "" {
		userShell := os.Getenv("SHELL")
		if userShell == "" {
			// Platform-specific shell fallback
			if runtime.GOOS == "windows" {
				// Try PowerShell first, fall back to cmd.exe
				if pwsh, err := exec.LookPath("pwsh"); err == nil {
					userShell = pwsh
				} else if powershell, err := exec.LookPath("powershell"); err == nil {
					userShell = powershell
				} else {
					userShell = "cmd.exe"
				}
			} else {
				userShell = "/bin/bash"
			}
		}
		if runtime.GOOS == "windows" {
			cmd = exec.Command(userShell)
		} else {
			cmd = exec.Command(userShell, "-l")
		}
	} else {
		args := strings.Fields(rawCommand)
		cmd = exec.Command(args[0], args[1:]...)
	}

	cwd := strings.TrimSpace(query.Get("cwd"))
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cmd.Dir = abs
		}
	}
	cmd.Env = os.Environ()
	return cmd
}

func startPTY(cmd *exec.Cmd, r *http.Request) (*os.File, error) {
	rows, cols := parseSize(r)
	rows16, okRows := intToUint16(rows)
	cols16, okCols := intToUint16(cols)
	if okRows && okCols {
		return pty.StartWithSize(cmd, &pty.Winsize{
			Rows: rows16,
			Cols: cols16,
		})
	}
	return pty.Start(cmd)
}

func parseSize(r *http.Request) (rows, cols int) {
	query := r.URL.Query()
	rows, _ = strconv.Atoi(query.Get("rows"))
	cols, _ = strconv.Atoi(query.Get("cols"))
	return
}

func intToUint16(value int) (uint16, bool) {
	if value <= 0 || value > math.MaxUint16 {
		return 0, false
	}
	return uint16(value), true
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ProcessState != nil {
		return exitErr.ProcessState.ExitCode()
	}
	return -1
}

func marshalPTYError(err error) []byte {
	packet := ptyMessage{
		Type: "error",
		Data: err.Error(),
	}
	payload, _ := json.Marshal(packet)
	return payload
}

func httpError(w http.ResponseWriter, msg string, status int) {
	respondError(w, status, errors.New(msg))
}
