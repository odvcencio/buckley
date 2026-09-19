package model

import (
	"context"
	"testing"
	"time"
)

func TestManagerStreamingEntryPointsShareResolvedDispatch(t *testing.T) {
	for _, providerID := range []string{"openrouter", "selected"} {
		for _, governed := range []bool{false, true} {
			name := providerID + "/ordinary"
			if governed {
				name = providerID + "/governed"
			}
			t.Run(name, func(t *testing.T) {
				provider := &stubProvider{
					id: providerID,
					streamPlans: []stubStreamPlan{{chunks: []StreamChunk{{
						ID: "observed-id", Model: "observed-model",
						Choices: []StreamChoice{{Delta: MessageDelta{Content: "answer"}}},
					}}}},
				}
				mgr := identityTestManager(provider)
				requested := providerID + "/model"
				req := ChatRequest{
					Model: requested, ToolChoice: "auto",
					Messages:  []Message{{Role: "user", Content: "hello"}},
					Reasoning: &ReasoningConfig{Effort: " Medium ", MaxTokens: 512},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var chunks <-chan StreamChunk
				var errs <-chan error
				if governed {
					route, err := mgr.ResolveModelRoute(requested)
					if err != nil {
						t.Fatal(err)
					}
					chunks, errs = mgr.ChatCompletionStreamForRoute(ctx, req, route)
				} else {
					chunks, errs = mgr.ChatCompletionStream(ctx, req)
				}
				var received []StreamChunk
				for chunk := range chunks {
					received = append(received, chunk)
				}
				for err := range errs {
					if err != nil {
						t.Fatal(err)
					}
				}
				if len(received) != 1 || received[0].Choices[0].Delta.Content != "answer" {
					t.Fatalf("chunks = %+v", received)
				}
				identity := received[0].ExecutionIdentity
				if identity == nil || identity.RequestedModel != requested || identity.SelectedModel != requested || identity.ProviderID != providerID || identity.ResponseModel != "observed-model" || identity.ResponseID != "observed-id" {
					t.Fatalf("identity = %+v", identity)
				}
				if len(provider.streamRequests) != 1 {
					t.Fatalf("requests = %d, want 1", len(provider.streamRequests))
				}
				wire := provider.streamRequests[0]
				if wire.Model != "model" || wire.ToolChoice != "" || wire.Reasoning == nil || wire.Reasoning.Effort != "medium" || wire.Reasoning.MaxTokens != 0 {
					t.Fatalf("transformed request = %+v", wire)
				}
			})
		}
	}
}
