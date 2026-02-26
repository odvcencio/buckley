package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (c *remoteClient) listSessions(ctx context.Context) ([]remoteSessionSummary, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/sessions?limit=50", nil)
	if err != nil {
		return nil, fmt.Errorf("building list sessions request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending list sessions request: %w", err)
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
		return nil, fmt.Errorf("decoding sessions response: %w", err)
	}
	return result.Sessions, nil
}

func (c *remoteClient) getSessionDetail(ctx context.Context, sessionID string) (*remoteSessionDetail, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s", sessionID), nil)
	if err != nil {
		return nil, fmt.Errorf("building session detail request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending session detail request: %w", err)
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
		return nil, fmt.Errorf("decoding session detail response: %w", err)
	}
	return &detail, nil
}

func (c *remoteClient) fetchPlanLog(ctx context.Context, planID, kind string) ([]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/plans/%s/logs/%s?limit=50", planID, kind), nil)
	if err != nil {
		return nil, fmt.Errorf("building plan log request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending plan log request: %w", err)
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
		return nil, fmt.Errorf("decoding plan log response: %w", err)
	}
	return payload.Entries, nil
}

func (c *remoteClient) listAPITokens(ctx context.Context) ([]remoteAPIToken, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/config/api-tokens", nil)
	if err != nil {
		return nil, fmt.Errorf("building token list request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending token list request: %w", err)
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
		return nil, fmt.Errorf("decoding token list response: %w", err)
	}
	return payload.Tokens, nil
}

func (c *remoteClient) createAPIToken(ctx context.Context, name, owner, scope string) (string, remoteAPIToken, error) {
	body := map[string]string{"name": name, "owner": owner, "scope": scope}
	buf, _ := json.Marshal(body)
	req, err := c.newRequest(ctx, http.MethodPost, "/config/api-tokens", strings.NewReader(string(buf)))
	if err != nil {
		return "", remoteAPIToken{}, fmt.Errorf("building token create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", remoteAPIToken{}, fmt.Errorf("sending token create request: %w", err)
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
		return "", remoteAPIToken{}, fmt.Errorf("decoding token create response: %w", err)
	}
	return payload.Token, payload.Record, nil
}

func (c *remoteClient) revokeAPIToken(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, fmt.Sprintf("/config/api-tokens/%s", id), nil)
	if err != nil {
		return fmt.Errorf("building token revoke request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending token revoke request: %w", err)
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
