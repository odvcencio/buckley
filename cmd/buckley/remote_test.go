package main

import (
	"net/http"
	"testing"
	"time"
)

func TestBuildRemoteAction(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		action  string
		planID  string
		feature string
		desc    string
		note    string
		command string
	}{
		{input: "/execute", action: "command", command: "/execute"},
		{input: "plan feature build stuff", action: "plan", feature: "feature", desc: "build stuff"},
		{input: "execute", action: "execute"},
		{input: "execute plan-123", action: "execute", planID: "plan-123"},
		{input: "pause need feedback", action: "pause", note: "need feedback"},
		{input: "resume ok go", action: "resume", note: "ok go"},
	}

	for _, tt := range tests {
		got, err := buildRemoteAction(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("expected error for %q", tt.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("buildRemoteAction(%q) error: %v", tt.input, err)
		}
		if got.Action != tt.action {
			t.Fatalf("buildRemoteAction(%q) action=%s want %s", tt.input, got.Action, tt.action)
		}
		if got.PlanID != tt.planID {
			t.Fatalf("buildRemoteAction(%q) plan=%s want %s", tt.input, got.PlanID, tt.planID)
		}
		if got.FeatureName != tt.feature {
			t.Fatalf("buildRemoteAction(%q) feature=%s want %s", tt.input, got.FeatureName, tt.feature)
		}
		if got.Description != tt.desc {
			t.Fatalf("buildRemoteAction(%q) desc=%s want %s", tt.input, got.Description, tt.desc)
		}
		if got.Command != tt.command {
			t.Fatalf("buildRemoteAction(%q) command=%s want %s", tt.input, got.Command, tt.command)
		}
		if got.Note != tt.note {
			t.Fatalf("buildRemoteAction(%q) note=%s want %s", tt.input, got.Note, tt.note)
		}
	}
}

func TestRemoteClientStreamHTTPClientDoesNotTimeout(t *testing.T) {
	client, err := newRemoteClient(remoteBaseOptions{BaseURL: "https://buckley.example.com/root"})
	if err != nil {
		t.Fatalf("newRemoteClient: %v", err)
	}

	streamClient := client.streamHTTPClient()
	if streamClient == nil {
		t.Fatalf("streamHTTPClient returned nil")
	}
	if streamClient.Timeout != 0 {
		t.Fatalf("streamHTTPClient timeout=%v want 0 (no deadline)", streamClient.Timeout)
	}
	if streamClient.Jar != client.httpClient.Jar {
		t.Fatalf("streamHTTPClient jar mismatch")
	}
	transport, ok := streamClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("streamHTTPClient transport=%T want *http.Transport", streamClient.Transport)
	}
	if transport.ResponseHeaderTimeout != 15*time.Second {
		t.Fatalf("streamHTTPClient response header timeout=%v want %v", transport.ResponseHeaderTimeout, 15*time.Second)
	}
	if transport == client.httpClient.Transport {
		t.Fatalf("streamHTTPClient should clone transport, got same instance")
	}
}
