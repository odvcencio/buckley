package modelstep

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func validBlockedReplayFixture(t *testing.T) (runledger.ExecutionStep, evidence.Object, string) {
	t.Helper()
	rawProviderError := "upstream rejected token sk-" + strings.Repeat("a", 30) + " " + strings.Repeat("x", MaxPersistedErrorRunes+200)
	providerError := NormalizeErrorText(rawProviderError)
	body, err := EncodeResponse(ResponseEnvelope{
		Response: &model.ChatResponse{
			Choices:      []model.Choice{{Message: model.Message{Role: "assistant", Content: "durable partial"}}},
			Usage:        model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
			UsagePresent: true,
		},
		ChargedCostUSD: 0.42,
		CostRecorded:   true,
		Partial:        true,
		ProviderError:  providerError,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := evidence.ContentSHA256Hex(body)
	object := evidence.Object{ID: "ev_partial", Kind: evidence.KindModelResponse, MediaType: "application/json", ContentSHA256: digest, InlineBody: body}
	marker, err := EncodeBlockedMarker(BlockedMarker{
		Incomplete:         true,
		ProviderError:      providerError,
		DurabilityError:    NormalizeErrorText("completion failed: bearer " + strings.Repeat("b", 30)),
		ResponseEvidenceID: object.ID,
		OutputDigest:       digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	step := runledger.ExecutionStep{RunID: "run", StepID: "step", Kind: "model", Status: runledger.StepBlocked, Attempt: 1, Error: marker, OutputEvidenceID: object.ID, OutputDigest: digest}
	return step, object, providerError
}

func TestValidateBlockedReplay_RestoresControllerProjection(t *testing.T) {
	step, object, providerError := validBlockedReplayFixture(t)
	if strings.Contains(providerError, "sk-") || len([]rune(providerError)) > MaxPersistedErrorRunes {
		t.Fatalf("provider error was not redacted and bounded: %q", providerError)
	}
	replay, err := ValidateBlockedReplay(step, &object)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Marker.ProviderError != providerError || replay.Response == nil || replay.Response.ProviderError != providerError {
		t.Fatalf("provider projection = %+v", replay)
	}
	if replay.Response.Response.Usage.TotalTokens != 7 || replay.Response.ChargedCostUSD != 0.42 || !replay.Response.CostRecorded {
		t.Fatalf("accounting projection = %+v", replay.Response)
	}
	if got, _ := replay.Response.Response.Choices[0].Message.Content.(string); got != "durable partial" {
		t.Fatalf("content = %q", got)
	}
}

func TestValidateBlockedReplay_RejectsCorruptProjection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*runledger.ExecutionStep, *evidence.Object)
		want   string
	}{
		{name: "step digest", mutate: func(step *runledger.ExecutionStep, _ *evidence.Object) { step.OutputDigest = strings.Repeat("0", 64) }, want: "projection"},
		{name: "loaded digest", mutate: func(_ *runledger.ExecutionStep, object *evidence.Object) {
			object.ContentSHA256 = strings.Repeat("0", 64)
		}, want: "digest mismatch"},
		{name: "content digest", mutate: func(_ *runledger.ExecutionStep, object *evidence.Object) {
			object.InlineBody = append(object.InlineBody, ' ')
		}, want: "content digest mismatch"},
		{name: "response kind", mutate: func(_ *runledger.ExecutionStep, object *evidence.Object) { object.Kind = evidence.KindToolResult }, want: "invalid shape"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			step, object, _ := validBlockedReplayFixture(t)
			tt.mutate(&step, &object)
			if _, err := ValidateBlockedReplay(step, &object); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDurableRecords_RejectInvalidCausesResponseAndAccounting(t *testing.T) {
	step, object, providerError := validBlockedReplayFixture(t)
	var marker BlockedMarker
	if err := json.Unmarshal([]byte(strings.TrimPrefix(step.Error, BlockedMarkerPrefix)), &marker); err != nil {
		t.Fatal(err)
	}
	marker.DurabilityError = ""
	markerBody, _ := json.Marshal(marker)
	step.Error = BlockedMarkerPrefix + string(markerBody)
	if _, err := ValidateBlockedReplay(step, &object); err == nil || !strings.Contains(err.Error(), "requires provider and durability errors") {
		t.Fatalf("missing cause error = %v", err)
	}

	invalid := ResponseEnvelope{
		Version:       ResponseVersion,
		Response:      &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "partial"}}}},
		Partial:       true,
		ProviderError: providerError,
	}
	invalid.Response.Usage.TotalTokens = -1
	body, _ := json.Marshal(invalid)
	if _, err := DecodeResponse(body); err == nil || !strings.Contains(err.Error(), "invalid usage") {
		t.Fatalf("negative usage error = %v", err)
	}
	invalid.Response.Usage.TotalTokens = 0
	invalid.Response.Usage.PromptTokensDetails = &model.PromptTokensDetails{CachedTokens: -1}
	body, _ = json.Marshal(invalid)
	if _, err := DecodeResponse(body); err == nil || !strings.Contains(err.Error(), "cached-token") {
		t.Fatalf("negative cached usage error = %v", err)
	}
	invalid.Response.Usage.PromptTokensDetails = nil
	invalid.Response.Usage.CompletionTokenDetails = &model.CompletionTokenDetails{ReasoningTokens: -1}
	body, _ = json.Marshal(invalid)
	if _, err := DecodeResponse(body); err == nil || !strings.Contains(err.Error(), "reasoning-token") {
		t.Fatalf("negative reasoning usage error = %v", err)
	}
	invalid.Response.Usage.CompletionTokenDetails = nil
	invalid.CostRecorded = true
	invalid.ChargedCostUSD = -0.01
	body, _ = json.Marshal(invalid)
	if _, err := DecodeResponse(body); err == nil || !strings.Contains(err.Error(), "invalid charged cost") {
		t.Fatalf("negative cost error = %v", err)
	}
	for _, invalidCost := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		invalid.ChargedCostUSD = invalidCost
		if _, err := EncodeResponse(invalid); err == nil || !strings.Contains(err.Error(), "invalid charged cost") {
			t.Fatalf("non-finite cost %v error = %v", invalidCost, err)
		}
	}
	invalid.ChargedCostUSD = 0
	invalid.CostRecorded = false
	invalid.PricingError = "pricing failed sk-" + strings.Repeat("z", 30)
	if _, err := EncodeResponse(invalid); err == nil || !strings.Contains(err.Error(), "pricing error is not normalized") {
		t.Fatalf("noncanonical pricing error = %v", err)
	}
	invalid.PricingError = NormalizeErrorText(invalid.PricingError)
	invalid.Response = nil
	if _, err := EncodeResponse(invalid); err == nil || !strings.Contains(err.Error(), "has no response") {
		t.Fatalf("response shape error = %v", err)
	}
}

// TestEncodeDecodeResponse_PreservesNativeFinishReasonAndUsagePresent covers
// the stealth/ox-alpha empty-response incident's two durable-evidence
// requirements: native_finish_reason survives into the recorded
// buckley.model-response/v1 envelope, and a response with no usage object
// (the OpenRouter early-200 transport failure shell) stays distinguishable
// from one with an honest, literally-zero usage object after a full
// encode/decode round trip through evidence.
func TestEncodeDecodeResponse_PreservesNativeFinishReasonAndUsagePresent(t *testing.T) {
	for _, tt := range []struct {
		name         string
		usagePresent bool
	}{
		{name: "usage absent -- the transport failure shell", usagePresent: false},
		{name: "usage present with literal zero fields", usagePresent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			envelope := ResponseEnvelope{
				Response: &model.ChatResponse{
					Choices: []model.Choice{{
						Message:            model.Message{Role: "assistant", Content: nil},
						FinishReason:       "stop",
						NativeFinishReason: "network_error",
					}},
					UsagePresent: tt.usagePresent,
				},
			}
			body, err := EncodeResponse(envelope)
			if err != nil {
				t.Fatalf("EncodeResponse: %v", err)
			}
			decoded, err := DecodeResponse(body)
			if err != nil {
				t.Fatalf("DecodeResponse: %v", err)
			}
			if decoded.Response.Choices[0].NativeFinishReason != "network_error" {
				t.Fatalf("native_finish_reason = %q, want network_error", decoded.Response.Choices[0].NativeFinishReason)
			}
			if decoded.Response.UsagePresent != tt.usagePresent {
				t.Fatalf("UsagePresent = %v, want %v", decoded.Response.UsagePresent, tt.usagePresent)
			}
		})
	}
}
