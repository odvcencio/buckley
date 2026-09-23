package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// testConfigWithDecisions builds a DefaultConfig with an OpenRouter API
// key set and decisions.gates.reasoning_choice configured for
// reasoningChoiceGate's tests.
func testConfigWithDecisions(t *testing.T, endpoint string, enabled, gateEnabled bool) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Decisions.Enabled = enabled
	cfg.Decisions.Endpoint = endpoint
	cfg.Decisions.Timeout = 5 * time.Second
	cfg.Decisions.Gates.ReasoningChoice.Enabled = gateEnabled
	return cfg
}

func TestLowerReasoningEffort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"xhigh", "high"},
		{"high", "medium"},
		{"medium", "low"},
		{"low", "minimal"},
		{"minimal", "minimal"},
		{"MEDIUM", "low"}, // case-insensitive match
		{"", ""},          // unrecognized: unchanged
		{"bogus", "bogus"},
	}
	for _, tc := range cases {
		if got := lowerReasoningEffort(tc.in); got != tc.want {
			t.Errorf("lowerReasoningEffort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func newDecisionsGateServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testPRContext(diff string) *commands.PRContext {
	return &commands.PRContext{
		PR:   &commands.PRInfo{ChangedFiles: 2, Additions: 5, Deletions: 1},
		Diff: diff,
	}
}

func TestApplyReviewDepthGate_DisabledIsNoOp(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: false, apiKey: "k", model: "typesafe/jev-1.13", endpoint: srv.URL, timeout: time.Second,
		},
	}
	out := applyReviewDepthGate(context.Background(), opts, testPRContext("diff"))
	if out.reasoningEffort != "high" || calls != 0 {
		t.Fatalf("expected no-op when disabled: effort=%s calls=%d", out.reasoningEffort, calls)
	}
}

func TestApplyReviewDepthGate_ForceFullDepthIsNoOp(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: true, forceFullDepth: true, apiKey: "k", model: "typesafe/jev-1.13",
			endpoint: srv.URL, timeout: time.Second,
		},
	}
	out := applyReviewDepthGate(context.Background(), opts, testPRContext("diff"))
	if out.reasoningEffort != "high" || calls != 0 {
		t.Fatalf("expected --full-depth to skip the gate entirely: effort=%s calls=%d", out.reasoningEffort, calls)
	}
}

func TestApplyReviewDepthGate_LowersReasoningAtThreshold(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	srv := newDecisionsGateServer(t, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"depth":{"type":"score","score":0.1,"legend":{"0":"trivial","1":"light","2":"standard","3":"deep"},"probabilities":{"0":0.9,"1":0.05,"2":0.03,"3":0.02}}},
		"usage":{"input_tokens":100,"output_tokens":10,"cost":0.000005}
	}`)
	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: true, apiKey: "k", model: "typesafe/jev-1.13", endpoint: srv.URL,
			timeout: 5 * time.Second, trivialProbability: 0.85, logPath: logPath,
		},
	}
	out := applyReviewDepthGate(context.Background(), opts, testPRContext("diff --git a/x b/x"))
	if out.reasoningEffort != "medium" {
		t.Fatalf("expected reasoning lowered from high to medium, got %s", out.reasoningEffort)
	}
	records := readJSONLRecords(t, logPath)
	if len(records) != 1 || records[0]["gate"] != "review_depth" || records[0]["score"] != "trivial" {
		t.Fatalf("unexpected log records: %+v", records)
	}
}

func TestApplyReviewDepthGate_BelowThresholdLeavesReasoningUnchanged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	srv := newDecisionsGateServer(t, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"depth":{"type":"score","score":1.5,"legend":{"0":"trivial","1":"light","2":"standard","3":"deep"},"probabilities":{"0":0.4,"1":0.3,"2":0.2,"3":0.1}}},
		"usage":{"input_tokens":100,"output_tokens":10,"cost":0.000005}
	}`)
	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: true, apiKey: "k", model: "typesafe/jev-1.13", endpoint: srv.URL,
			timeout: 5 * time.Second, trivialProbability: 0.85, logPath: logPath,
		},
	}
	out := applyReviewDepthGate(context.Background(), opts, testPRContext("diff --git a/x b/x"))
	if out.reasoningEffort != "high" {
		t.Fatalf("expected reasoning unchanged below threshold, got %s", out.reasoningEffort)
	}
	records := readJSONLRecords(t, logPath)
	if len(records) != 1 || records[0]["reasoning_after"] != "high" {
		t.Fatalf("unexpected log records: %+v", records)
	}
}

func TestApplyReviewDepthGate_TimeoutFallsBackToDefault(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: true, apiKey: "k", model: "typesafe/jev-1.13", endpoint: srv.URL,
			timeout: 20 * time.Millisecond, trivialProbability: 0.85, logPath: logPath,
		},
	}
	start := time.Now()
	out := applyReviewDepthGate(context.Background(), opts, testPRContext("diff"))
	if time.Since(start) > 2*time.Second {
		t.Fatalf("gate did not respect its own timeout")
	}
	if out.reasoningEffort != "high" {
		t.Fatalf("expected a timeout to fall back to the non-gated default, got %s", out.reasoningEffort)
	}
	records := readJSONLRecords(t, logPath)
	if len(records) != 1 || records[0]["error"] == nil {
		t.Fatalf("expected the timeout to be logged as an error: %+v", records)
	}
}

func TestApplyReviewDepthGate_TruncatesLargeDiffs(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"model":"typesafe/jev-1.13-20260917",
			"answers":{"depth":{"type":"score","score":3,"legend":{"0":"trivial","1":"light","2":"standard","3":"deep"},"probabilities":{"0":0.1,"1":0.1,"2":0.1,"3":0.7}}},
			"usage":{"input_tokens":100,"output_tokens":10,"cost":0.000005}
		}`)
	}))
	defer srv.Close()
	opts := automatedReviewOptions{
		reasoningEffort: "high",
		decisionsGate: reviewDepthGateConfig{
			enabled: true, apiKey: "k", model: "typesafe/jev-1.13", endpoint: srv.URL,
			timeout: 5 * time.Second, trivialProbability: 0.85,
		},
	}
	bigDiff := make([]byte, maxReviewDepthGateDiffBytes*3)
	for i := range bigDiff {
		bigDiff[i] = 'x'
	}
	applyReviewDepthGate(context.Background(), opts, testPRContext(string(bigDiff)))
	var wire map[string]any
	if err := json.Unmarshal(capturedBody, &wire); err != nil {
		t.Fatalf("invalid request body: %v", err)
	}
	state, _ := wire["state"].(string)
	if len(state) > maxReviewDepthGateDiffBytes+500 {
		t.Fatalf("expected the diff sent to the gate to be bounded, got %d bytes", len(state))
	}
}

func TestReasoningChoiceGate_DisabledReturnsFalse(t *testing.T) {
	cfg := testConfigWithDecisions(t, "", false, false)
	if _, ok := reasoningChoiceGate(context.Background(), cfg); ok {
		t.Fatal("expected the gate to be off by default")
	}
}

func TestReasoningChoiceGate_ReturnsChosenEffort(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	srv := newDecisionsGateServer(t, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"effort":{"type":"choice","choice":"high","probabilities":{"low":0.1,"medium":0.2,"high":0.7}}},
		"usage":{"input_tokens":50,"output_tokens":5,"cost":0.000002}
	}`)
	cfg := testConfigWithDecisions(t, srv.URL, true, true)
	cfg.Decisions.LogPath = logPath
	effort, ok := reasoningChoiceGate(context.Background(), cfg)
	if !ok || effort != "high" {
		t.Fatalf("expected the gate to choose high, got %q ok=%v", effort, ok)
	}
	records := readJSONLRecords(t, logPath)
	if len(records) != 1 || records[0]["choice"] != "high" {
		t.Fatalf("unexpected log records: %+v", records)
	}
}

func TestReasoningChoiceGate_InvalidChoiceReturnsFalse(t *testing.T) {
	srv := newDecisionsGateServer(t, `{
		"model":"typesafe/jev-1.13-20260917",
		"answers":{"effort":{"type":"choice","choice":"extreme","probabilities":{"low":0.1,"medium":0.2,"high":0.7}}},
		"usage":{"input_tokens":50,"output_tokens":5,"cost":0.000002}
	}`)
	cfg := testConfigWithDecisions(t, srv.URL, true, true)
	if _, ok := reasoningChoiceGate(context.Background(), cfg); ok {
		t.Fatal("expected an unofferred choice from the mock to be rejected by the client and fall back")
	}
}

func TestReasoningChoiceGate_RequiresAPIKey(t *testing.T) {
	cfg := testConfigWithDecisions(t, "http://unused.invalid", true, true)
	cfg.Providers.OpenRouter.APIKey = ""
	if _, ok := reasoningChoiceGate(context.Background(), cfg); ok {
		t.Fatal("expected a missing API key to disable the gate")
	}
}

func TestLogDecisionGate_AppendsJSONL(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nested", "decisions.jsonl")
	logDecisionGate(logPath, "review_depth", map[string]any{"score": "trivial"})
	logDecisionGate(logPath, "reasoning_choice", map[string]any{"choice": "low"})
	records := readJSONLRecords(t, logPath)
	if len(records) != 2 {
		t.Fatalf("expected two records, got %d: %+v", len(records), records)
	}
	if records[0]["gate"] != "review_depth" || records[1]["gate"] != "reasoning_choice" {
		t.Fatalf("unexpected gate labels: %+v", records)
	}
}

// readJSONLRecords reads every JSON line in path as a map, failing the
// test if the file or any line is invalid.
func readJSONLRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var records []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return records
}
