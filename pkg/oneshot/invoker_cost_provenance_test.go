package oneshot

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type costProvenanceClient struct {
	response *model.ChatResponse
	err      error
}

func (c costProvenanceClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	return c.response, c.err
}

type costProvenanceStreamClient struct{ costProvenanceClient }

func (c costProvenanceStreamClient) ChatCompletionStream(ctx context.Context, _ model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk)
	errs := make(chan error)
	go func() {
		defer close(chunks)
		defer close(errs)
		if c.response != nil {
			select {
			case chunks <- model.StreamChunk{Usage: &c.response.Usage}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case errs <- c.err:
		case <-ctx.Done():
		}
	}()
	return chunks, errs
}

func TestCostProvenance_InvokerPaths(t *testing.T) {
	response := &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}, Usage: model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}
	for _, method := range []string{"invoke", "text", "stream", "stream-fallback", "tools"} {
		for _, pricing := range []struct {
			name    string
			unknown bool
			rates   transparency.ModelPricing
			cost    float64
		}{
			{"unknown", true, transparency.ModelPricing{InputPerMillion: 10000, OutputPerMillion: 20000}, 0},
			{"known-zero", false, transparency.ModelPricing{}, 0},
			{"known", false, transparency.ModelPricing{InputPerMillion: 10000, OutputPerMillion: 20000}, 2},
		} {
			t.Run(method+"/"+pricing.name, func(t *testing.T) {
				var client ModelClient = costProvenanceClient{response: response}
				if method == "stream" {
					client = &mockStreamClient{responses: []*model.ChatResponse{response}}
				}
				ledger := transparency.NewCostLedger()
				inv := NewInvoker(InvokerConfig{Client: client, Model: "test", Pricing: pricing.rates, PricingUnknown: pricing.unknown, Ledger: ledger})
				trace, err := invokeCostProvenanceMethod(inv, method)
				if err != nil {
					t.Fatal(err)
				}
				if trace == nil || trace.CostUnknown != pricing.unknown || trace.Cost != pricing.cost {
					t.Fatalf("trace = %+v", trace)
				}
				summary := ledger.Summary()
				if summary.InvocationCount != 1 || summary.SessionCostUnknown != pricing.unknown || summary.SessionCost != pricing.cost || summary.SessionTokens.Input != 100 || summary.SessionTokens.Output != 50 {
					t.Fatalf("summary = %+v", summary)
				}
				entry := ledger.Entries()[0]
				if entry.CostUnknown != pricing.unknown || entry.InvocationID != trace.ID {
					t.Fatalf("entry = %+v", entry)
				}
			})
		}
	}
}

// TestCostProvenance_OpenRouterBYOKCostOverridesUnknownPricing covers C7:
// an OpenRouter BYOK call (openai/gpt-6-luna-pro, live-verified payload
// shape) with no catalog pricing (PricingUnknown: true, matching a
// provider whose per-token rate Buckley cannot know) must still report a
// known cost, taken from the provider's own inline usage.cost +
// usage.cost_details.upstream_inference_cost, instead of "cost unknown".
// This is what makes --budget and repair work for a BYOK model like Luna.
func TestCostProvenance_OpenRouterBYOKCostOverridesUnknownPricing(t *testing.T) {
	byokFee := 0.0
	response := &model.ChatResponse{
		Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
		Usage: model.Usage{
			PromptTokens:     1396,
			CompletionTokens: 41,
			TotalTokens:      1437,
			Cost:             &byokFee,
			IsBYOK:           true,
			CostDetails:      &model.UsageCostDetails{UpstreamInferenceCost: 0.0001601},
		},
	}
	for _, method := range []string{"invoke", "text", "stream", "tools"} {
		t.Run(method, func(t *testing.T) {
			var client ModelClient = costProvenanceClient{response: response}
			if method == "stream" {
				client = &mockStreamClient{responses: []*model.ChatResponse{response}}
			}
			ledger := transparency.NewCostLedger()
			inv := NewInvoker(InvokerConfig{
				Client:         client,
				Model:          "openai/gpt-6-luna-pro",
				PricingUnknown: true,
				Ledger:         ledger,
			})
			trace, err := invokeCostProvenanceMethod(inv, method)
			if err != nil {
				t.Fatal(err)
			}
			if trace == nil || trace.CostUnknown {
				t.Fatalf("trace = %+v, want a known BYOK cost", trace)
			}
			if trace.Cost != 0.0001601 {
				t.Fatalf("trace.Cost = %v, want 0.0001601 (fee 0 + upstream 0.0001601)", trace.Cost)
			}
			summary := ledger.Summary()
			if summary.SessionCostUnknown || summary.SessionCost != 0.0001601 {
				t.Fatalf("summary = %+v, want a known session cost of 0.0001601", summary)
			}
		})
	}
}

func TestCostProvenance_PartialAndNoResponse(t *testing.T) {
	for _, method := range []string{"invoke", "text", "stream", "stream-fallback", "tools"} {
		for _, observed := range []bool{false, true} {
			t.Run(method+"/"+map[bool]string{false: "no-response", true: "partial"}[observed], func(t *testing.T) {
				var response *model.ChatResponse
				if observed {
					response = &model.ChatResponse{Usage: model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}
				}
				ledger := transparency.NewCostLedger()
				baseClient := costProvenanceClient{response: response, err: errors.New("provider interrupted")}
				var client ModelClient = baseClient
				if method == "stream" {
					client = costProvenanceStreamClient{baseClient}
				}
				inv := NewInvoker(InvokerConfig{Client: client, Model: "test", PricingUnknown: true, Pricing: transparency.ModelPricing{InputPerMillion: 10000}, Ledger: ledger})
				trace, err := invokeCostProvenanceMethod(inv, method)
				if err == nil || trace == nil || !trace.CostUnknown || trace.Cost != 0 {
					t.Fatalf("trace=%+v err=%v", trace, err)
				}
				wantCount := 0
				if observed {
					wantCount = 1
				}
				summary := ledger.Summary()
				if summary.InvocationCount != wantCount || summary.SessionCostUnknown != observed {
					t.Fatalf("summary=%+v observed=%v", summary, observed)
				}
			})
		}
	}
}

func invokeCostProvenanceMethod(inv *DefaultInvoker, method string) (*transparency.Trace, error) {
	ctx := context.Background()
	tool := tools.Definition{Name: "test", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}
	switch method {
	case "invoke":
		_, trace, err := inv.Invoke(ctx, "system", "user", tool, nil)
		return trace, err
	case "text":
		_, trace, err := inv.InvokeText(ctx, "system", "user", nil)
		return trace, err
	case "stream", "stream-fallback":
		_, trace, err := inv.InvokeStream(ctx, "system", "user", tool, nil, nil)
		return trace, err
	default:
		_, trace, err := inv.InvokeWithTools(ctx, "system", "user", []tools.Definition{tool}, &mockToolExecutor{}, 2)
		return trace, err
	}
}

func TestCostProvenance_IncompleteToolLoopRetainsUsage(t *testing.T) {
	client := &multiResponseClient{responses: []*model.ChatResponse{
		{Choices: []model.Choice{{Message: model.Message{ToolCalls: []model.ToolCall{{ID: "read", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: "{}"}}}}}}, Usage: model.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
		{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Reasoning: "not final output"}}}, Usage: model.Usage{CompletionTokens: 1, TotalTokens: 1}},
		{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Reasoning: "not final output"}}}, Usage: model.Usage{CompletionTokens: 1, TotalTokens: 1}},
		{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Reasoning: "not final output"}}}, Usage: model.Usage{CompletionTokens: 1, TotalTokens: 1}},
	}}
	ledger := transparency.NewCostLedger()
	inv := NewInvoker(InvokerConfig{Client: client, Model: "test", PricingUnknown: true, Ledger: ledger})
	_, trace, err := inv.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{{Name: "read_file", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}}, &mockToolExecutor{}, 1)
	if err == nil || trace == nil || !trace.CostUnknown || trace.Cost != 0 || trace.Error == "" {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}
	summary := ledger.Summary()
	if client.callCount != 4 || summary.InvocationCount != 1 || !summary.SessionCostUnknown || summary.SessionTokens.Input != 10 || summary.SessionTokens.Output != 5 {
		t.Fatalf("calls=%d summary=%+v", client.callCount, summary)
	}
}
