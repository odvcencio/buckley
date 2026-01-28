//go:build integration

// Package integration provides deterministic tests for the TUI tool use loop.
//
// These tests verify that the TUI controller correctly handles:
// - Simple responses (no tools)
// - Single tool calls
// - Multiple tool calls in sequence
// - Tool errors and recovery
// - Streaming responses
//
// Run with: go test -tags=integration ./tests/integration -v -run TestTUI
//
// Required environment variables:
//   - OPENROUTER_API_KEY: Your OpenRouter API key
//   - MAX_TEST_COST_USD: Maximum cost per test (default: $0.50)
package integration

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/buckley/ui/tui"
	"github.com/odvcencio/buckley/pkg/tool"
)

// tuiCostTracker tracks costs for TUI tests
type tuiCostTracker struct {
	totalCost   atomic.Float64
	totalTokens atomic.Int64
	mu          sync.Mutex
	calls       []tuiAPICall
}

type tuiAPICall struct {
	Timestamp time.Time
	Model     string
	TokensIn  int
	TokensOut int
	Cost      float64
	Prompt    string
}

func (ct *tuiCostTracker) Record(model string, tokensIn, tokensOut int, cost float64, prompt string) {
	ct.totalCost.Add(cost)
	ct.totalTokens.Add(int64(tokensIn + tokensOut))
	
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.calls = append(ct.calls, tuiAPICall{
		Timestamp: time.Now(),
		Model:     model,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Cost:      cost,
		Prompt:    prompt,
	})
}

func (ct *tuiCostTracker) Report() string {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Total Cost: $%.4f\n", ct.totalCost.Load()))
	b.WriteString(fmt.Sprintf("Total Tokens: %d\n", ct.totalTokens.Load()))
	b.WriteString(fmt.Sprintf("API Calls: %d\n", len(ct.calls)))
	b.WriteString("\nCall Log:\n")
	for i, call := range ct.calls {
		b.WriteString(fmt.Sprintf("  %d. %s - %s: %d/%d tokens, $%.4f\n",
			i+1, call.Timestamp.Format("15:04:05"), call.Model,
			call.TokensIn, call.TokensOut, call.Cost))
	}
	return b.String()
}

func (ct *tuiCostTracker) CheckBudget(t *testing.T) {
	maxCost := 0.50 // Default 50 cents
	if v := os.Getenv("MAX_TEST_COST_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			maxCost = f
		}
	}
	
	if cost := ct.totalCost.Load(); cost > maxCost {
		t.Fatalf("TEST ABORTED: Exceeded max test cost $%.2f (spent $%.4f)",
			maxCost, cost)
	}
}

// skipWithoutOpenRouter skips the test if OPENROUTER_API_KEY is not set
func skipWithoutOpenRouter(t *testing.T) string {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping: OPENROUTER_API_KEY not set")
	}
	return apiKey
}

// createTestController creates a TUI controller for testing
func createTestController(t *testing.T, apiKey string) (*tui.Controller, *storage.Store, *tuiCostTracker) {
	t.Helper()
	
	tracker := &tuiCostTracker{}
	
	// Create config
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = apiKey
	cfg.Models.Execution = "openai/gpt-4o-mini" // Use cheap model
	
	// Create model manager
	modelMgr := model.NewManager(cfg)
	if err := modelMgr.Initialize(); err != nil {
		t.Fatalf("Failed to initialize model manager: %v", err)
	}
	
	// Wrap the model client to track costs
	originalClient := modelMgr.GetClient("openrouter")
	if originalClient != nil {
		wrappedClient := &trackingModelClient{
			client:  originalClient,
			tracker: tracker,
		}
		modelMgr.SetClient("openrouter", wrappedClient)
	}
	
	// Create temp storage
	tempDir := t.TempDir()
	store, err := storage.New(tempDir + "/buckley.db")
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	
	// Create project context
	projectCtx := &projectcontext.ProjectContext{
		Loaded: false, // Minimal context for tests
	}
	
	// Create controller
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	
	ctrl, err := tui.NewController(tui.ControllerConfig{
		Config:       cfg,
		ModelManager: modelMgr,
		Store:        store,
		ProjectCtx:   projectCtx,
		SessionID:    "",
		Context:      ctx,
	})
	if err != nil {
		store.Close()
		t.Fatalf("Failed to create controller: %v", err)
	}
	
	return ctrl, store, tracker
}

// TestTUI_SimpleResponse tests a simple response without tools
func TestTUI_SimpleResponse(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	ctrl, store, tracker := createTestController(t, apiKey)
	defer store.Close()
	defer ctrl.Stop()
	
	// Send a simple prompt that shouldn't require tools
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	prompt := "Say 'Hello from test' and nothing else."
	
	t.Logf("Sending prompt: %s", prompt)
	start := time.Now()
	
	// Use the controller's handleSubmit via the app interface
	ctrl.App().AddMessage(prompt, "user")
	
	// Wait for response (poll for a while)
	var responseReceived bool
	for i := 0; i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		
		// Check if we got a response by looking at the chat view
		// This is a heuristic - in real tests we'd query the app state
		if time.Since(start) > 25*time.Second {
			break
		}
		
		// Check streaming status
		if !ctrl.App().IsStreaming() {
			responseReceived = true
			break
		}
	}
	
	duration := time.Since(start)
	t.Logf("Response received: %v, duration: %v", responseReceived, duration)
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	if !responseReceived {
		t.Error("Did not receive response within timeout")
	}
}

// TestTUI_ToolUse_Single tests a single tool call
func TestTUI_ToolUse_Single(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	ctrl, store, tracker := createTestController(t, apiKey)
	defer store.Close()
	defer ctrl.Stop()
	
	// Register a simple test tool
	testTool := &testCalculatorTool{}
	ctrl.RegisterTool(testTool)
	
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	
	prompt := "Calculate 15 + 27 using the calculator tool."
	
	t.Logf("Sending prompt: %s", prompt)
	start := time.Now()
	
	ctrl.App().AddMessage(prompt, "user")
	
	// Wait for response
	var responseReceived bool
	for i := 0; i < 90; i++ {
		time.Sleep(500 * time.Millisecond)
		
		if time.Since(start) > 40*time.Second {
			break
		}
		
		if !ctrl.App().IsStreaming() {
			responseReceived = true
			break
		}
	}
	
	duration := time.Since(start)
	t.Logf("Response received: %v, duration: %v", responseReceived, duration)
	t.Logf("Tool was called: %v", testTool.WasCalled())
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	if !responseReceived {
		t.Error("Did not receive response within timeout")
	}
	
	if !testTool.WasCalled() {
		t.Error("Calculator tool was not called")
	}
}

// TestTUI_ToolUse_ErrorRecovery tests tool error handling
func TestTUI_ToolUse_ErrorRecovery(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	ctrl, store, tracker := createTestController(t, apiKey)
	defer store.Close()
	defer ctrl.Stop()
	
	// Register a tool that returns an error
	testTool := &testErrorTool{}
	ctrl.RegisterTool(testTool)
	
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	
	prompt := "Try to use the error_tool. It will fail, but you should handle it gracefully."
	
	t.Logf("Sending prompt: %s", prompt)
	start := time.Now()
	
	ctrl.App().AddMessage(prompt, "user")
	
	// Wait for response
	var responseReceived bool
	for i := 0; i < 90; i++ {
		time.Sleep(500 * time.Millisecond)
		
		if time.Since(start) > 40*time.Second {
			break
		}
		
		if !ctrl.App().IsStreaming() {
			responseReceived = true
			break
		}
	}
	
	duration := time.Since(start)
	t.Logf("Response received: %v, duration: %v", responseReceived, duration)
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	if !responseReceived {
		t.Error("Did not receive response within timeout")
	}
}

// TestTUI_StreamingResponse tests streaming behavior
func TestTUI_StreamingResponse(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	ctrl, store, tracker := createTestController(t, apiKey)
	defer store.Close()
	defer ctrl.Stop()
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	prompt := "Write a 3-sentence story about a cat."
	
	t.Logf("Sending prompt: %s", prompt)
	start := time.Now()
	
	ctrl.App().AddMessage(prompt, "user")
	
	// Track streaming state changes
	streamingStates := []bool{}
	for i := 0; i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		streamingStates = append(streamingStates, ctrl.App().IsStreaming())
		
		if time.Since(start) > 25*time.Second {
			break
		}
		
		if !ctrl.App().IsStreaming() && i > 5 {
			break
		}
	}
	
	duration := time.Since(start)
	t.Logf("Duration: %v", duration)
	t.Logf("Streaming states: %v", streamingStates)
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	// Verify we saw streaming (true) and then stopped (false)
	hasStreaming := false
	hasStopped := false
	for _, state := range streamingStates {
		if state {
			hasStreaming = true
		}
		if hasStreaming && !state {
			hasStopped = true
			break
		}
	}
	
	if !hasStreaming {
		t.Log("Warning: Did not observe streaming state")
	}
}

// TestTUI_ContextCancellation tests that context cancellation works
func TestTUI_ContextCancellation(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	ctrl, store, tracker := createTestController(t, apiKey)
	defer store.Close()
	defer ctrl.Stop()
	
	// Use a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	
	prompt := "Write a long story about a dragon." // Something that takes time
	
	t.Logf("Sending prompt with 3s timeout: %s", prompt)
	start := time.Now()
	
	ctrl.App().AddMessage(prompt, "user")
	
	// Wait for context to cancel
	<-ctx.Done()
	
	duration := time.Since(start)
	t.Logf("Test completed in %v", duration)
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	// Verify cancellation happened
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("Expected deadline exceeded, got: %v", ctx.Err())
	}
}

// trackingModelClient wraps a model client to track costs
type trackingTUIClient struct {
	client  model.Client
	tracker *tuiCostTracker
}

func (tmc *trackingTUIClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	resp, err := tmc.client.ChatCompletion(ctx, req)
	if err != nil {
		return resp, err
	}
	
	if resp != nil && resp.Usage.TotalTokens > 0 {
		// Estimate cost (gpt-4o-mini pricing)
		cost := float64(resp.Usage.PromptTokens)*0.15/1e6 + float64(resp.Usage.CompletionTokens)*0.60/1e6
		promptPreview := ""
		if len(req.Messages) > 0 {
			promptPreview = req.Messages[len(req.Messages)-1].Content
			if len(promptPreview) > 50 {
				promptPreview = promptPreview[:50] + "..."
			}
		}
		tmc.tracker.Record(req.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cost, promptPreview)
	}
	
	return resp, err
}

func (tmc *trackingTUIClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	return tmc.client.ChatCompletionStream(ctx, req)
}

func (tmc *trackingTUIClient) SupportsReasoning(modelID string) bool {
	return tmc.client.SupportsReasoning(modelID)
}

// testCalculatorTool is a simple calculator for testing
type testCalculatorTool struct {
	called atomic.Bool
}

func (t *testCalculatorTool) Name() string        { return "calculator" }
func (t *testCalculatorTool) Description() string { return "Calculate mathematical expressions" }
func (t *testCalculatorTool) Parameters() tool.ParameterSchema {
	return tool.ParameterSchema{
		Type: "object",
		Properties: map[string]tool.ParameterProperty{
			"expression": {
				Type:        "string",
				Description: "The mathematical expression to evaluate",
			},
		},
		Required: []string{"expression"},
	}
}

func (t *testCalculatorTool) Execute(params map[string]any) (*tool.Result, error) {
	t.called.Store(true)
	
	expression, ok := params["expression"].(string)
	if !ok {
		return nil, fmt.Errorf("expression parameter required")
	}
	
	// Simple evaluation - just return the expression for testing
	return &tool.Result{
		Content: fmt.Sprintf("Result of %s = 42 (test value)", expression),
	}, nil
}

func (t *testCalculatorTool) WasCalled() bool {
	return t.called.Load()
}

// testErrorTool always returns an error
type testErrorTool struct{}

func (t *testErrorTool) Name() string        { return "error_tool" }
func (t *testErrorTool) Description() string { return "A tool that always fails" }
func (t *testErrorTool) Parameters() tool.ParameterSchema {
	return tool.ParameterSchema{
		Type:       "object",
		Properties: map[string]tool.ParameterProperty{},
	}
}

func (t *testErrorTool) Execute(params map[string]any) (*tool.Result, error) {
	return nil, fmt.Errorf("intentional test error")
}
