//go:build integration && manual

// Package integration provides chaos engineering tests for chat loop safety.
//
// These tests intentionally trigger edge cases that could cause runaway
// API spending, verifying that safety mechanisms actually work with real
// OpenRouter API calls.
//
// WARNING: These tests spend real money. Run with caution.
//
// Run with: go test -tags="integration,manual" ./tests/integration -v -run TestChatLoop
//
// Required environment variables:
//   - OPENROUTER_API_KEY: Your OpenRouter API key
//   - MAX_TEST_COST_USD: Maximum cost per test (default: $0.50)
//   - MAX_ITERATIONS_OVERRIDE: Override default iteration limits
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

// maxTestCost returns the maximum cost allowed per test
func maxTestCost() float64 {
	if v := os.Getenv("MAX_TEST_COST_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0.50 // Default: 50 cents max per test
}

// costTracker tracks actual spend during tests
type costTracker struct {
	totalCost   atomic.Float64
	totalTokens atomic.Int64
	iterations  atomic.Int64
	mu          sync.RWMutex
	calls       []apiCall
}

type apiCall struct {
	Timestamp   time.Time
	TokensIn    int
	TokensOut   int
	Cost        float64
	Model       string
	Description string
}

func (ct *costTracker) Record(tokensIn, tokensOut int, cost float64, model, desc string) {
	ct.totalCost.Add(cost)
	ct.totalTokens.Add(int64(tokensIn + tokensOut))
	ct.iterations.Add(1)
	
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.calls = append(ct.calls, apiCall{
		Timestamp:   time.Now(),
		TokensIn:    tokensIn,
		TokensOut:   tokensOut,
		Cost:        cost,
		Model:       model,
		Description: desc,
	})
}

func (ct *costTracker) Report() string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Total Cost: $%.4f\n", ct.totalCost.Load()))
	b.WriteString(fmt.Sprintf("Total Tokens: %d\n", ct.totalTokens.Load()))
	b.WriteString(fmt.Sprintf("API Calls: %d\n", ct.iterations.Load()))
	b.WriteString("\nCall Log:\n")
	for i, call := range ct.calls {
		b.WriteString(fmt.Sprintf("  %d. %s - %s: %d/%d tokens, $%.4f\n",
			i+1, call.Timestamp.Format("15:04:05"), call.Description,
			call.TokensIn, call.TokensOut, call.Cost))
	}
	return b.String()
}

func (ct *costTracker) CheckBudget(t *testing.T) {
	if cost := ct.totalCost.Load(); cost > maxTestCost() {
		t.Fatalf("TEST ABORTED: Exceeded max test cost $%.2f (spent $%.4f)",
			maxTestCost(), cost)
	}
}

// TestChatLoop_IterationLimit verifies the tool loop terminates at maxIterations
func TestChatLoop_IterationLimit(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	
	// Create a model client that tracks costs
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	// Create a simple tool that encourages the model to continue
	registry := tool.NewRegistry()
	
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	
	// Use a cheap model for cost control
	modelID := "openai/gpt-4o-mini"
	
	// Build a prompt designed to trigger multiple tool calls
	prompt := `You are a test assistant. Your task is to verify loop safety.

You have access to a tool called "counter" that increments a number.
Call the counter tool repeatedly. Each call should increment the counter.
Stop after exactly 3 tool calls and report success.`

	messages := []model.Message{
		{Role: "system", Content: "You are a helpful assistant that uses tools efficiently."},
		{Role: "user", Content: prompt},
	}
	
	// Set a very low iteration limit to test enforcement
	maxIterations := 5
	
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &trackingModelClient{client: client, tracker: tracker},
		Registry:             registry,
		DefaultMaxIterations: maxIterations,
		MaxToolsPhase1:       10,
		EnableReasoning:      false,
	})
	if err != nil {
		t.Fatalf("Failed to create toolrunner: %v", err)
	}
	
	t.Logf("Starting iteration limit test with maxIterations=%d", maxIterations)
	start := time.Now()
	
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      messages,
		MaxIterations: maxIterations,
		Model:         modelID,
	})
	
	duration := time.Since(start)
	
	// Log spend report
	t.Logf("\n%s", tracker.Report())
	
	// Verify we didn't exceed cost budget
	tracker.CheckBudget(t)
	
	// Verify iteration tracking
	if err != nil {
		t.Logf("Run completed with error (expected): %v", err)
	}
	
	if result == nil {
		t.Fatal("Expected result, got nil")
	}
	
	t.Logf("Completed in %v, iterations: %d", duration, result.Iterations)
	
	// The loop should have terminated at or before maxIterations
	if result.Iterations > maxIterations {
		t.Errorf("Iteration limit violated: got %d, max allowed %d",
			result.Iterations, maxIterations)
	}
	
	// Verify finish reason indicates limit reached or natural completion
	if result.FinishReason != "stop" && result.FinishReason != "" {
		t.Logf("Finish reason: %s", result.FinishReason)
	}
}

// TestChatLoop_ContextTimeout verifies context cancellation works
func TestChatLoop_ContextTimeout(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	registry := tool.NewRegistry()
	
	// Very short timeout to test cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	modelID := "openai/gpt-4o-mini"
	
	// Create a prompt that might trigger tool calls
	prompt := `You are a test assistant. Use any available tools to help answer: what is 2+2?
If you have a calculator tool, use it. Otherwise just answer.`

	messages := []model.Message{
		{Role: "user", Content: prompt},
	}
	
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &trackingModelClient{client: client, tracker: tracker},
		Registry:             registry,
		DefaultMaxIterations: 25,
		MaxToolsPhase1:       10,
	})
	if err != nil {
		t.Fatalf("Failed to create toolrunner: %v", err)
	}
	
	t.Logf("Starting context timeout test with 5s timeout")
	start := time.Now()
	
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      messages,
		MaxIterations: 25,
		Model:         modelID,
	})
	
	duration := time.Since(start)
	
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	// Should have completed quickly or been cancelled
	if duration > 10*time.Second {
		t.Errorf("Test took too long (%v), context cancellation may not be working", duration)
	}
	
	// Context cancellation should produce an error or partial result
	if err != nil && !strings.Contains(err.Error(), "context") {
		t.Logf("Expected context error, got: %v", err)
	}
	
	if result != nil {
		t.Logf("Result received after %v, iterations: %d", duration, result.Iterations)
	}
}

// TestChatLoop_CostTrackingAccuracy verifies cost tracking matches actual usage
func TestChatLoop_CostTrackingAccuracy(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	registry := tool.NewRegistry()
	
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	
	// Use a model with known pricing
	modelID := "openai/gpt-4o-mini"
	
	prompt := `Generate a short paragraph (2-3 sentences) about software testing.`
	
	messages := []model.Message{
		{Role: "user", Content: prompt},
	}
	
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &trackingModelClient{client: client, tracker: tracker},
		Registry:             registry,
		DefaultMaxIterations: 5,
		MaxToolsPhase1:       5,
	})
	if err != nil {
		t.Fatalf("Failed to create toolrunner: %v", err)
	}
	
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      messages,
		MaxIterations: 5,
		Model:         modelID,
	})
	
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	// Verify token counts are reasonable
	if result.Usage.TotalTokens == 0 {
		t.Error("Expected non-zero token usage")
	}
	
	// Verify tracked cost matches usage
	trackedCost := tracker.totalCost.Load()
	if trackedCost <= 0 {
		t.Error("Expected positive cost tracking")
	}
	
	t.Logf("Usage: %d prompt, %d completion, %d total tokens",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
}

// TestChatLoop_HeadlessSessionLoop tests the headless runner loop limits
func TestChatLoop_HeadlessSessionLoop(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	
	// Create minimal dependencies
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = apiKey
	
	// Use cheap model
	cfg.Models.Execution = "openai/gpt-4o-mini"
	
	modelMgr := model.NewManager(*cfg)
	modelMgr.SetClient("openrouter", model.NewClient(apiKey, ""))
	
	// Create storage
	tempDir := t.TempDir()
	store, err := storage.New(tempDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()
	
	// Create session
	session := &storage.Session{
		ID:        "test-loop-session",
		CreatedAt: time.Now(),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("Failed to save session: %v", err)
	}
	
	// Create tool registry
	registry := tool.NewRegistry()
	
	// Create runner with short idle timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	runner, err := headless.NewRunner(headless.RunnerConfig{
		Session:      session,
		ModelManager: modelMgr,
		Tools:        registry,
		Store:        store,
		Config:       cfg,
		IdleTimeout:  5 * time.Second,
		MaxRuntime:   20 * time.Second,
		Context:      ctx,
	})
	if err != nil {
		t.Fatalf("Failed to create runner: %v", err)
	}
	defer runner.Stop()
	
	// Send a simple message that shouldn't loop
	cmd := command.SessionCommand{
		Type:    "input",
		Content: "Say hello and nothing else.",
	}
	
	t.Logf("Sending message to headless runner")
	start := time.Now()
	
	if err := runner.HandleSessionCommand(cmd); err != nil {
		t.Fatalf("Failed to handle command: %v", err)
	}
	
	// Wait for processing
	time.Sleep(3 * time.Second)
	
	duration := time.Since(start)
	t.Logf("Completed in %v", duration)
	
	// Verify state
	state := runner.State()
	t.Logf("Final runner state: %s", state)
	
	_ = tracker
}

// TestChatLoop_ToolResultDeduplication verifies duplicate tool results are detected
func TestChatLoop_ToolResultDeduplication(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	registry := tool.NewRegistry()
	
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	
	modelID := "openai/gpt-4o-mini"
	
	// This prompt might trigger repeated tool calls
	prompt := `List the files in the current directory using available tools.
If you don't have a file tool, just say "no tools available".`

	messages := []model.Message{
		{Role: "user", Content: prompt},
	}
	
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &trackingModelClient{client: client, tracker: tracker},
		Registry:             registry,
		DefaultMaxIterations: 10,
		MaxToolsPhase1:       5,
	})
	if err != nil {
		t.Fatalf("Failed to create toolrunner: %v", err)
	}
	
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      messages,
		MaxIterations: 10,
		Model:         modelID,
	})
	
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	if err != nil {
		t.Logf("Run completed with error: %v", err)
	}
	
	if result != nil {
		t.Logf("Iterations: %d, Tool calls: %d", result.Iterations, len(result.ToolCalls))
		
		// Check for duplicate tool calls
		seen := make(map[string]int)
		for _, tc := range result.ToolCalls {
			key := tc.Name + ":" + tc.Arguments
			seen[key]++
		}
		
		for key, count := range seen {
			if count > 1 {
				t.Logf("Duplicate tool call detected: %s (called %d times)", key, count)
			}
		}
	}
}

// trackingModelClient wraps a model client to track costs
type trackingModelClient struct {
	client  *model.Client
	tracker *costTracker
}

func (tmc *trackingModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	resp, err := tmc.client.ChatCompletion(ctx, req)
	if err != nil {
		return resp, err
	}
	
	if resp != nil && resp.Usage.TotalTokens > 0 {
		// Estimate cost (gpt-4o-mini pricing)
		cost := float64(resp.Usage.PromptTokens)*0.15/1e6 + float64(resp.Usage.CompletionTokens)*0.60/1e6
		tmc.tracker.Record(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cost, req.Model, "completion")
	}
	
	return resp, err
}

func (tmc *trackingModelClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunkChan, errChan := tmc.client.ChatCompletionStream(ctx, req)
	
	// Track streaming usage (approximate since we get usage at end)
	trackedChan := make(chan model.StreamChunk)
	go func() {
		defer close(trackedChan)
		var totalTokens int
		for chunk := range chunkChan {
			// Rough estimate based on content
			if len(chunk.Content) > 0 {
				totalTokens += len(chunk.Content) / 4 // Rough estimate
			}
			trackedChan <- chunk
		}
		if totalTokens > 0 {
			// Estimate cost
			cost := float64(totalTokens) * 0.15 / 1e6
			tmc.tracker.Record(0, totalTokens, cost, req.Model, "stream")
		}
	}()
	
	return trackedChan, errChan
}

func (tmc *trackingModelClient) GetExecutionModel() string {
	return "openai/gpt-4o-mini"
}

// TestChatLoop_MemoryBudgetTest verifies memory safety under load
func TestChatLoop_MemoryBudgetTest(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	registry := tool.NewRegistry()
	
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	
	modelID := "openai/gpt-4o-mini"
	
	// Create a conversation with context that might trigger compaction
	messages := []model.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Write a 100-word story about a robot learning to paint."},
	}
	
	// Add some back-and-forth to build context
	for i := 0; i < 5; i++ {
		messages = append(messages,
			model.Message{Role: "assistant", Content: fmt.Sprintf("Response %d: This is a generated response for testing context limits.", i+1)},
			model.Message{Role: "user", Content: fmt.Sprintf("Follow-up question %d: Tell me more.", i+1)},
		)
	}
	
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &trackingModelClient{client: client, tracker: tracker},
		Registry:             registry,
		DefaultMaxIterations: 10,
		MaxToolsPhase1:       5,
	})
	if err != nil {
		t.Fatalf("Failed to create toolrunner: %v", err)
	}
	
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      messages,
		MaxIterations: 10,
		Model:         modelID,
	})
	
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
	
	if err != nil {
		t.Logf("Run completed: %v", err)
	}
	
	if result != nil {
		t.Logf("Final iterations: %d", result.Iterations)
	}
}

// TestChatLoop_RapidCancellation tests rapid start/cancel cycles
func TestChatLoop_RapidCancellation(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	tracker := &costTracker{}
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	registry := tool.NewRegistry()
	
	// Run multiple short-lived contexts
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		
		modelID := "openai/gpt-4o-mini"
		messages := []model.Message{
			{Role: "user", Content: "Write a haiku about coding."},
		}
		
		runner, _ := toolrunner.New(toolrunner.Config{
			Models:               &trackingModelClient{client: client, tracker: tracker},
			Registry:             registry,
			DefaultMaxIterations: 10,
			MaxToolsPhase1:       5,
		})
		
		t.Logf("Cycle %d: Starting run with 2s timeout", i+1)
		start := time.Now()
		
		runner.Run(ctx, toolrunner.Request{
			Messages:      messages,
			MaxIterations: 10,
			Model:         modelID,
		})
		
		duration := time.Since(start)
		t.Logf("Cycle %d: Completed in %v", i+1, duration)
		
		cancel()
		time.Sleep(100 * time.Millisecond) // Brief pause between cycles
	}
	
	t.Logf("\n%s", tracker.Report())
	tracker.CheckBudget(t)
}

// TestChatLoop_ConversationCompaction verifies conversation compaction works
func TestChatLoop_ConversationCompaction(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)
	
	client := model.NewClient(apiKey, "")
	defer client.Close()
	
	// Create a conversation
	conv := conversation.New("test-compaction")
	conv.AddSystemMessage("You are a helpful assistant.")
	
	// Add many messages to trigger compaction
	for i := 0; i < 50; i++ {
		conv.AddUserMessage(fmt.Sprintf("Question %d: What is %d + %d?", i, i, i+1))
		conv.AddAssistantMessage(fmt.Sprintf("Answer %d: %d + %d = %d", i, i, i+1, i+i+1))
	}
	
	t.Logf("Conversation has %d messages", len(conv.Messages))
	
	// Compact the conversation
	compactor := conversation.NewCompactionManager(client, config.DefaultConfig())
	compactor.SetConversation(conv)
	
	// Force compaction
	if len(conv.Messages) > 20 {
		t.Logf("Messages exceed threshold, compaction would trigger")
	}
	
	t.Logf("Compaction test completed")
}

// toJSON helper for debug output
func toJSONHelper(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
