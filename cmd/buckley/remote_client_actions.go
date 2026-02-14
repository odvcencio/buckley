package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

func (c *remoteClient) issueSessionToken(ctx context.Context, sessionID string) (string, error) {
	payload := strings.NewReader(`{}`)
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("/sessions/%s/tokens", sessionID), payload)
	if err != nil {
		return "", fmt.Errorf("building session token request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending session token request: %w", err)
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
		return "", fmt.Errorf("decoding session token response: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("session token missing in response")
	}
	return result.Token, nil
}

func (c *remoteClient) sendWorkflowAction(ctx context.Context, sessionID string, sessionToken string, action workflowActionRequest) error {
	body, err := json.Marshal(action)
	if err != nil {
		return fmt.Errorf("marshaling workflow action: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("/workflow/%s", sessionID), strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("building workflow action request: %w", err)
	}
	req.Header.Set("X-Buckley-Session-Token", sessionToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending workflow action: %w", err)
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
		return nil, fmt.Errorf("marshaling cli ticket request: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/cli/tickets", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building cli ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending cli ticket request: %w", err)
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
		return nil, fmt.Errorf("decoding cli ticket response: %w", err)
	}
	return &info, nil
}

func (c *remoteClient) pollCliTicket(ctx context.Context, ticket, secret string) (*cliTicketStatus, error) {
	endpoint := fmt.Sprintf("/cli/tickets/%s", ticket)
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building ticket poll request: %w", err)
	}
	req.Header.Set("X-Buckley-CLI-Ticket-Secret", secret)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending ticket poll request: %w", err)
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
		return nil, fmt.Errorf("decoding ticket poll response: %w", err)
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
