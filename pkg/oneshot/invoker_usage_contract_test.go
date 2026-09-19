package oneshot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

const invokerUsageReasoningTwentyTokens = "abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd abcd"

func TestDefaultInvokerUsageProjectionTotalOnlyRetained(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(*DefaultInvoker) (*transparency.Trace, error)
	}{
		{
			name: "text",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeText(context.Background(), "system", "user", nil)
				return trace, err
			},
		},
		{
			name: "invoke",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.Invoke(context.Background(), "system", "user", invokerUsageTool(), nil)
				return trace, err
			},
		},
		{
			name: "stream",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeStream(context.Background(), "system", "user", invokerUsageTool(), nil, nil)
				return trace, err
			},
		},
		{
			name: "tools terminal",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 1)
				return trace, err
			},
		},
		{
			name: "tools response error",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 1)
				if err == nil || !strings.Contains(err.Error(), "partial response") {
					t.Fatalf("InvokeWithTools error = %v, want partial response", err)
				}
				return trace, nil
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invoker := newUsageProjectionInvoker(tt.name, model.Usage{TotalTokens: 42}, "", "")
			trace, err := tt.run(invoker)
			if err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}
			if trace == nil {
				t.Fatal("trace = nil")
			}
			if got := trace.Tokens.Total(); got != 42 {
				t.Fatalf("trace tokens = %+v total=%d, want total-only provider usage retained as 42", trace.Tokens, got)
			}
			if trace.Tokens.Unclassified != 42 || trace.Tokens.ReportedTotal != 42 {
				t.Fatalf("trace tokens = %+v, want total-only usage as unclassified with reported total", trace.Tokens)
			}
			if !trace.CostUnknown {
				t.Fatalf("trace CostUnknown = false, want unknown for total-only usage")
			}
		})
	}
}

func TestDefaultInvokerUsageProjectionProviderReasoningDoesNotDoubleCountCompletion(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(*DefaultInvoker) (*transparency.Trace, error)
	}{
		{
			name: "text",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeText(context.Background(), "system", "user", nil)
				return trace, err
			},
		},
		{
			name: "invoke",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.Invoke(context.Background(), "system", "user", invokerUsageTool(), nil)
				return trace, err
			},
		},
		{
			name: "stream",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeStream(context.Background(), "system", "user", invokerUsageTool(), nil, nil)
				return trace, err
			},
		},
		{
			name: "tools terminal",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 1)
				return trace, err
			},
		},
		{
			name: "tools response error",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				_, trace, err := inv.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 1)
				if err == nil || !strings.Contains(err.Error(), "partial response") {
					t.Fatalf("InvokeWithTools error = %v, want partial response", err)
				}
				return trace, nil
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usage := model.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				CompletionTokenDetails: &model.CompletionTokenDetails{
					ReasoningTokens: 20,
				},
			}
			invoker := newUsageProjectionInvoker(tt.name, usage, invokerUsageReasoningTwentyTokens, "")
			trace, err := tt.run(invoker)
			if err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}
			if trace == nil {
				t.Fatal("trace = nil")
			}
			if got := trace.Tokens.Total(); got != 150 {
				t.Fatalf("trace tokens = %+v total=%d, want provider total 150 without adding reasoning subset/text twice", trace.Tokens, got)
			}
			if trace.Tokens.Reasoning != 0 || trace.Tokens.ReportedReasoning == nil || *trace.Tokens.ReportedReasoning != 20 {
				t.Fatalf("trace reasoning = additive:%d reported:%v, want non-additive reported reasoning 20", trace.Tokens.Reasoning, trace.Tokens.ReportedReasoning)
			}
			if trace.CostUnknown {
				t.Fatalf("trace CostUnknown = true, want ordinary reported reasoning subset priced through output once")
			}
		})
	}
}

func TestDefaultInvokerUsageProjectionReportedTotalMismatchAndCacheEvidence(t *testing.T) {
	for _, tt := range []struct {
		name        string
		usage       model.Usage
		want        transparency.TokenUsage
		wantUnknown bool
	}{
		{
			name:        "reported total above split",
			usage:       model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 170},
			want:        transparency.TokenUsage{Input: 100, Output: 50, Unclassified: 20, ReportedTotal: 170},
			wantUnknown: true,
		},
		{
			name:        "reported total below split",
			usage:       model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 140},
			want:        transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 140, ReportedUsageInconsistent: true},
			wantUnknown: true,
		},
		{
			name: "reported reasoning exceeds completion",
			usage: model.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				CompletionTokenDetails: &model.CompletionTokenDetails{
					ReasoningTokens: 60,
				},
			},
			want:        tokenUsageWithReportedReasoning(transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150, ReportedUsageInconsistent: true}, 60),
			wantUnknown: true,
		},
		{
			name:        "negative provider count",
			usage:       model.Usage{PromptTokens: -1, CompletionTokens: 50, TotalTokens: 49},
			want:        transparency.TokenUsage{Input: -1, Output: 50, ReportedTotal: 49, ReportedUsageInconsistent: true},
			wantUnknown: true,
		},
		{
			name: "cached and write details",
			usage: model.Usage{
				PromptTokens:        100,
				CompletionTokens:    50,
				TotalTokens:         150,
				PromptTokensDetails: &model.PromptTokensDetails{CachedTokens: 30},
				CacheWriteTokens:    12,
			},
			want:        tokenUsageWithReportedCached(transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150, ReportedCacheWrite: 12}, 30),
			wantUnknown: true,
		},
		{
			name:        "estimated usage",
			usage:       model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, Estimated: true},
			want:        transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150, Estimated: true},
			wantUnknown: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invoker := newUsageProjectionInvoker("text", tt.usage, "", "")
			_, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
			if err != nil {
				t.Fatalf("InvokeText: %v", err)
			}
			assertTokenUsageContract(t, trace.Tokens, tt.want)
			if trace.CostUnknown != tt.wantUnknown {
				t.Fatalf("CostUnknown = %v, want %v", trace.CostUnknown, tt.wantUnknown)
			}
		})
	}
}

func TestDefaultInvokerUsageProjectionAggregatesPartialReportedTotalsWithoutFalseInconsistency(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_usage", Type: "function", Function: model.FunctionCall{Name: "usage_tool", Arguments: `{}`},
		}}}, "tool_calls", model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})},
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", Content: "done"}, "stop", model.Usage{PromptTokens: 10, CompletionTokens: 5})},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "usage-model"})

	content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 2)
	if err != nil {
		t.Fatalf("InvokeWithTools: %v", err)
	}
	if content != "done" {
		t.Fatalf("content = %q, want done", content)
	}
	assertTokenUsageContract(t, trace.Tokens, transparency.TokenUsage{Input: 110, Output: 55, ReportedTotal: 150})
	if trace.CostUnknown {
		t.Fatalf("CostUnknown = true, want missing per-response total treated as partial evidence, not inconsistent evidence")
	}
}

func TestDefaultInvokerUsageProjectionAggregatesMaskedInconsistentEvidence(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_usage", Type: "function", Function: model.FunctionCall{Name: "usage_tool", Arguments: `{}`},
		}}}, "tool_calls", model.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      140,
		})},
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", Content: "done"}, "stop", model.Usage{PromptTokens: 5, CompletionTokens: 20, TotalTokens: 25})},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "usage-model"})

	content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 2)
	if err != nil {
		t.Fatalf("InvokeWithTools: %v", err)
	}
	if content != "done" {
		t.Fatalf("content = %q, want done", content)
	}
	want := transparency.TokenUsage{
		Input:                     105,
		Output:                    70,
		ReportedTotal:             165,
		ReportedUsageInconsistent: true,
	}
	assertTokenUsageContract(t, trace.Tokens, want)
	if !trace.CostUnknown {
		t.Fatalf("CostUnknown = false, want invalid per-response usage evidence preserved through aggregation")
	}
}

func TestDefaultInvokerUsageProjectionToolsMissingFinalUsageKeepsCostUnknown(t *testing.T) {
	ledger := transparency.NewCostLedger()
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_usage", Type: "function", Function: model.FunctionCall{Name: "usage_tool", Arguments: `{}`},
		}}}, "tool_calls", model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})},
		{resp: invokerUsageChatResponse(model.Message{Role: "assistant", Content: "done"}, "stop", model.Usage{})},
	}}
	invoker := NewInvoker(InvokerConfig{
		Client:  client,
		Model:   "usage-model",
		Pricing: transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2},
		Ledger:  ledger,
	})

	content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{invokerUsageTool()}, retentionExecutor{}, 2)
	if err != nil {
		t.Fatalf("InvokeWithTools: %v", err)
	}
	if content != "done" {
		t.Fatalf("content = %q, want done", content)
	}
	assertTokenUsageContract(t, trace.Tokens, transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150})
	if !trace.CostUnknown || trace.Cost != 0 {
		t.Fatalf("trace cost = %v unknown=%v, want unknown zero subtotal because final observed response omitted usage", trace.Cost, trace.CostUnknown)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || !entries[0].CostUnknown || entries[0].Cost != 0 {
		t.Fatalf("ledger entries = %+v, want one aggregate unknown entry", entries)
	}
}

func TestDefaultInvokerUsageProjectionNoUsageResponseWithPricedModelIsCostUnknown(t *testing.T) {
	ledger := transparency.NewCostLedger()
	invoker := NewInvoker(InvokerConfig{
		Client: &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: invokerUsageChatResponse(model.Message{Role: "assistant", Content: "public answer"}, "stop", model.Usage{})},
		}},
		Model:   "usage-model",
		Pricing: transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2},
		Ledger:  ledger,
	})

	content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err != nil || content != "public answer" {
		t.Fatalf("InvokeText = %q, %v", content, err)
	}
	assertTokenUsageContract(t, trace.Tokens, transparency.TokenUsage{})
	if !trace.CostUnknown || trace.Cost != 0 {
		t.Fatalf("trace cost = %v unknown=%v, want unknown zero subtotal for observed response with missing usage", trace.Cost, trace.CostUnknown)
	}
	if entries := ledger.Entries(); len(entries) != 1 || !entries[0].CostUnknown || entries[0].Cost != 0 {
		t.Fatalf("ledger entries = %+v, want one unknown zero subtotal entry", entries)
	}
}

func newUsageProjectionInvoker(name string, usage model.Usage, reasoning string, content string) *DefaultInvoker {
	if content == "" {
		content = "public answer"
	}
	msg := model.Message{Role: "assistant", Content: content, Reasoning: reasoning}
	switch name {
	case "stream":
		finish := "stop"
		return NewInvoker(InvokerConfig{Client: retentionStreamClient{chunks: []model.StreamChunk{{
			ID:      "resp-stream",
			Model:   "wire-model",
			Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: content, Reasoning: reasoning}, FinishReason: &finish}},
			Usage:   &usage,
		}}}, Model: "usage-model"})
	case "tools terminal":
		return NewInvoker(InvokerConfig{Client: &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: invokerUsageChatResponse(msg, "stop", usage)},
		}}, Model: "usage-model"})
	case "tools response error":
		return NewInvoker(InvokerConfig{Client: &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: invokerUsageChatResponse(msg, "stop", usage), err: errors.New("provider failed after response")},
		}}, Model: "usage-model"})
	default:
		return NewInvoker(InvokerConfig{Client: &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: invokerUsageChatResponse(msg, "stop", usage)},
		}}, Model: "usage-model"})
	}
}

func invokerUsageChatResponse(msg model.Message, finish string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		ID:           "resp-usage",
		Model:        "wire-model",
		Choices:      []model.Choice{{Message: msg, FinishReason: finish}},
		Usage:        usage,
		UsagePresent: usage != (model.Usage{}),
	}
}

func invokerUsageTool() tools.Definition {
	return tools.Definition{
		Name:        "usage_tool",
		Description: "usage",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}
}

func tokenUsageWithReportedCached(usage transparency.TokenUsage, cached int) transparency.TokenUsage {
	usage.ReportedCachedInput = &cached
	return usage
}

func tokenUsageWithReportedReasoning(usage transparency.TokenUsage, reasoning int) transparency.TokenUsage {
	usage.ReportedReasoning = &reasoning
	return usage
}

func assertTokenUsageContract(t *testing.T, got, want transparency.TokenUsage) {
	t.Helper()
	if got.Input != want.Input ||
		got.Output != want.Output ||
		got.Reasoning != want.Reasoning ||
		got.Unclassified != want.Unclassified ||
		got.CachedInput != want.CachedInput ||
		got.ReportedTotal != want.ReportedTotal ||
		got.ReportedCacheWrite != want.ReportedCacheWrite ||
		got.ReportedUsageInconsistent != want.ReportedUsageInconsistent ||
		got.Estimated != want.Estimated {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	if !optionalIntEqual(got.ReportedReasoning, want.ReportedReasoning) ||
		!optionalIntEqual(got.ReportedCachedInput, want.ReportedCachedInput) {
		t.Fatalf("reported usage pointers = %+v, want %+v", got, want)
	}
}

func optionalIntEqual(got, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}
