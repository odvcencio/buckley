package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecutionIdentityFoundationAuthoritativeRoute(t *testing.T) {
	response := &ChatResponse{ID: " actual-id ", Model: " actual-model ", ExecutionIdentity: &ExecutionIdentity{
		RequestedModel: "forged", SelectedModel: "forged", ProviderID: "forged",
		ResponseModel: "embedded-model", ResponseID: "embedded-id",
	}}
	stampChatResponseExecutionIdentity(response, " requested ", " selected ", " provider ")
	want := ExecutionIdentity{RequestedModel: "requested", SelectedModel: "selected", ProviderID: "provider", ResponseModel: "actual-model", ResponseID: "actual-id"}
	if response.ExecutionIdentity == nil || *response.ExecutionIdentity != want {
		t.Fatalf("identity = %+v", response.ExecutionIdentity)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requested_model", "selected_model", "provider_id", "response_model", "response_id"} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("missing %s in %s", field, encoded)
		}
	}
}

func TestExecutionIdentityFoundationDoesNotInventObservation(t *testing.T) {
	if got := observedExecutionIdentity(" ", "", nil); got != nil {
		t.Fatalf("empty observation = %+v", got)
	}
	for _, provider := range []string{"anthropic", "codex", "google"} {
		response := &ChatResponse{ID: "synthetic-id", Model: "request-echo"}
		stampChatResponseExecutionIdentity(response, "requested", "selected", provider)
		if response.ExecutionIdentity.ResponseID != "" || response.ExecutionIdentity.ResponseModel != "" {
			t.Fatalf("%s invented observation: %+v", provider, response.ExecutionIdentity)
		}
	}
}

func TestExecutionIdentityFoundationCopiesAndFlagsConflicts(t *testing.T) {
	original := &ExecutionIdentity{ProviderID: "provider", ResponseID: "first"}
	var identity *ExecutionIdentity
	mergeExecutionIdentity(&identity, original)
	original.ResponseID = "changed"
	if identity.ResponseID != "first" {
		t.Fatal("merge retained caller-owned pointer")
	}
	mergeExecutionIdentity(&identity, &ExecutionIdentity{ResponseID: "second"})
	if !identity.Conflicted || identity.ResponseID != "" {
		t.Fatalf("conflicting identities silently merged: %+v", identity)
	}
	mergeExecutionIdentity(&identity, &ExecutionIdentity{ResponseID: "third"})
	if !identity.Conflicted {
		t.Fatal("conflict flag was cleared")
	}
}

func TestExecutionIdentityFoundationSSEUsesWireObservation(t *testing.T) {
	wire := "data: {\"id\":\"wire-id\",\"model\":\"wire-model\",\"execution_identity\":{\"provider_id\":\"forged\",\"response_id\":\"forged\"},\"choices\":[]}\n\ndata: [DONE]\n\n"
	chunks := make(chan StreamChunk, 1)
	_, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(wire), chunks)
	if err != nil {
		t.Fatal(err)
	}
	chunk := <-chunks
	want := ExecutionIdentity{ResponseID: "wire-id", ResponseModel: "wire-model"}
	if chunk.ExecutionIdentity == nil || *chunk.ExecutionIdentity != want {
		t.Fatalf("identity = %+v", chunk.ExecutionIdentity)
	}
}

func TestExecutionIdentityFoundationPreservesBufferedPrefixOnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	chunks := make(chan StreamChunk, 1)
	chunks <- StreamChunk{ID: "prefix", Model: "observed"}
	errs := make(chan error, 1)
	sentinel := errors.New("provider failed")
	errs <- sentinel
	out, outErrs := stampStreamChunks(ctx, chunks, errs, "requested", "selected", "provider")
	count := 0
	for chunk := range out {
		count++
		if chunk.ExecutionIdentity == nil || chunk.ExecutionIdentity.ResponseID != "prefix" || chunk.ExecutionIdentity.SelectedModel != "selected" {
			t.Fatalf("prefix lost identity: %+v", chunk)
		}
	}
	if count != 1 {
		t.Fatalf("prefix count = %d", count)
	}
	if err := <-outErrs; !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
}

func TestExecutionIdentityFoundationCancellationUnblocksUndrainedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan StreamChunk, 1)
	chunks <- StreamChunk{ID: "prefix"}
	_, errs := stampStreamChunks(ctx, chunks, nil, "requested", "selected", "provider")
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("identity forwarding leaked after cancellation")
	}
}
