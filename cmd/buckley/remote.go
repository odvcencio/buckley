package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/term"
	"nhooyr.io/websocket"

	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func runRemoteCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley remote <attach|sessions|tokens|login|console> [flags]")
	}

	switch args[0] {
	case "attach":
		return runRemoteAttach(args[1:])
	case "sessions":
		return runRemoteSessions(args[1:])
	case "tokens":
		return runRemoteTokens(args[1:])
	case "login":
		return runRemoteLogin(args[1:])
	case "console":
		return runRemoteConsole(args[1:])
	default:
		return fmt.Errorf("unknown remote subcommand: %s", args[0])
	}
}

type remoteBaseOptions struct {
	BaseURL      string
	IPCAuthToken string
	BasicUser    string
	BasicPass    string
	InsecureTLS  bool
}

type remoteAttachOptions struct {
	remoteBaseOptions
	SessionID string
}

func runRemoteAttach(args []string) error {
	opts, err := parseRemoteAttachFlags(args)
	if err != nil {
		return err
	}

	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nInterrupted – closing remote session")
		cancel()
	}()

	sessionToken, err := client.issueSessionToken(ctx, opts.SessionID)
	if err != nil {
		return err
	}

	var activePlan string
	if detail, err := client.getSessionDetail(ctx, opts.SessionID); err == nil {
		fmt.Printf("Session %s status: %s (branch: %s)\n", detail.Session.ID, detail.Session.Status, detail.Session.GitBranch)
		if detail.Plan.ID != "" {
			activePlan = detail.Plan.ID
			fmt.Printf("Plan %s – %s (%d tasks)\n", detail.Plan.ID, detail.Plan.FeatureName, len(detail.Plan.Tasks))
		}
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	eventCh := make(chan remoteEvent, 64)
	go func() {
		defer close(eventCh)
		if err := client.streamEvents(streamCtx, opts.SessionID, eventCh); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "remote stream ended: %v\n", err)
			cancel()
		}
	}()

	fmt.Printf("📡 Attached to session %s at %s\n", opts.SessionID, opts.BaseURL)
	fmt.Println("Type slash commands (/plan, /execute, /workflow pause ...) or plain text. Type :q to exit.")

	go func() {
		for evt := range eventCh {
			if evt.SessionID != "" && evt.SessionID != opts.SessionID {
				continue
			}
			printRemoteEvent(evt)
		}
	}()

	return remoteInputLoop(ctx, client, opts.SessionID, sessionToken, activePlan)
}

func runRemoteSessions(args []string) error {
	base, err := parseRemoteBaseFlags("sessions", args)
	if err != nil {
		return err
	}
	client, err := newRemoteClient(base)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessions, err := client.listSessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("No active sessions")
		return nil
	}
	fmt.Printf("%-16s %-12s %-24s %s\n", "SESSION", "STATUS", "LAST ACTIVE", "BRANCH")
	for _, sess := range sessions {
		last := sess.LastActive
		if last != "" {
			if ts, err := time.Parse(time.RFC3339Nano, last); err == nil {
				last = ts.Local().Format(time.RFC822)
			}
		}
		fmt.Printf("%-16s %-12s %-24s %s\n", sess.ID, sess.Status, last, sess.GitBranch)
	}
	return nil
}

func runRemoteTokens(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley remote tokens <list|create|revoke> [flags]")
	}
	sub := args[0]
	switch sub {
	case "list":
		base, err := parseRemoteBaseFlags("tokens list", args[1:])
		if err != nil {
			return err
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tokens, err := client.listAPITokens(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-8s %-12s %-10s %-12s %s\n", "ID", "NAME", "SCOPE", "LAST USED", "STATUS")
		for _, tok := range tokens {
			last := tok.LastUsed
			if last != "" {
				if ts, err := time.Parse(time.RFC3339Nano, last); err == nil {
					last = ts.Local().Format(time.RFC822)
				}
			} else {
				last = "—"
			}
			status := "active"
			if tok.Revoked {
				status = "revoked"
			}
			fmt.Printf("%.8s %-12s %-10s %-12s %s\n", tok.ID, tok.Name, tok.Scope, last, status)
		}
	case "create":
		fs := flag.NewFlagSet("remote tokens create", flag.ContinueOnError)
		var base remoteBaseOptions
		registerRemoteBaseFlags(fs, &base)
		name := fs.String("name", "", "Token label")
		owner := fs.String("owner", "", "Owner / contact")
		scope := fs.String("scope", "member", "Scope (operator|member|viewer)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(base.BaseURL) == "" {
			return fmt.Errorf("--url is required")
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		token, record, err := client.createAPIToken(ctx, *name, *owner, *scope)
		if err != nil {
			return err
		}
		fmt.Println("Token created. Store this secret safely:")
		fmt.Printf("  %s\n", token)
		fmt.Printf("Scope: %s  Prefix: %s\n", record.Scope, record.Prefix)
	case "revoke":
		fs := flag.NewFlagSet("remote tokens revoke", flag.ContinueOnError)
		var base remoteBaseOptions
		registerRemoteBaseFlags(fs, &base)
		tokenID := fs.String("id", "", "Token ID to revoke")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(base.BaseURL) == "" {
			return fmt.Errorf("--url is required")
		}
		client, err := newRemoteClient(base)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		if strings.TrimSpace(*tokenID) == "" {
			return fmt.Errorf("--id required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.revokeAPIToken(ctx, *tokenID); err != nil {
			return err
		}
		fmt.Println("Token revoked.")
	default:
		return fmt.Errorf("unknown tokens subcommand %s", sub)
	}
	return nil
}

type remoteLoginOptions struct {
	remoteBaseOptions
	Label     string
	NoBrowser bool
	Timeout   time.Duration
}

type remoteConsoleOptions struct {
	remoteBaseOptions
	SessionID string
	Command   string
}

func runRemoteLogin(args []string) error {
	opts, err := parseRemoteLoginFlags(args)
	if err != nil {
		return err
	}
	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	ticket, err := client.createCliTicket(ctx, opts.Label)
	if err != nil {
		return err
	}
	fmt.Printf("📟 Ticket %s created. Secret stored locally.\n", ticket.Ticket)
	fmt.Printf("Open this URL in a logged-in browser session to approve:\n  %s\n", ticket.LoginURL)
	if !opts.NoBrowser {
		_ = openBrowser(ticket.LoginURL)
	}
	fmt.Println("Waiting for approval… Press Ctrl+C to cancel.")
	if err := client.waitForCliApproval(ctx, ticket.Ticket, ticket.Secret, 3*time.Second); err != nil {
		return err
	}
	if err := client.persistCookies(); err != nil {
		return err
	}
	fmt.Println("✅ Buckley CLI is now authenticated via your browser session.")
	return nil
}

func runRemoteConsole(args []string) error {
	opts, err := parseRemoteConsoleFlags(args)
	if err != nil {
		return err
	}
	client, err := newRemoteClient(opts.remoteBaseOptions)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionToken, err := client.issueSessionToken(ctx, opts.SessionID)
	if err != nil {
		return err
	}
	fmt.Printf("Opening remote console for session %s. Press Ctrl+C to exit.\n", opts.SessionID)
	return client.openRemotePTY(ctx, opts.SessionID, sessionToken, opts.Command)
}

func parseRemoteAttachFlags(args []string) (remoteAttachOptions, error) {
	fs := flag.NewFlagSet("remote attach", flag.ContinueOnError)
	opts := remoteAttachOptions{}
	registerRemoteBaseFlags(fs, &opts.remoteBaseOptions)
	fs.StringVar(&opts.SessionID, "session", "", "Session ID to attach to")

	if err := fs.Parse(args); err != nil {
		return remoteAttachOptions{}, err
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteAttachOptions{}, err
	}

	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteAttachOptions{}, fmt.Errorf("--url is required")
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		return remoteAttachOptions{}, fmt.Errorf("--session is required")
	}

	return opts, nil
}

func parseRemoteBaseFlags(cmd string, args []string) (remoteBaseOptions, error) {
	fs := flag.NewFlagSet("remote "+cmd, flag.ContinueOnError)
	var opts remoteBaseOptions
	registerRemoteBaseFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return remoteBaseOptions{}, err
	}
	if err := validateRemoteBaseOptions(opts); err != nil {
		return remoteBaseOptions{}, err
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteBaseOptions{}, fmt.Errorf("--url is required")
	}
	return opts, nil
}

func registerRemoteBaseFlags(fs *flag.FlagSet, opts *remoteBaseOptions) {
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "IPC bearer token")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
}

func validateRemoteBaseOptions(opts remoteBaseOptions) error {
	user := strings.TrimSpace(opts.BasicUser)
	pass := strings.TrimSpace(opts.BasicPass)
	if user != "" && pass == "" {
		return fmt.Errorf("--basic-auth-user requires --basic-auth-pass (or BUCKLEY_BASIC_AUTH_PASSWORD)")
	}
	if user == "" && pass != "" {
		return fmt.Errorf("--basic-auth-pass requires --basic-auth-user (or BUCKLEY_BASIC_AUTH_USER)")
	}
	return nil
}

func parseRemoteLoginFlags(args []string) (remoteLoginOptions, error) {
	fs := flag.NewFlagSet("remote login", flag.ContinueOnError)
	var opts remoteLoginOptions
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	fs.StringVar(&opts.Label, "label", strings.TrimSpace(os.Getenv("USER")), "Label shown to the reviewer")
	fs.BoolVar(&opts.NoBrowser, "no-browser", false, "Do not open the approval URL automatically")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "Time to wait for browser approval")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "API token (optional; browser auth preferred)")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
	if err := fs.Parse(args); err != nil {
		return remoteLoginOptions{}, err
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteLoginOptions{}, err
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteLoginOptions{}, fmt.Errorf("--url is required")
	}
	return opts, nil
}

func parseRemoteConsoleFlags(args []string) (remoteConsoleOptions, error) {
	fs := flag.NewFlagSet("remote console", flag.ContinueOnError)
	var opts remoteConsoleOptions
	fs.StringVar(&opts.BaseURL, "url", "", "Base URL of the remote Buckley instance")
	fs.StringVar(&opts.SessionID, "session", "", "Session ID to open a console for")
	fs.StringVar(&opts.Command, "cmd", "", "Optional command to run instead of the default shell")
	defaultToken := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	fs.StringVar(&opts.IPCAuthToken, "token", defaultToken, "IPC bearer token")
	defaultUser := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_USER"))
	defaultPass := strings.TrimSpace(os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"))
	fs.StringVar(&opts.BasicUser, "basic-auth-user", defaultUser, "HTTP basic auth username")
	fs.StringVar(&opts.BasicPass, "basic-auth-pass", defaultPass, "HTTP basic auth password")
	fs.BoolVar(&opts.InsecureTLS, "insecure-skip-verify", false, "Skip TLS verification (development only)")
	if err := fs.Parse(args); err != nil {
		return remoteConsoleOptions{}, err
	}
	if err := validateRemoteBaseOptions(opts.remoteBaseOptions); err != nil {
		return remoteConsoleOptions{}, err
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return remoteConsoleOptions{}, fmt.Errorf("--url is required")
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		return remoteConsoleOptions{}, fmt.Errorf("--session is required")
	}
	return opts, nil
}

type remoteClient struct {
	baseURL     *url.URL
	httpClient  *http.Client
	cookieJar   http.CookieJar
	authStore   *remoteAuthStore
	ipcToken    string
	basicHeader string
	insecureTLS bool
}

func (c *remoteClient) Close() error {
	return c.persistCookies()
}

func (c *remoteClient) streamHTTPClient() *http.Client {
	if c == nil || c.httpClient == nil {
		return &http.Client{}
	}
	clone := *c.httpClient
	clone.Timeout = 0
	if transport, ok := clone.Transport.(*http.Transport); ok && transport != nil {
		transportClone := transport.Clone()
		transportClone.ResponseHeaderTimeout = 15 * time.Second
		clone.Transport = transportClone
	}
	return &clone
}

func (c *remoteClient) persistCookies() error {
	if c == nil || c.authStore == nil || c.cookieJar == nil {
		return nil
	}
	return c.authStore.Save(c.baseURL, c.cookieJar.Cookies(c.baseURL))
}

type workflowActionRequest struct {
	Action      string `json:"action"`
	FeatureName string `json:"feature"`
	Description string `json:"description"`
	PlanID      string `json:"planId"`
	Note        string `json:"note"`
	Command     string `json:"command"`
}

type cliTicketInfo struct {
	Ticket   string `json:"ticket"`
	Secret   string `json:"secret"`
	LoginURL string `json:"loginUrl"`
	Expires  string `json:"expiresAt"`
}

type cliTicketStatus struct {
	Status    string         `json:"status"`
	Label     string         `json:"label"`
	ExpiresAt string         `json:"expiresAt"`
	Principal map[string]any `json:"principal"`
}

func newRemoteClient(opts remoteBaseOptions) (*remoteClient, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	// url.Parse treats scheme-less hosts as paths; prefix https:// for convenience.
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid remote url: %w", err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	c := &remoteClient{
		baseURL:     parsed,
		ipcToken:    strings.TrimSpace(opts.IPCAuthToken),
		insecureTLS: opts.InsecureTLS,
	}
	if opts.BasicUser != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(opts.BasicUser + ":" + opts.BasicPass))
		c.basicHeader = "Basic " + auth
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.InsecureTLS {
		transport.TLSClientConfig.InsecureSkipVerify = true // #nosec G402
	}
	jar, _ := cookiejar.New(nil)
	store := newRemoteAuthStore()
	if store != nil && jar != nil {
		if cookies := store.CookiesFor(parsed); len(cookies) > 0 {
			jar.SetCookies(parsed, cookies)
		}
	}
	c.cookieJar = jar
	c.authStore = store
	c.httpClient = &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		Jar:       jar,
	}
	return c, nil
}

func (c *remoteClient) apiURL(p string) string {
	u := *c.baseURL
	rel, err := url.Parse(p)
	if err == nil {
		apiPath := path.Join(strings.TrimSuffix(u.Path, "/"), "/api", rel.Path)
		u.Path = apiPath
		q := u.Query()
		for key, vals := range rel.Query() {
			for _, val := range vals {
				q.Add(key, val)
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	// Fallback: treat p as path-only.
	apiPath := path.Join(strings.TrimSuffix(u.Path, "/"), "/api", p)
	u.Path = apiPath
	return u.String()
}

func (c *remoteClient) attachCookies(header http.Header, rawURL string) {
	if c.cookieJar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	cookies := c.cookieJar.Cookies(u)
	if len(cookies) == 0 {
		return
	}
	var parts []string
	for _, ck := range cookies {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	header.Set("Cookie", strings.Join(parts, "; "))
}

func (c *remoteClient) newRequest(ctx context.Context, method, p string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL(p), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.basicHeader != "" {
		req.Header.Set("Authorization", c.basicHeader)
	} else if c.ipcToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.ipcToken)
	}
	return req, nil
}

const maxRemoteErrorBodyBytes int64 = 64 << 10
const remoteAuthHint = "authenticate with --token, basic auth, or `buckley remote login`"

type ipcErrorEnvelope struct {
	Error       string   `json:"error"`
	Status      int      `json:"status"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message"`
	Details     string   `json:"details,omitempty"`
	Remediation []string `json:"remediation,omitempty"`
	Retryable   bool     `json:"retryable,omitempty"`
	Timestamp   string   `json:"timestamp"`
}

func readBodyLimited(r io.Reader, maxBytes int64) []byte {
	if r == nil || maxBytes <= 0 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(r, maxBytes))
	return data
}

func formatIPCErrorBody(data []byte) string {
	if len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	var payload ipcErrorEnvelope
	if err := json.Unmarshal(data, &payload); err == nil {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = strings.TrimSpace(payload.Error)
		}
		if msg != "" {
			out := msg
			if code := strings.TrimSpace(payload.Code); code != "" {
				out = fmt.Sprintf("%s (%s)", out, code)
			}
			hint := ""
			if len(payload.Remediation) > 0 {
				hint = strings.TrimSpace(payload.Remediation[0])
			}
			if hint != "" {
				out = fmt.Sprintf("%s — %s", out, hint)
			}
			if payload.Retryable {
				out += " (retryable)"
			}
			return out
		}
	}
	return strings.TrimSpace(string(data))
}

func (c *remoteClient) issueSessionToken(ctx context.Context, sessionID string) (string, error) {
	payload := strings.NewReader(`{}`)
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("/sessions/%s/tokens", sessionID), payload)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return "", fmt.Errorf("session token request failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return "", fmt.Errorf("session token request failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return "", fmt.Errorf("session token request failed (%s): %s", resp.Status, body)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("session token missing in response")
	}
	return result.Token, nil
}

func (c *remoteClient) sendWorkflowAction(ctx context.Context, sessionID string, sessionToken string, action workflowActionRequest) error {
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}

	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("/workflow/%s", sessionID), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("X-Buckley-Session-Token", sessionToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return fmt.Errorf("workflow action failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return fmt.Errorf("workflow action failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return fmt.Errorf("workflow action failed (%s): %s", resp.Status, body)
	}
	return nil
}

func (c *remoteClient) wsURL(sessionID string) string {
	u := *c.baseURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path.Join(strings.TrimSuffix(c.baseURL.Path, "/"), "/ws")
	q := u.Query()
	if strings.TrimSpace(sessionID) != "" {
		q.Set("sessionId", strings.TrimSpace(sessionID))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *remoteClient) ptyURL(sessionID string, rows, cols int, cmd string) string {
	u := *c.baseURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path.Join(strings.TrimSuffix(c.baseURL.Path, "/"), "/ws/pty")
	q := u.Query()
	if sessionID != "" {
		q.Set("sessionId", sessionID)
	}
	if rows > 0 {
		q.Set("rows", fmt.Sprintf("%d", rows))
	}
	if cols > 0 {
		q.Set("cols", fmt.Sprintf("%d", cols))
	}
	if strings.TrimSpace(cmd) != "" {
		q.Set("cmd", cmd)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *remoteClient) createCliTicket(ctx context.Context, label string) (*cliTicketInfo, error) {
	body, err := json.Marshal(map[string]string{"label": label})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/cli/tickets", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("create ticket failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("create ticket failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("create ticket failed (%s): %s", resp.Status, body)
	}
	var info cliTicketInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *remoteClient) pollCliTicket(ctx context.Context, ticket, secret string) (*cliTicketStatus, error) {
	endpoint := fmt.Sprintf("/cli/tickets/%s", ticket)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Buckley-CLI-Ticket-Secret", secret)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("ticket poll failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("ticket poll failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("ticket poll failed (%s): %s", resp.Status, body)
	}
	var status cliTicketStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *remoteClient) waitForCliApproval(ctx context.Context, ticket, secret string, interval time.Duration) error {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, err := c.pollCliTicket(ctx, ticket, secret)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(os.Stderr, "ticket poll failed (retrying): %v\n", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		switch strings.ToLower(status.Status) {
		case "approved":
			return nil
		case "expired":
			return fmt.Errorf("ticket expired before approval")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

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
		return err
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
			return err
		default:
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
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
		return err
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
			return err
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
			return err
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
		return err
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

func (c *remoteClient) listSessions(ctx context.Context) ([]remoteSessionSummary, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/sessions?limit=50", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("list sessions failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("list sessions failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("list sessions failed (%s): %s", resp.Status, body)
	}
	var result struct {
		Sessions []remoteSessionSummary `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Sessions, nil
}

func (c *remoteClient) getSessionDetail(ctx context.Context, sessionID string) (*remoteSessionDetail, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s", sessionID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("session detail failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("session detail failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("session detail failed (%s): %s", resp.Status, body)
	}
	var detail remoteSessionDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (c *remoteClient) fetchPlanLog(ctx context.Context, planID, kind string) ([]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/plans/%s/logs/%s?limit=50", planID, kind), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("log fetch failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("log fetch failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("log fetch failed (%s): %s", resp.Status, body)
	}
	var payload struct {
		Entries []string `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Entries, nil
}

func (c *remoteClient) listAPITokens(ctx context.Context) ([]remoteAPIToken, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/config/api-tokens", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return nil, fmt.Errorf("token list failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return nil, fmt.Errorf("token list failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return nil, fmt.Errorf("token list failed (%s): %s", resp.Status, body)
	}
	var payload struct {
		Tokens []remoteAPIToken `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Tokens, nil
}

func (c *remoteClient) createAPIToken(ctx context.Context, name, owner, scope string) (string, remoteAPIToken, error) {
	body := map[string]string{"name": name, "owner": owner, "scope": scope}
	buf, _ := json.Marshal(body)
	req, err := c.newRequest(ctx, http.MethodPost, "/config/api-tokens", strings.NewReader(string(buf)))
	if err != nil {
		return "", remoteAPIToken{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", remoteAPIToken{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return "", remoteAPIToken{}, fmt.Errorf("token create failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return "", remoteAPIToken{}, fmt.Errorf("token create failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return "", remoteAPIToken{}, fmt.Errorf("token create failed (%s): %s", resp.Status, body)
	}
	var payload struct {
		Token  string         `json:"token"`
		Record remoteAPIToken `json:"record"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", remoteAPIToken{}, err
	}
	return payload.Token, payload.Record, nil
}

func (c *remoteClient) revokeAPIToken(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, fmt.Sprintf("/config/api-tokens/%s", id), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data := readBodyLimited(resp.Body, maxRemoteErrorBodyBytes)
		body := formatIPCErrorBody(data)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if body != "" {
				return fmt.Errorf("token revoke failed (%s): %s (%s)", resp.Status, body, remoteAuthHint)
			}
			return fmt.Errorf("token revoke failed (%s): %s", resp.Status, remoteAuthHint)
		}
		if body == "" {
			body = resp.Status
		}
		return fmt.Errorf("token revoke failed (%s): %s", resp.Status, body)
	}
	return nil
}

func remoteInputLoop(ctx context.Context, client *remoteClient, sessionID string, sessionToken string, planID string) error {
	for {
		line, readErr := readLineWithContext(ctx, "remote> ")
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) {
				fmt.Println("\nClosing remote session.")
				return nil
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == ":q" || line == ":quit" || line == ":exit" {
			fmt.Println("Closing remote session.")
			return nil
		}
		if strings.HasPrefix(line, ":logs") {
			if planID == "" {
				fmt.Println("No plan ID associated with this session yet.")
				continue
			}
			parts := strings.Fields(line)
			kind := "builder"
			if len(parts) > 1 {
				kind = parts[1]
			}
			logCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			entries, err := client.fetchPlanLog(logCtx, planID, kind)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "log fetch failed: %v\n", err)
				continue
			}
			if len(entries) == 0 {
				fmt.Printf("No %s log entries\n", kind)
				continue
			}
			for _, entry := range entries {
				fmt.Printf("[%s] %s\n", kind, entry)
			}
			continue
		}
		if line == ":interrupt" {
			action := workflowActionRequest{Action: "pause", Note: "Paused via remote CLI"}
			if err := client.sendWorkflowAction(ctx, sessionID, sessionToken, action); err != nil {
				fmt.Fprintf(os.Stderr, "pause failed: %v\n", err)
			}
			continue
		}
		action, err := buildRemoteAction(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid input: %v\n", err)
			continue
		}
		if err := client.sendWorkflowAction(ctx, sessionID, sessionToken, action); err != nil {
			fmt.Fprintf(os.Stderr, "action failed: %v\n", err)
		}
	}
}

func readLineWithContext(ctx context.Context, prompt string) (string, error) {
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			lineCh <- scanner.Text()
			return
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- io.EOF
	}()

	if strings.TrimSpace(prompt) != "" {
		fmt.Print(prompt)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case line := <-lineCh:
		return line, nil
	}
}

func openBrowser(target string) error {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func buildRemoteAction(input string) (workflowActionRequest, error) {
	input = strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(input, "/"):
		return workflowActionRequest{Action: "command", Command: input}, nil
	case strings.HasPrefix(strings.ToLower(input), "plan "):
		parts := strings.SplitN(input, " ", 3)
		if len(parts) < 3 {
			return workflowActionRequest{}, fmt.Errorf("usage: plan <feature> <description>")
		}
		return workflowActionRequest{
			Action:      "plan",
			FeatureName: parts[1],
			Description: strings.TrimSpace(parts[2]),
		}, nil
	case strings.HasPrefix(strings.ToLower(input), "execute"):
		parts := strings.Fields(input)
		req := workflowActionRequest{Action: "execute"}
		if len(parts) > 1 {
			req.PlanID = parts[1]
		}
		return req, nil
	case strings.HasPrefix(strings.ToLower(input), "pause"):
		note := strings.TrimSpace(input[len("pause"):])
		return workflowActionRequest{Action: "pause", Note: note}, nil
	case strings.HasPrefix(strings.ToLower(input), "resume"):
		note := strings.TrimSpace(input[len("resume"):])
		return workflowActionRequest{Action: "resume", Note: note}, nil
	default:
		return workflowActionRequest{Action: "command", Command: input}, nil
	}
}

type remoteEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

type remoteSessionSummary struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	LastActive string `json:"lastActive"`
	GitBranch  string `json:"gitBranch"`
}

type remoteSessionDetail struct {
	Session struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		LastActive string `json:"lastActive"`
		GitBranch  string `json:"gitBranch"`
	} `json:"session"`
	Plan struct {
		ID          string           `json:"id"`
		FeatureName string           `json:"featureName"`
		Description string           `json:"description"`
		Tasks       []map[string]any `json:"tasks"`
	} `json:"plan"`
}

type remoteAPIToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Scope     string `json:"scope"`
	Prefix    string `json:"prefix"`
	CreatedAt string `json:"createdAt"`
	LastUsed  string `json:"lastUsedAt"`
	Revoked   bool   `json:"revoked"`
}

func printRemoteEvent(evt remoteEvent) {
	switch {
	case evt.Type == "message.created":
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(evt.Payload, &msg); err == nil {
			fmt.Printf("[%s] %s\n", strings.ToUpper(msg.Role), strings.TrimSpace(msg.Content))
			return
		}
	case strings.HasPrefix(evt.Type, "telemetry."):
		var tel struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(evt.Payload, &tel); err == nil {
			if detail := formatTelemetryDetail(tel.Type, tel.Data); detail != "" {
				fmt.Printf("📊 %s\n", detail)
				return
			}
		}
	case evt.Type == "session.updated":
		fmt.Printf("ℹ️ Session updated %s\n", evt.Timestamp.Format(time.RFC822))
		return
	}
	var compact map[string]any
	if err := json.Unmarshal(evt.Payload, &compact); err == nil && len(compact) > 0 {
		data, _ := json.Marshal(compact)
		fmt.Printf("%s %s\n", evt.Type, string(data))
		return
	}
	fmt.Printf("%s\n", evt.Type)
}

func formatTelemetryDetail(eventType string, data map[string]any) string {
	switch eventType {
	case string(telemetry.EventTaskStarted):
		return fmt.Sprintf("Task %v started", data["taskId"])
	case string(telemetry.EventTaskCompleted):
		return fmt.Sprintf("Task %v completed", data["taskId"])
	case string(telemetry.EventTaskFailed):
		return fmt.Sprintf("Task %v failed: %v", data["taskId"], data["error"])
	case string(telemetry.EventBuilderStarted):
		return fmt.Sprintf("Builder started %v", data["phase"])
	case string(telemetry.EventBuilderCompleted):
		return "Builder completed"
	case string(telemetry.EventBuilderFailed):
		return fmt.Sprintf("Builder failed: %v", data["error"])
	case string(telemetry.EventCostUpdated):
		return fmt.Sprintf("Cost updated: $%.2f", data["total"])
	case string(telemetry.EventTokenUsageUpdated):
		return fmt.Sprintf("Token usage: %v tokens", data["totalTokens"])
	default:
		return ""
	}
}
