package modelstep

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestResponseEnvelope_PartialRetryDecisionRoundTrip(t *testing.T) {
	envelope := ResponseEnvelope{Response: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "partial"}}}}, Partial: true, PartialRetryable: true, ProviderError: "unexpected EOF"}
	body, err := EncodeResponse(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponse(body)
	if err != nil || !decoded.PartialRetryable {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
	envelope.PartialRetryable = false
	body, err = EncodeResponse(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "partial_retryable") {
		t.Fatal("legacy shape unexpectedly opted in to retry")
	}
	decoded, err = DecodeResponse(body)
	if err != nil || decoded.PartialRetryable {
		t.Fatalf("legacy decoded=%+v error=%v", decoded, err)
	}
	envelope.Partial = false
	envelope.ProviderError = ""
	envelope.PartialRetryable = true
	if _, err = EncodeResponse(envelope); err == nil {
		t.Fatal("conclusive response accepted a partial retry flag")
	}
}
