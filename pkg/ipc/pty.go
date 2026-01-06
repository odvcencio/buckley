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

	"github.com/odvcencio/buckley/pkg/storage"
)

type ptyMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

func (s *Server) handlePTY(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authorize(r)
	if !ok {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if scopeRank[strings.ToLower(principal.Scope)] < scopeRank[strings.ToLower(storage.TokenScopeMember)] {
		httpError(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.isWebSocketOriginAllowed(r) {
		httpError(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.ptyConnLimiter != nil && !s.ptyConnLimiter.Acquire() {
		httpError(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer func() {
		if s.ptyConnLimiter != nil {
			s.ptyConnLimiter.Release()
		}
	}()

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		httpError(w, "session id required", http.StatusBadRequest)
		return
	}

	if s.store == nil {
		httpError(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		httpError(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		httpError(w, "session not found", http.StatusNotFound)
		return
	}

	if !s.commandLimiter.Allow("pty:" + sessionID) {
		httpError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	providedToken := strings.TrimSpace(r.Header.Get("X-Buckley-Session-Token"))
	if providedToken == "" && isLoopbackBindAddress(s.cfg.BindAddress) {
		providedToken = strings.TrimSpace(r.URL.Query().Get("session_token"))
	}
	if providedToken != "" && !s.validateSessionTokenValue(sessionID, providedToken) {
		httpError(w, "invalid session token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Printf("pty websocket accept failed: %v", err)
		return
	}
	conn.SetReadLimit(maxWSReadBytesPTY)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	startWSPing(ctx, conn)

	if providedToken == "" {
		token, err := s.readPTYAuthToken(ctx, conn)
		if err != nil {
			_ = conn.Write(ctx, websocket.MessageText, marshalPTYError(err))
			conn.Close(websocket.StatusPolicyViolation, err.Error())
			return
		}
		if !s.validateSessionTokenValue(sessionID, token) {
			err := errors.New("invalid session token")
			_ = conn.Write(ctx, websocket.MessageText, marshalPTYError(err))
			conn.Close(websocket.StatusPolicyViolation, err.Error())
			return
		}
	}

	cmd := buildPTYCommand(r)
	ptmx, err := startPTY(cmd, r)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, marshalPTYError(err))
		conn.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer func() {
		_ = ptmx.Close()
	}()

	// Copy PTY output to websocket
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
				if payload, mErr := json.Marshal(packet); mErr == nil {
					writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					_ = conn.Write(writeCtx, websocket.MessageText, payload)
					cancel()
				}
			}
			if err != nil {
				exitStatus := exitCode(err)
				packet := ptyMessage{
					Type: "exit",
					Data: strconv.Itoa(exitStatus),
				}
				if payload, mErr := json.Marshal(packet); mErr == nil {
					_ = conn.Write(ctx, websocket.MessageText, payload)
				}
				return
			}
		}
	}()

	// Read inbound websocket messages
receiveLoop:
	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if messageType != websocket.MessageText {
			continue
		}

		var msg ptyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			if msg.Data == "" {
				continue
			}
			bytes, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				continue
			}
			_, _ = ptmx.Write(bytes)
		case "resize":
			if msg.Rows <= 0 || msg.Cols <= 0 {
				continue
			}
			rows, okRows := intToUint16(msg.Rows)
			cols, okCols := intToUint16(msg.Cols)
			if !okRows || !okCols {
				continue
			}
			_ = pty.Setsize(ptmx, &pty.Winsize{
				Rows: rows,
				Cols: cols,
			})
		case "close":
			break receiveLoop
		default:
			// ignore unknown message types
		}
	}

	cancel()
	<-outputDone
	_ = conn.Close(websocket.StatusNormalClosure, "pty closed")
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
