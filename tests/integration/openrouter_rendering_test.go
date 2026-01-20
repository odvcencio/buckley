//go:build integration && manual

// Package integration provides manual integration tests that require real API credentials.
//
// These tests verify that the rendering pipeline works correctly end-to-end.
// Run with: go test -tags="integration,manual" ./tests/integration -v -run TestOpenRouter
//
// Required environment variables:
//   - OPENROUTER_API_KEY: Your OpenRouter API key
package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// skipWithoutOpenRouter skips the test if no OpenRouter API key is set.
func skipWithoutOpenRouter(t *testing.T) string {
	t.Helper()
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY not set, skipping OpenRouter integration test")
	}
	return apiKey
}

// TestOpenRouter_BasicCompletion verifies basic model completion works.
func TestOpenRouter_BasicCompletion(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)

	client := model.NewClient(apiKey, "")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := model.ChatRequest{
		Model: "openai/gpt-4o-mini", // Fast, cheap model for testing
		Messages: []model.Message{
			{Role: "user", Content: "Reply with exactly: PONG"},
		},
		MaxTokens: 10,
	}

	resp, err := client.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice in response")
	}

	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		t.Fatalf("Expected string content, got %T", resp.Choices[0].Message.Content)
	}

	if !strings.Contains(content, "PONG") {
		t.Errorf("Expected response to contain 'PONG', got: %q", content)
	}

	t.Logf("Response: %s", content)
	t.Logf("Tokens: input=%d, output=%d", resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// TestOpenRouter_ToolCall verifies tool calling works correctly.
func TestOpenRouter_ToolCall(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)

	client := model.NewClient(apiKey, "")
	defer client.Close()

	// Define a simple test tool
	testTool := tools.Definition{
		Name:        "test_response",
		Description: "Generate a test response",
		Parameters: tools.ParameterSchema{
			Type: "object",
			Properties: map[string]tools.Property{
				"message": {
					Type:        "string",
					Description: "The test message to return",
				},
			},
			Required: []string{"message"},
		},
	}

	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client:   client,
		Model:    "openai/gpt-4o-mini",
		Provider: "openrouter",
		Pricing:  transparency.ModelPricing{InputPer1M: 0.15, OutputPer1M: 0.6},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, trace, err := invoker.Invoke(
		ctx,
		"You are a test assistant. Always use the test_response tool.",
		"Generate a test response with the message 'hello world'",
		testTool,
		nil,
	)
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}

	if !result.HasToolCall() {
		t.Fatalf("Expected tool call, got text response: %q", result.TextContent)
	}

	if result.ToolCall.Name != "test_response" {
		t.Errorf("Expected tool name 'test_response', got %q", result.ToolCall.Name)
	}

	t.Logf("Tool call: %s", result.ToolCall.Name)
	t.Logf("Arguments: %s", string(result.ToolCall.Arguments))
	t.Logf("Duration: %v", trace.Duration)
	t.Logf("Cost: $%.6f", trace.Cost)
}

// TestOpenRouter_StreamingCompletion verifies streaming works.
func TestOpenRouter_StreamingCompletion(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)

	client := model.NewClient(apiKey, "")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := model.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []model.Message{
			{Role: "user", Content: "Count from 1 to 5, one number per line."},
		},
		MaxTokens: 50,
	}

	chunks, errs := client.ChatCompletionStream(ctx, req)

	var content strings.Builder
	chunkCount := 0

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			chunkCount++
			content.WriteString(chunk.Content)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("Stream error: %v", err)
			}
		}

		if chunks == nil && errs == nil {
			break
		}
	}

	if chunkCount == 0 {
		t.Fatal("Expected at least one chunk")
	}

	result := content.String()
	t.Logf("Received %d chunks", chunkCount)
	t.Logf("Content: %s", result)

	// Verify we got numbers
	for i := 1; i <= 5; i++ {
		if !strings.Contains(result, string(rune('0'+i))) {
			t.Errorf("Expected content to contain %d", i)
		}
	}
}

// TestOpenRouter_ReasoningModel tests models with extended thinking (if available).
func TestOpenRouter_ReasoningModel(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)

	client := model.NewClient(apiKey, "")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Use a reasoning model - these are more expensive but provide thinking
	req := model.ChatRequest{
		Model: "anthropic/claude-3.5-sonnet", // Has reasoning capability
		Messages: []model.Message{
			{Role: "user", Content: "What is 17 * 23? Show your reasoning briefly."},
		},
		MaxTokens: 200,
	}

	resp, err := client.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}

	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		t.Fatalf("Expected string content, got %T", resp.Choices[0].Message.Content)
	}

	// 17 * 23 = 391
	if !strings.Contains(content, "391") {
		t.Errorf("Expected response to contain '391', got: %q", content)
	}

	t.Logf("Response: %s", content)
	if resp.Choices[0].Message.Reasoning != "" {
		t.Logf("Reasoning: %s", resp.Choices[0].Message.Reasoning)
	}
}

// TestOpenRouter_ModelCatalog verifies we can fetch the model catalog.
func TestOpenRouter_ModelCatalog(t *testing.T) {
	apiKey := skipWithoutOpenRouter(t)

	client := model.NewClient(apiKey, "")
	defer client.Close()

	catalog, err := client.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog failed: %v", err)
	}

	if len(catalog.Models) == 0 {
		t.Fatal("Expected at least one model in catalog")
	}

	t.Logf("Catalog contains %d models", len(catalog.Models))

	// Check for some expected models
	expectedModels := []string{"openai/gpt-4o", "anthropic/claude-3.5-sonnet"}
	for _, expected := range expectedModels {
		found := false
		for _, m := range catalog.Models {
			if m.ID == expected {
				found = true
				t.Logf("Found %s: context=%d, input=$%.4f/1M, output=$%.4f/1M",
					m.ID, m.ContextLength, m.Pricing.Prompt*1e6, m.Pricing.Completion*1e6)
				break
			}
		}
		if !found {
			t.Logf("Model %s not found in catalog (may have been renamed)", expected)
		}
	}
}
