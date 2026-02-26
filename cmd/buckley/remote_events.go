package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

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
