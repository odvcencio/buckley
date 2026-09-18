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

func TestDefaultInvokerPricingKnownFreeAndUnknownTextLedger(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pricing transparency.ModelPricing
		unknown bool
		want    float64
		wantUnk bool
	}{
		{name: "known paid", pricing: transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15}, want: 18},
		{name: "known free"},
		{name: "unknown ignores nonzero fallback", pricing: transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15}, unknown: true, wantUnk: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger := transparency.NewCostLedger()
			client := &retentionInvokerClient{responses: []retentionInvokerResponse{
				{resp: retentionChatResponse("resp-text", nil, model.Message{Role: "assistant", Content: "hello"}, "stop", 1_000_000, 1_000_000)},
			}}
			invoker := NewInvoker(InvokerConfig{Client: client, Model: "priced-model", Pricing: tt.pricing, PricingUnknown: tt.unknown, Ledger: ledger})

			content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
			if err != nil || content != "hello" {
				t.Fatalf("InvokeText = %q, %v", content, err)
			}
			requireCostProjection(t, trace, ledger, transparency.TokenUsage{Input: 1_000_000, Output: 1_000_000}, tt.want, tt.wantUnk, 1)
		})
	}
}

func TestDefaultInvokerPricingUnknownPartialErrorsRecordLedger(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(*DefaultInvoker) (*transparency.Trace, error)
	}{
		{
			name: "invoke",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				result, trace, err := inv.Invoke(context.Background(), "system", "user", retentionToolDef(), nil)
				if result == nil || result.TextContent != "public partial" {
					t.Fatalf("Invoke result = %+v, want public partial", result)
				}
				return trace, err
			},
		},
		{
			name: "invoke text",
			run: func(inv *DefaultInvoker) (*transparency.Trace, error) {
				content, trace, err := inv.InvokeText(context.Background(), "system", "user", nil)
				if content != "public partial" {
					t.Fatalf("InvokeText content = %q, want public partial", content)
				}
				return trace, err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger := transparency.NewCostLedger()
			client := &retentionInvokerClient{responses: []retentionInvokerResponse{
				{resp: retentionChatResponse("resp-partial", nil, model.Message{Role: "assistant", Content: "public partial"}, "stop", 10, 20), err: errors.New("transport failed after body")},
			}}
			invoker := NewInvoker(InvokerConfig{
				Client:         client,
				Model:          "unknown-model",
				Pricing:        transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15},
				PricingUnknown: true,
				Ledger:         ledger,
			})

			trace, err := tt.run(invoker)
			if err == nil || !strings.Contains(err.Error(), "partial response") {
				t.Fatalf("%s error = %v, want partial response", tt.name, err)
			}
			requireCostProjection(t, trace, ledger, transparency.TokenUsage{Input: 10, Output: 20}, 0, true, 1)
		})
	}
}

func TestDefaultInvokerPricingUnknownNoResponseErrorBuildsTraceWithoutLedger(t *testing.T) {
	ledger := transparency.NewCostLedger()
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{{err: errors.New("network down")}}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

	content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err == nil || content != "" {
		t.Fatalf("InvokeText = %q, %v, want no-response error", content, err)
	}
	if trace == nil || !trace.CostUnknown || trace.Cost != 0 {
		t.Fatalf("trace = %+v, want unknown no-response trace", trace)
	}
	if ledger.InvocationCount() != 0 {
		t.Fatalf("ledger entries = %d, want 0 without observed response", ledger.InvocationCount())
	}
}

func TestDefaultInvokerPricingUnknownStreamTraceAndLedger(t *testing.T) {
	for _, tt := range []struct {
		name       string
		chunks     []model.StreamChunk
		streamErr  error
		wantErrSub string
		wantText   string
		wantTokens transparency.TokenUsage
		wantLedger int
	}{
		{name: "success", chunks: pricingStreamChunks("stop", "stream public", 4, 5), wantText: "stream public", wantTokens: transparency.TokenUsage{Input: 4, Output: 5}, wantLedger: 1},
		{name: "truncated", chunks: pricingStreamChunks("length", "stream public", 4, 5), wantErrSub: "truncated", wantText: "stream public", wantTokens: transparency.TokenUsage{Input: 4, Output: 5}, wantLedger: 1},
		{name: "terminal error", chunks: pricingStreamChunks("", "stream public", 4, 5), streamErr: errors.New("stream broke"), wantErrSub: "partial response", wantText: "stream public", wantTokens: transparency.TokenUsage{Input: 4, Output: 5}, wantLedger: 1},
		{name: "empty frame before error", chunks: []model.StreamChunk{{ID: "resp-empty"}}, streamErr: errors.New("stream broke"), wantErrSub: "model request failed", wantTokens: transparency.TokenUsage{}, wantLedger: 1},
		{name: "error before frame", streamErr: errors.New("stream broke"), wantErrSub: "model request failed", wantTokens: transparency.TokenUsage{}, wantLedger: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger := transparency.NewCostLedger()
			invoker := NewInvoker(InvokerConfig{
				Client:         retentionStreamClient{chunks: tt.chunks, err: tt.streamErr},
				Model:          "unknown-model",
				Pricing:        transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15},
				PricingUnknown: true,
				Ledger:         ledger,
			})

			result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", retentionToolDef(), nil, nil)
			if tt.wantErrSub == "" && err != nil {
				t.Fatalf("InvokeStream error = %v", err)
			}
			if tt.wantErrSub != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErrSub)) {
				t.Fatalf("InvokeStream error = %v, want %q", err, tt.wantErrSub)
			}
			if tt.wantText != "" {
				if result == nil || result.TextContent != tt.wantText {
					t.Fatalf("result = %+v, want public stream text %q", result, tt.wantText)
				}
			}
			requireCostProjection(t, trace, ledger, tt.wantTokens, 0, true, tt.wantLedger)
		})
	}
}

func pricingStreamChunks(finishReason, content string, input, output int) []model.StreamChunk {
	var finish *string
	if finishReason != "" {
		finish = &finishReason
	}
	return []model.StreamChunk{{
		ID:      "resp-stream",
		Model:   "wire-model",
		Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: content}, FinishReason: finish}},
		Usage:   &model.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output},
	}}
}

func TestDefaultInvokerPricingUnknownInvokeWithRetryRecordsEachAttemptOnce(t *testing.T) {
	ledger := transparency.NewCostLedger()
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-first", nil, model.Message{Role: "assistant", Content: "plain text"}, "stop", 10, 1)},
		{resp: retentionChatResponse("resp-second", nil, model.Message{Role: "assistant", Content: "public partial"}, "stop", 7, 3), err: errors.New("transport failed after body")},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

	result, trace, err := invoker.InvokeWithRetry(context.Background(), "system", "user", retentionToolDef(), nil)
	if err == nil || result == nil || result.TextContent != "public partial" {
		t.Fatalf("InvokeWithRetry result=%+v err=%v, want retained partial error", result, err)
	}
	if trace == nil || !trace.CostUnknown || trace.Cost != 0 || trace.Tokens.Input != 17 || trace.Tokens.Output != 4 {
		t.Fatalf("aggregate trace = %+v, want unknown aggregate usage 17/4", trace)
	}
	if ledger.InvocationCount() != 2 {
		t.Fatalf("ledger entries = %d, want one per underlying invocation", ledger.InvocationCount())
	}
	for _, entry := range ledger.Entries() {
		if entry.Cost != 0 || !entry.CostUnknown {
			t.Fatalf("ledger entry = %+v, want unknown zero known subtotal", entry)
		}
	}
}

func TestDefaultInvokerPricingUnknownInvokeWithToolsAccountingBoundary(t *testing.T) {
	t.Run("known success records monetary subtotal", func(t *testing.T) {
		ledger := transparency.NewCostLedger()
		client := &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: retentionChatResponse("resp-known", nil, model.Message{Role: "assistant", Content: "complete"}, "stop", 1_000_000, 1_000_000)},
		}}
		invoker := NewInvoker(InvokerConfig{
			Client:  client,
			Model:   "priced-model",
			Pricing: transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15},
			Ledger:  ledger,
		})

		content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
		if err != nil || content != "complete" {
			t.Fatalf("InvokeWithTools content=%q err=%v, want success", content, err)
		}
		requireCostProjection(t, trace, ledger, transparency.TokenUsage{Input: 1_000_000, Output: 1_000_000}, 18, false, 1)
	})

	t.Run("response error records observed nonzero usage and raw cause", func(t *testing.T) {
		runErr := errors.New("provider failed after body")
		ledger := transparency.NewCostLedger()
		client := &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: retentionChatResponse("resp-partial", nil, model.Message{Role: "assistant", Content: "partial"}, "stop", 8, 9), err: runErr},
		}}
		invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

		content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
		if !errors.Is(err, runErr) || content != "partial" {
			t.Fatalf("InvokeWithTools content=%q err=%v, want retained partial raw cause", content, err)
		}
		requireCostProjection(t, trace, ledger, transparency.TokenUsage{Input: 8, Output: 9}, 0, true, 1)
	})

	t.Run("response error records observed zero usage", func(t *testing.T) {
		ledger := transparency.NewCostLedger()
		client := &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: &model.ChatResponse{ID: "resp-zero"}, err: errors.New("provider failed after headers")},
		}}
		invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

		_, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
		if err == nil {
			t.Fatal("InvokeWithTools error = nil, want provider failure")
		}
		requireCostProjection(t, trace, ledger, transparency.TokenUsage{}, 0, true, 1)
	})

	t.Run("no response error does not record ledger", func(t *testing.T) {
		ledger := transparency.NewCostLedger()
		client := &retentionInvokerClient{responses: []retentionInvokerResponse{{err: errors.New("dial failed")}}}
		invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

		_, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
		if err == nil {
			t.Fatal("InvokeWithTools error = nil, want no-response failure")
		}
		if trace == nil || !trace.CostUnknown || trace.Cost != 0 {
			t.Fatalf("trace = %+v, want unknown build trace", trace)
		}
		if ledger.InvocationCount() != 0 {
			t.Fatalf("ledger entries = %d, want 0 without observed response", ledger.InvocationCount())
		}
	})

	t.Run("inconclusive response records observed usage", func(t *testing.T) {
		ledger := transparency.NewCostLedger()
		client := &retentionInvokerClient{responses: []retentionInvokerResponse{
			{resp: retentionChatResponse("resp-truncated", nil, model.Message{Role: "assistant", Content: "partial"}, "length", 6, 7)},
		}}
		invoker := NewInvoker(InvokerConfig{Client: client, Model: "unknown-model", PricingUnknown: true, Ledger: ledger})

		content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
		if err == nil || content == "" {
			t.Fatalf("InvokeWithTools content=%q err=%v, want incomplete partial", content, err)
		}
		requireCostProjection(t, trace, ledger, transparency.TokenUsage{Input: 6, Output: 7}, 0, true, 1)
	})
}

func requireCostProjection(t *testing.T, trace *transparency.Trace, ledger *transparency.CostLedger, wantTokens transparency.TokenUsage, wantCost float64, wantUnknown bool, wantEntries int) {
	t.Helper()
	wantTokens = expectedUsageWithFixtureReportedTotal(wantTokens)
	if trace == nil {
		t.Fatal("trace = nil")
	}
	if trace.Cost != wantCost || trace.CostUnknown != wantUnknown {
		t.Fatalf("trace cost/tokens = cost:%v unknown:%v tokens:%+v, want cost:%v unknown:%v tokens:%+v",
			trace.Cost, trace.CostUnknown, trace.Tokens, wantCost, wantUnknown, wantTokens)
	}
	assertTokenUsageContract(t, trace.Tokens, wantTokens)
	if ledger == nil {
		return
	}
	if ledger.InvocationCount() != wantEntries {
		t.Fatalf("ledger entries = %d, want %d", ledger.InvocationCount(), wantEntries)
	}
	if wantEntries == 0 {
		return
	}
	entries := ledger.Entries()
	last := entries[len(entries)-1]
	if last.Cost != wantCost || last.CostUnknown != wantUnknown {
		t.Fatalf("ledger last = cost:%v unknown:%v tokens:%+v, want cost:%v unknown:%v tokens:%+v",
			last.Cost, last.CostUnknown, last.Tokens, wantCost, wantUnknown, wantTokens)
	}
	assertTokenUsageContract(t, last.Tokens, wantTokens)
	if last.InvocationID != trace.ID || last.Latency != trace.Duration || last.Model != trace.Model {
		t.Fatalf("ledger last identity = model:%q invocation:%q latency:%s, want model:%q invocation:%q latency:%s",
			last.Model, last.InvocationID, last.Latency, trace.Model, trace.ID, trace.Duration)
	}
}

func expectedUsageWithFixtureReportedTotal(usage transparency.TokenUsage) transparency.TokenUsage {
	if usage.ReportedTotal == 0 && usage.Input+usage.Output > 0 {
		usage.ReportedTotal = usage.Input + usage.Output
	}
	return usage
}
