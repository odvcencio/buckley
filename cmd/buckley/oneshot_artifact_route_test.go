package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

const artifactRouteTestAlias = "artifact-route-alias"

func TestOneShotArtifactRoute_NegotiatesSelectedRouteAndRetainsItThroughACP(t *testing.T) {
	artifactJSON := oneShotArtifactRouteJSON(t)
	cases := []struct {
		name             string
		selectedModel    string
		responseMode     string
		wantRequests     int
		wantRouteChecks  int
		wantToollessWire bool
	}{
		{
			name:            "capable selected route captures submitted artifact",
			selectedModel:   "selected-capable",
			responseMode:    "submission",
			wantRequests:    1,
			wantRouteChecks: 2,
		},
		{
			name:            "unknown selected route retains submission contract and accepts direct JSON",
			selectedModel:   "selected-unknown",
			responseMode:    "direct_json",
			wantRequests:    1,
			wantRouteChecks: 2,
		},
		{
			name:             "catalog confirmed tool-less selected route uses direct JSON without tool wire fields",
			selectedModel:    "selected-toolless",
			responseMode:     "direct_json",
			wantRequests:     1,
			wantRouteChecks:  2,
			wantToollessWire: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runOneShotArtifactRouteFixture(t, tc.selectedModel, tc.responseMode, artifactJSON, false)
			if result.exitCode != 0 {
				t.Fatalf("executeOneShotWithLimitsAndOutputSchema exit = %d, stdout=%q", result.exitCode, result.stdout)
			}
			if !strings.Contains(result.stdout, artifactv1.SchemaVersion) {
				t.Fatalf("one-shot output = %q, want rendered artifact JSON", result.stdout)
			}
			if got := len(result.requests); got != tc.wantRequests {
				t.Fatalf("provider requests = %d, want %d", got, tc.wantRequests)
			}
			if got := result.routeChecks; got != tc.wantRouteChecks {
				t.Fatalf("routing hook calls = %d, want %d (one negotiation resolution plus each route-locked dispatch)", got, tc.wantRouteChecks)
			}
			if tc.wantToollessWire {
				assertACPToolOfferWireOmitsTools(t, 1, result.requests[0])
				if artifactRouteWireHasTool(result.requests[0], "submit_artifact") {
					t.Fatalf("catalog-negative wire request unexpectedly advertises submit_artifact: %+v", result.requests[0])
				}
				return
			}
			if !artifactRouteWireHasTool(result.requests[0], "submit_artifact") || result.requests[0].ToolChoice != "auto" {
				t.Fatalf("first wire request = %+v, want forced submit_artifact schema", result.requests[0])
			}
		})
	}
}

func TestOneShotArtifactRoute_DriftFailsBeforeProviderIO(t *testing.T) {
	result := runOneShotArtifactRouteFixture(t, "selected-unknown", "direct_json", oneShotArtifactRouteJSON(t), true)
	if result.exitCode != 1 {
		t.Fatalf("executeOneShotWithLimitsAndOutputSchema exit = %d, want 1", result.exitCode)
	}
	if result.routeChecks != 2 {
		t.Fatalf("routing hook calls = %d, want negotiation resolution plus route-locked dispatch check", result.routeChecks)
	}
	if got := len(result.requests); got != 0 {
		t.Fatalf("provider requests after route drift = %d, want 0", got)
	}
}

func TestRunACPLoopWithLimits_RetainedRouteRejectsInvalidStateBeforeProviderIO(t *testing.T) {
	var providerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			providerRequests.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cases := []struct {
		name  string
		route model.ModelRoute
		want  string
	}{
		{
			name:  "missing provider",
			route: model.ModelRoute{RequestedModel: artifactRouteTestAlias, SelectedModel: "openai_compatible/selected"},
			want:  "requires requested model, selected model, and provider",
		},
		{
			name:  "mismatched logical model",
			route: model.ModelRoute{RequestedModel: "different-model", SelectedModel: "openai_compatible/selected", ProviderID: "openai_compatible"},
			want:  "does not match resolved execution model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := conversation.New("invalid-retained-route")
			conv.AddUserMessage("return a result")
			_, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil, artifactRouteTestAlias, "", "invalid-retained-route", nil, nil, nil, acpLoopLimits{executionRoute: tc.route})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runACPLoopWithLimits error = %v, want %q", err, tc.want)
			}
		})
	}
	if got := providerRequests.Load(); got != 0 {
		t.Fatalf("provider requests = %d, want 0", got)
	}
}

type oneShotArtifactRouteFixtureResult struct {
	exitCode    int
	stdout      string
	requests    []acpToolOfferWireRequest
	routeChecks int
}

func runOneShotArtifactRouteFixture(t *testing.T, selectedModel, responseMode, artifactJSON string, drift bool) oneShotArtifactRouteFixtureResult {
	t.Helper()
	var (
		mu       sync.Mutex
		requests []acpToolOfferWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"selected-capable","supported_parameters":["tools"]},{"id":"selected-toolless","supported_parameters":[]},{"id":"selected-unknown"}]}`)
		case "/chat/completions":
			request := readACPToolOfferWireRequest(t, r)
			mu.Lock()
			requests = append(requests, request)
			round := len(requests)
			mu.Unlock()
			if responseMode == "submission" && round == 1 {
				arguments, err := json.Marshal(map[string]json.RawMessage{"artifact": json.RawMessage(artifactJSON)})
				if err != nil {
					t.Errorf("marshal submit_artifact arguments: %v", err)
					return
				}
				writeOneShotArtifactRouteSSE(t, w, map[string]any{
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call-artifact",
						"type":  "function",
						"function": map[string]any{
							"name":      "submit_artifact",
							"arguments": string(arguments),
						},
					}},
				}, "tool_calls")
				return
			}
			if responseMode == "submission" {
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "artifact submission accepted"}, "stop")
				return
			}
			writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": artifactJSON}, "stop")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"selected-capable", "selected-toolless", "selected-unknown"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{
		"selected-capable":  {"tools"},
		"selected-toolless": {},
	}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var routeChecks atomic.Int32
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision == nil || decision.RequestedModel != artifactRouteTestAlias {
			return decision
		}
		check := routeChecks.Add(1)
		target := selectedModel
		if drift && check > 1 {
			target = "changed-before-dispatch"
		}
		decision.SelectedModel = "openai_compatible/" + target
		return decision
	})

	previousQuiet := quietMode
	quietMode = true
	t.Cleanup(func() { quietMode = previousQuiet })
	result := oneShotArtifactRouteFixtureResult{exitCode: -1}
	result.stdout = captureStdout(t, func() {
		result.exitCode = executeOneShotWithLimitsAndOutputSchema("return the required artifact", cfg, mgr, nil, nil, nil, nil, artifactRouteTestAlias, []string{"read_file"}, false, acpLoopLimits{}, artifactv1.SchemaVersion)
	})
	mu.Lock()
	result.requests = append([]acpToolOfferWireRequest(nil), requests...)
	mu.Unlock()
	result.routeChecks = int(routeChecks.Load())
	return result
}

func oneShotArtifactRouteJSON(t *testing.T) string {
	t.Helper()
	encoded, err := artifactv1.RenderJSON(artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Route negotiation", "Artifact output remained bound to its selected route."))
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	return string(encoded)
}

func artifactRouteWireHasTool(request acpToolOfferWireRequest, name string) bool {
	for _, definition := range request.Tools {
		function, _ := definition["function"].(map[string]any)
		if function["name"] == name {
			return true
		}
	}
	return false
}

func writeOneShotArtifactRouteSSE(t *testing.T, w http.ResponseWriter, delta map[string]any, finishReason string) {
	t.Helper()
	payload := map[string]any{
		"id":    "artifact-route",
		"model": "selected-route",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
