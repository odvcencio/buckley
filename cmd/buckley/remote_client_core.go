package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"strings"
	"time"
)

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
		return nil, fmt.Errorf("building %s request for %s: %w", method, p, err)
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
