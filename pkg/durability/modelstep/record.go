// Package modelstep defines the durable wire records shared by model-step
// execution and read-only replay verification.
package modelstep

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

const (
	ResponseVersion        = "buckley.model-response/v1"
	BlockedMarkerVersion   = "buckley.model-response-retry-blocked/v1"
	BlockedMarkerPrefix    = BlockedMarkerVersion + ":"
	MaxPersistedErrorRunes = 2048
)

// ResponseEnvelope is the replayable provider response and its original
// accounting projection.
type ResponseEnvelope struct {
	Version        string              `json:"version"`
	Response       *model.ChatResponse `json:"response"`
	ChargedCostUSD float64             `json:"charged_cost_usd,omitempty"`
	CostRecorded   bool                `json:"cost_recorded"`
	PricingError   string              `json:"pricing_error,omitempty"`
	Partial        bool                `json:"partial,omitempty"`
	ProviderError  string              `json:"provider_error,omitempty"`
}

// BlockedMarker is the terminal record for a provider response whose durable
// completion became ambiguous. Incomplete distinguishes it from successful
// replay records even when no response evidence was stored.
type BlockedMarker struct {
	Version            string `json:"version"`
	Incomplete         bool   `json:"incomplete"`
	ProviderError      string `json:"provider_error"`
	DurabilityError    string `json:"durability_error"`
	ResponseEvidenceID string `json:"response_evidence_id,omitempty"`
	OutputDigest       string `json:"output_digest,omitempty"`
}

// DecodedResponse is the validated projection used for replay/accounting.
type DecodedResponse struct {
	Response       *model.ChatResponse
	ChargedCostUSD float64
	CostRecorded   bool
	PricingError   string
	Partial        bool
	ProviderError  string
	Legacy         bool
}

// BlockedReplay is a validated blocked marker plus its optional response.
type BlockedReplay struct {
	Marker   BlockedMarker
	Response *DecodedResponse
}

// NormalizeError produces the sole persisted projection of an error.
func NormalizeError(err error) string {
	if err == nil {
		return ""
	}
	return NormalizeErrorText(err.Error())
}

// NormalizeErrorText redacts known credentials and bounds durable text.
func NormalizeErrorText(text string) string {
	text = strings.TrimSpace(string(evidence.Redact([]byte(text))))
	runes := []rune(text)
	if len(runes) <= MaxPersistedErrorRunes {
		return text
	}
	return string(runes[:MaxPersistedErrorRunes-1]) + "…"
}

// EncodeResponse validates and marshals the current response envelope.
func EncodeResponse(envelope ResponseEnvelope) ([]byte, error) {
	if envelope.Version == "" {
		envelope.Version = ResponseVersion
	}
	if err := validateResponseEnvelope(envelope); err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

// DecodeResponse accepts current envelopes and legacy raw ChatResponse bodies.
func DecodeResponse(body []byte) (DecodedResponse, error) {
	var header struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &header); err != nil {
		return DecodedResponse{}, err
	}
	if header.Version == "" {
		response := &model.ChatResponse{}
		if err := json.Unmarshal(body, response); err != nil {
			return DecodedResponse{}, err
		}
		if err := validateUsage(response.Usage); err != nil {
			return DecodedResponse{}, err
		}
		return DecodedResponse{Response: response, Legacy: true}, nil
	}
	if header.Version != ResponseVersion {
		return DecodedResponse{}, fmt.Errorf("unsupported model response evidence version %q", header.Version)
	}
	var envelope ResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return DecodedResponse{}, err
	}
	if err := validateResponseEnvelope(envelope); err != nil {
		return DecodedResponse{}, err
	}
	return DecodedResponse{
		Response:       envelope.Response,
		ChargedCostUSD: envelope.ChargedCostUSD,
		CostRecorded:   envelope.CostRecorded,
		PricingError:   envelope.PricingError,
		Partial:        envelope.Partial,
		ProviderError:  envelope.ProviderError,
	}, nil
}

// EncodeBlockedMarker validates and marshals a terminal blocked marker.
func EncodeBlockedMarker(marker BlockedMarker) (string, error) {
	if marker.Version == "" {
		marker.Version = BlockedMarkerVersion
	}
	if err := validateBlockedMarker(marker); err != nil {
		return "", err
	}
	body, err := json.Marshal(marker)
	if err != nil {
		return "", err
	}
	return BlockedMarkerPrefix + string(body), nil
}

// ParseBlockedMarker parses and validates the versioned terminal marker.
func ParseBlockedMarker(text string) (BlockedMarker, error) {
	if !strings.HasPrefix(text, BlockedMarkerPrefix) {
		return BlockedMarker{}, fmt.Errorf("model step has no supported blocked marker")
	}
	var marker BlockedMarker
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, BlockedMarkerPrefix)), &marker); err != nil {
		return BlockedMarker{}, fmt.Errorf("decode blocked marker: %w", err)
	}
	if err := validateBlockedMarker(marker); err != nil {
		return BlockedMarker{}, err
	}
	return marker, nil
}

// ValidateBlockedReplay verifies the marker, step projection, optional
// evidence object, and response envelope before any replay accounting occurs.
func ValidateBlockedReplay(step runledger.ExecutionStep, object *evidence.Object) (BlockedReplay, error) {
	marker, err := ValidateBlockedStep(step)
	if err != nil {
		return BlockedReplay{}, err
	}
	if marker.ResponseEvidenceID == "" {
		if object != nil {
			return BlockedReplay{}, fmt.Errorf("blocked model step without evidence unexpectedly loaded an object")
		}
		return BlockedReplay{Marker: marker}, nil
	}
	if object == nil {
		return BlockedReplay{}, fmt.Errorf("blocked model step requires response evidence %s", marker.ResponseEvidenceID)
	}
	decoded, err := ValidateResponseEvidence(marker.ResponseEvidenceID, marker.OutputDigest, *object)
	if err != nil {
		return BlockedReplay{}, err
	}
	if decoded.Legacy || !decoded.Partial {
		return BlockedReplay{}, fmt.Errorf("blocked response evidence %s is not an incomplete provider response", object.ID)
	}
	if decoded.ProviderError != marker.ProviderError {
		return BlockedReplay{}, fmt.Errorf("blocked response provider error does not match its marker")
	}
	return BlockedReplay{Marker: marker, Response: &decoded}, nil
}

// ValidateResponseEvidence verifies a model-response object's durable
// identity, shape, content digest, and shared response envelope.
func ValidateResponseEvidence(evidenceID, outputDigest string, object evidence.Object) (DecodedResponse, error) {
	if strings.TrimSpace(evidenceID) == "" || strings.TrimSpace(outputDigest) == "" {
		return DecodedResponse{}, fmt.Errorf("model response evidence identity and digest are required")
	}
	if object.ID != evidenceID {
		return DecodedResponse{}, fmt.Errorf("model response evidence identity mismatch: got %s, want %s", object.ID, evidenceID)
	}
	if object.Kind != evidence.KindModelResponse || object.MediaType != "application/json" {
		return DecodedResponse{}, fmt.Errorf("model response evidence %s has invalid shape", object.ID)
	}
	if object.ContentSHA256 != outputDigest {
		return DecodedResponse{}, fmt.Errorf("model response evidence %s digest mismatch", object.ID)
	}
	if evidence.ContentSHA256Hex(object.InlineBody) != object.ContentSHA256 {
		return DecodedResponse{}, fmt.Errorf("model response evidence %s content digest mismatch", object.ID)
	}
	decoded, err := DecodeResponse(object.InlineBody)
	if err != nil {
		return DecodedResponse{}, fmt.Errorf("decode model response evidence %s: %w", object.ID, err)
	}
	return decoded, nil
}

// ValidateBlockedStep validates the terminal marker and its projection onto
// the execution step before callers load any referenced evidence.
func ValidateBlockedStep(step runledger.ExecutionStep) (BlockedMarker, error) {
	if step.Status != runledger.StepBlocked {
		return BlockedMarker{}, fmt.Errorf("blocked model replay requires step status %q, got %q", runledger.StepBlocked, step.Status)
	}
	marker, err := ParseBlockedMarker(step.Error)
	if err != nil {
		return BlockedMarker{}, err
	}
	if step.OutputEvidenceID != marker.ResponseEvidenceID || step.OutputDigest != marker.OutputDigest {
		return BlockedMarker{}, fmt.Errorf("blocked marker evidence projection does not match the execution step")
	}
	return marker, nil
}

func validateBlockedMarker(marker BlockedMarker) error {
	if marker.Version != BlockedMarkerVersion {
		return fmt.Errorf("unsupported blocked marker version %q", marker.Version)
	}
	if !marker.Incomplete {
		return fmt.Errorf("blocked marker must be incomplete")
	}
	if strings.TrimSpace(marker.ProviderError) == "" || strings.TrimSpace(marker.DurabilityError) == "" {
		return fmt.Errorf("blocked marker requires provider and durability errors")
	}
	if NormalizeErrorText(marker.ProviderError) != marker.ProviderError || NormalizeErrorText(marker.DurabilityError) != marker.DurabilityError {
		return fmt.Errorf("blocked marker errors are not normalized")
	}
	if (marker.ResponseEvidenceID == "") != (marker.OutputDigest == "") {
		return fmt.Errorf("blocked marker evidence and digest must both be present or absent")
	}
	return nil
}

func validateResponseEnvelope(envelope ResponseEnvelope) error {
	if envelope.Version != ResponseVersion {
		return fmt.Errorf("unsupported model response evidence version %q", envelope.Version)
	}
	if envelope.Response == nil {
		return fmt.Errorf("model response evidence envelope has no response")
	}
	if err := validateUsage(envelope.Response.Usage); err != nil {
		return err
	}
	if envelope.CostRecorded {
		if envelope.ChargedCostUSD < 0 || math.IsNaN(envelope.ChargedCostUSD) || math.IsInf(envelope.ChargedCostUSD, 0) {
			return fmt.Errorf("model response evidence has invalid charged cost")
		}
		if strings.TrimSpace(envelope.PricingError) != "" {
			return fmt.Errorf("model response evidence cannot record cost and a pricing error")
		}
	} else if envelope.ChargedCostUSD != 0 {
		return fmt.Errorf("model response evidence has an unrecorded charged cost")
	}
	if envelope.PricingError != "" && NormalizeErrorText(envelope.PricingError) != envelope.PricingError {
		return fmt.Errorf("model response evidence pricing error is not normalized")
	}
	if envelope.Partial {
		if strings.TrimSpace(envelope.ProviderError) == "" || NormalizeErrorText(envelope.ProviderError) != envelope.ProviderError {
			return fmt.Errorf("partial model response evidence requires a normalized provider error")
		}
	} else if strings.TrimSpace(envelope.ProviderError) != "" {
		return fmt.Errorf("conclusive model response evidence cannot carry a provider error")
	}
	return nil
}

func validateUsage(usage model.Usage) error {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.CacheWriteTokens < 0 {
		return fmt.Errorf("model response evidence has invalid usage")
	}
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens < 0 {
		return fmt.Errorf("model response evidence has invalid cached-token usage")
	}
	if usage.CompletionTokenDetails != nil && usage.CompletionTokenDetails.ReasoningTokens < 0 {
		return fmt.Errorf("model response evidence has invalid reasoning-token usage")
	}
	return nil
}
