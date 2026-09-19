package oneshot

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestDefaultInvokerStreamExplicitZeroUsageSuppressesReasoningEstimate(t *testing.T) {
	finish := "stop"
	client := retentionStreamClient{chunks: []model.StreamChunk{{
		ID:    "resp-explicit-zero-stream",
		Model: "wire-stream",
		Choices: []model.StreamChoice{{
			Delta:        model.MessageDelta{Content: "public", Reasoning: invokerUsageReasoningTwentyTokens},
			FinishReason: &finish,
		}},
		Usage: &model.Usage{},
	}}}
	invoker := NewInvoker(InvokerConfig{
		Client:  client,
		Model:   "usage-model",
		Pricing: transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2},
	})

	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", invokerUsageTool(), nil, nil)
	if err != nil {
		t.Fatalf("InvokeStream: %v", err)
	}
	if result == nil || result.TextContent != "public" {
		t.Fatalf("result = %+v, want public stream text", result)
	}
	want := transparency.TokenUsage{UsageEvidencePresent: true}
	assertRichUsage(t, trace.Tokens, want)
	if trace.Tokens.Reasoning != 0 || trace.Tokens.Estimated {
		t.Fatalf("trace tokens = %+v, want no estimated reasoning when stream usage object is explicitly zero", trace.Tokens)
	}
	if trace.CostUnknown {
		t.Fatalf("CostUnknown = true, want known zero for explicit zero usage with known pricing")
	}
}

func TestDefaultInvokerMissingUsageSuppressesReasoningEstimate(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{{
		resp: &model.ChatResponse{
			ID:      "resp-missing-usage",
			Model:   "wire-model",
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "public", Reasoning: invokerUsageReasoningTwentyTokens}, FinishReason: "stop"}},
		},
		err: errors.New("provider failed after response"),
	}}}
	invoker := NewInvoker(InvokerConfig{
		Client:  client,
		Model:   "usage-model",
		Pricing: transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2},
	})

	content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err == nil {
		t.Fatalf("InvokeText error = nil, content=%q trace=%+v", content, trace)
	}
	want := transparency.TokenUsage{UsageEvidenceMissing: true}
	assertRichUsage(t, trace.Tokens, want)
	if trace.Tokens.Reasoning != 0 || trace.Tokens.Estimated {
		t.Fatalf("trace tokens = %+v, want no reasoning estimate when usage evidence is explicitly missing", trace.Tokens)
	}
	if !trace.CostUnknown {
		t.Fatalf("CostUnknown = false, want unknown for priced response with missing usage")
	}
}
