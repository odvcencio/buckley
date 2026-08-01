package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"m31labs.dev/buckley/v2/pkg/config"
	ipcpb "m31labs.dev/buckley/v2/pkg/ipc/proto"
	"m31labs.dev/buckley/v2/pkg/ipc/proto/ipcpbconnect"
)

// defaultAttachAddress is the loopback address `buckley serve` binds by
// default (pkg/ipc.Config.BindAddress default). `buckley attach` targets
// the same address unless overridden, so a second terminal can join a
// locally running server with no flags.
const defaultAttachAddress = "http://127.0.0.1:4488"

type attachOptions struct {
	Addr      string
	Token     string
	SessionID string
}

// runAttachCommand implements `buckley attach [session-id]`. It speaks the
// same loopback Connect/gRPC surface pkg/ipc already exposes (ListSessions,
// GetSession, IssueSessionToken, Subscribe, SendCommand); it does not open
// a second session store or a REST-first path.
func runAttachCommand(args []string) error {
	opts, err := parseAttachFlags(args)
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 0}
	client := ipcpbconnect.NewBuckleyIPCClient(httpClient, opts.Addr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if opts.SessionID == "" {
		return runAttachList(ctx, client)
	}
	return runAttachSession(ctx, client, opts)
}

func parseAttachFlags(args []string) (attachOptions, error) {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	addr := fs.String("addr", resolveDefaultAttachAddress(), "Address of the local Buckley IPC server")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	token := fs.String("token", defaultToken, "IPC bearer token (BUCKLEY_IPC_TOKEN by default)")
	if err := fs.Parse(args); err != nil {
		return attachOptions{}, err
	}

	opts := attachOptions{
		Addr:  normalizeAttachAddress(*addr),
		Token: strings.TrimSpace(*token),
	}
	if rest := fs.Args(); len(rest) > 0 {
		opts.SessionID = strings.TrimSpace(rest[0])
	}
	return opts, nil
}

// resolveDefaultAttachAddress picks --addr's default: BUCKLEY_IPC_ADDR,
// then the resolved config's ipc.bind, then the loopback default. It reads
// the environment and config; normalizeAttachAddress does the pure part.
func resolveDefaultAttachAddress() string {
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_ADDR")); v != "" {
		return normalizeAttachAddress(v)
	}
	if cfg, err := config.Load(); err == nil && cfg != nil {
		if bind := strings.TrimSpace(cfg.IPC.Bind); bind != "" {
			return normalizeAttachAddress(bind)
		}
	}
	return defaultAttachAddress
}

// normalizeAttachAddress turns a bare host:port (as pkg/ipc.Config.BindAddress
// and `buckley serve --bind` use) into an http(s) base URL a Connect client
// can dial. A value that already carries a scheme passes through unchanged.
func normalizeAttachAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return defaultAttachAddress
	}
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

func attachAuthHeader(req connect.AnyRequest, token string) {
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
}

func runAttachList(ctx context.Context, client ipcpbconnect.BuckleyIPCClient) error {
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req := connect.NewRequest(&ipcpb.ListSessionsRequest{})
	resp, err := client.ListSessions(listCtx, req)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	fmt.Print(renderSessionList(resp.Msg.GetSessions()))
	return nil
}

// renderSessionList is the pure formatter behind `buckley attach` (no
// session ID): a compact table, one line per session.
func renderSessionList(sessions []*ipcpb.SessionSummary) string {
	if len(sessions) == 0 {
		return "No active sessions\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %-10s %-24s %s\n", "SESSION", "STATUS", "LAST ACTIVE", "BRANCH")
	for _, sess := range sessions {
		last := ""
		if ts := sess.GetLastActive(); ts != nil {
			last = ts.AsTime().Local().Format(time.RFC822)
		}
		fmt.Fprintf(&b, "%-16s %-10s %-24s %s\n", sess.GetId(), sess.GetStatus(), last, sess.GetGitBranch())
	}
	return b.String()
}

func runAttachSession(ctx context.Context, client ipcpbconnect.BuckleyIPCClient, opts attachOptions) error {
	detailCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	detailReq := connect.NewRequest(&ipcpb.GetSessionRequest{SessionId: opts.SessionID, MessageLimit: 10})
	attachAuthHeader(detailReq, opts.Token)
	detail, err := client.GetSession(detailCtx, detailReq)
	cancel()
	if err != nil {
		return fmt.Errorf("get session %s: %w", opts.SessionID, err)
	}
	fmt.Print(renderSessionHeader(detail.Msg))

	sessionToken := ""
	tokenCtx, tokenCancel := context.WithTimeout(ctx, 15*time.Second)
	tokenReq := connect.NewRequest(&ipcpb.IssueSessionTokenRequest{SessionId: opts.SessionID})
	attachAuthHeader(tokenReq, opts.Token)
	tokenResp, tokenErr := client.IssueSessionToken(tokenCtx, tokenReq)
	tokenCancel()
	if tokenErr != nil {
		fmt.Fprintf(os.Stderr, "observing only (could not mint a session token to drive it: %v)\n", tokenErr)
	} else {
		sessionToken = tokenResp.Msg.GetToken()
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAttachStream(streamCtx, client, opts)
	}()

	fmt.Printf("Attached to session %s at %s. Ctrl+C detaches without stopping the session.\n", opts.SessionID, opts.Addr)
	if sessionToken == "" {
		fmt.Println("Observation only: no session token, so typed input will not be sent.")
	}

	if err := attachInputLoop(ctx, client, opts, sessionToken); err != nil && !errors.Is(err, context.Canceled) {
		streamCancel()
		<-done
		return err
	}
	streamCancel()
	<-done
	fmt.Println("Detached.")
	return nil
}

// renderSessionHeader is the pure formatter for GetSession's response: the
// session's current state plus its recent transcript.
func renderSessionHeader(detail *ipcpb.SessionDetail) string {
	if detail == nil || detail.GetSession() == nil {
		return ""
	}
	sess := detail.GetSession()
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s status=%s branch=%s\n", sess.GetId(), sess.GetStatus(), sess.GetGitBranch())
	for _, msg := range detail.GetRecentMessages() {
		fmt.Fprintf(&b, "  [%s] %s\n", strings.ToUpper(msg.GetRole()), strings.TrimSpace(msg.GetContent()))
	}
	return b.String()
}

func runAttachStream(ctx context.Context, client ipcpbconnect.BuckleyIPCClient, opts attachOptions) {
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		req := connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: opts.SessionID})
		attachAuthHeader(req, opts.Token)
		stream, err := client.Subscribe(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "stream connect failed: %v (retrying in %s)\n", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 500 * time.Millisecond
		for stream.Receive() {
			if line := formatAttachEvent(stream.Msg()); line != "" {
				fmt.Println(line)
			}
		}
		_ = stream.Close()
		if ctx.Err() != nil {
			return
		}
		if err := stream.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "stream disconnected: %v (reconnecting in %s)\n", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// formatAttachEvent is the pure, network-free rendering step behind
// `buckley attach`'s compact line-per-event view. It reuses
// formatTelemetryDetail (cmd/buckley/remote.go), the same helper `buckley
// remote attach` renders telemetry events with, so both commands describe
// the same event stream identically.
func formatAttachEvent(evt *ipcpb.Event) string {
	if evt == nil {
		return ""
	}
	switch evt.GetType() {
	case "server.hello", "server.keepalive", "server.backpressure", "sessions.snapshot", "view.patch":
		return ""
	}

	prefix := ""
	if ts := evt.GetTimestamp(); ts != nil && ts.IsValid() {
		prefix = "[" + ts.AsTime().Local().Format("15:04:05") + "] "
	}

	payload := map[string]any{}
	if s := evt.GetPayload(); s != nil {
		payload = s.AsMap()
	}

	switch evt.GetType() {
	case "message.created", "message.updated":
		role, _ := payload["role"].(string)
		content, _ := payload["content"].(string)
		return fmt.Sprintf("%s[%s] %s", prefix, strings.ToUpper(role), strings.TrimSpace(content))
	case "tool.started":
		name, _ := payload["name"].(string)
		return fmt.Sprintf("%s> %s started", prefix, name)
	case "tool.completed":
		name, _ := payload["name"].(string)
		return fmt.Sprintf("%s< %s completed", prefix, name)
	case "approval.required":
		name, _ := payload["toolName"].(string)
		return fmt.Sprintf("%s? approval required: %s", prefix, name)
	case "state.changed":
		state, _ := payload["state"].(string)
		return fmt.Sprintf("%s* state -> %s", prefix, state)
	case "error":
		msg, _ := payload["error"].(string)
		return fmt.Sprintf("%s! error: %s", prefix, msg)
	}

	if strings.HasPrefix(evt.GetType(), "telemetry.") {
		telType, _ := payload["type"].(string)
		if telType == "" {
			telType = evt.GetType()
		}
		telData, _ := payload["data"].(map[string]any)
		if detail := formatTelemetryDetail(telType, telData); detail != "" {
			return prefix + detail
		}
	}

	return prefix + evt.GetType()
}

// attachInputLoop reads stdin lines and forwards each one as a
// SendCommand(type=input) call: `buckley attach`'s programmatic-driving
// path. ":q"/":quit"/":exit" detach locally without contacting the server.
func attachInputLoop(ctx context.Context, client ipcpbconnect.BuckleyIPCClient, opts attachOptions, sessionToken string) error {
	scanner := bufio.NewScanner(os.Stdin)
	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr <- scanner.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-scanErr:
			return err
		case line := <-lines:
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if line == ":q" || line == ":quit" || line == ":exit" {
				return nil
			}
			if sessionToken == "" {
				fmt.Fprintln(os.Stderr, "no session token: cannot drive this session (observation only)")
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			req := connect.NewRequest(&ipcpb.CommandRequest{
				SessionId:    opts.SessionID,
				Type:         "input",
				Content:      line,
				SessionToken: sessionToken,
			})
			attachAuthHeader(req, opts.Token)
			resp, err := client.SendCommand(sendCtx, req)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "send failed: %v\n", err)
				continue
			}
			if resp.Msg.GetStatus() != "accepted" {
				fmt.Fprintf(os.Stderr, "send rejected: %s\n", resp.Msg.GetMessage())
			}
		}
	}
}
