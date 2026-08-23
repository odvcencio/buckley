package sessionexec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+/-]*$`)
	codeRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

func validationError(message string) error {
	return fmt.Errorf("%w: %s", ErrValidation, message)
}

func ValidateSessionID(value string) error {
	return validateIdentifier("session id", value, MaxSessionIDBytes)
}

func ValidateCommandID(value string) error {
	return validateIdentifier("command id", value, MaxCommandIDBytes)
}

func ValidateErrorCode(value string) error {
	if value == "" {
		return nil
	}
	return validateCode("error code", value, MaxErrorCodeBytes)
}

func ValidateExecutionMode(value ExecutionMode, allowHeadless bool) error {
	switch value {
	case ExecutionModeHeadless:
		if !allowHeadless {
			return validationError("headless mode is not a quiesced state")
		}
		return nil
	case ExecutionModeDetached, ExecutionModeAdopted:
		return nil
	default:
		return validationError("unsupported execution mode")
	}
}

// ValidateAcceptRequest validates the caller-provided request after an
// optional command identifier has been resolved by the adapter.
func ValidateAcceptRequest(req AcceptRequest, commandID string) error {
	if err := validateIdentifier("session id", req.SessionID, MaxSessionIDBytes); err != nil {
		return err
	}
	if err := validateIdentifier("command id", commandID, MaxCommandIDBytes); err != nil {
		return err
	}
	if req.CommandID != "" && req.CommandID != commandID {
		return validationError("resolved command id mismatch")
	}
	if err := validateCode("command type", req.Type, MaxCommandTypeBytes); err != nil {
		return err
	}
	if _, err := LaneFor(req.Type); err != nil {
		return err
	}
	if err := validateIdentifier("accepted principal", req.AcceptedBy, MaxPrincipalBytes); err != nil {
		return err
	}
	if err := validateBody("command content", req.Content, MaxContentBytes); err != nil {
		return err
	}
	switch strings.ToLower(req.Type) {
	case "input", "queue", "steer", "model", "slash":
		if req.Content == "" {
			return validationError("command content is required")
		}
	}
	return nil
}

func ValidateClaimRequest(req ClaimRequest) error {
	if err := validateIdentifier("session id", req.SessionID, MaxSessionIDBytes); err != nil {
		return err
	}
	if req.Lane != LaneWork && req.Lane != LaneControl {
		return validationError("unsupported lane")
	}
	if err := validateIdentifier("lease owner", req.Owner, MaxLeaseOwnerBytes); err != nil {
		return err
	}
	return ValidateLeaseDuration(req.LeaseDuration)
}

func ValidateLeaseDuration(value time.Duration) error {
	if value < time.Millisecond || value > MaxLeaseDuration {
		return validationError("lease duration out of range")
	}
	return nil
}

func ValidateLeaseRef(ref LeaseRef) error {
	if err := validateIdentifier("session id", ref.SessionID, MaxSessionIDBytes); err != nil {
		return err
	}
	if err := validateIdentifier("command id", ref.CommandID, MaxCommandIDBytes); err != nil {
		return err
	}
	if err := validateIdentifier("lease owner", ref.Owner, MaxLeaseOwnerBytes); err != nil {
		return err
	}
	if ref.Generation < 0 || ref.LeaseGeneration <= 0 {
		return validationError("invalid lease generation")
	}
	return nil
}

func ValidateEffectRequest(req EffectRequest) error {
	if err := ValidateLeaseRef(req.Lease); err != nil {
		return err
	}
	if err := validateIdentifier("effect id", req.EffectID, MaxEffectIDBytes); err != nil {
		return err
	}
	switch req.Kind {
	case EffectKindModel, EffectKindTool:
		return nil
	default:
		return validationError("unsupported effect kind")
	}
}

func ValidateEffectPermit(permit EffectPermit) error {
	if err := ValidateEffectRequest(permit.EffectRequest); err != nil {
		return err
	}
	if permit.ExpiresAt.IsZero() || permit.CreatedAt.IsZero() {
		return validationError("effect permit timestamps are required")
	}
	if permit.ExpiresAt.Before(permit.CreatedAt) {
		return validationError("effect permit expiry precedes creation")
	}
	if err := validateEffectPermitState(permit); err != nil {
		return err
	}
	if permit.ResolvedBy != "" {
		if err := validateIdentifier("effect resolver", permit.ResolvedBy, MaxPrincipalBytes); err != nil {
			return err
		}
	}
	if err := validateBody("effect resolution reason", permit.ResolutionReason, MaxEffectResolutionReasonBytes); err != nil {
		return err
	}
	return nil
}

func validateEffectPermitState(permit EffectPermit) error {
	if permit.AmbiguousAt != nil && permit.AmbiguousAt.Before(permit.CreatedAt) {
		return validationError("effect ambiguity precedes creation")
	}
	if permit.EndedAt != nil && permit.EndedAt.Before(permit.CreatedAt) {
		return validationError("effect end precedes creation")
	}
	if permit.EndedAt != nil && permit.AmbiguousAt != nil && permit.EndedAt.Before(*permit.AmbiguousAt) {
		return validationError("effect end precedes ambiguity")
	}
	if permit.ResolvedAt != nil && (permit.ResolvedAt.Before(permit.CreatedAt) ||
		(permit.AmbiguousAt != nil && permit.ResolvedAt.Before(*permit.AmbiguousAt))) {
		return validationError("effect resolution precedes ambiguity")
	}
	if permit.ResolvedAt != nil && permit.ResolvedAt.Before(permit.ExpiresAt) {
		return validationError("effect resolution precedes expiry")
	}
	switch permit.State {
	case EffectStateActive:
		if permit.AmbiguousAt != nil || permit.EndedAt != nil || permit.ResolvedAt != nil || permit.ResolvedBy != "" || permit.ResolutionReason != "" {
			return validationError("active effect permit has terminal fields")
		}
	case EffectStateAmbiguous:
		if permit.AmbiguousAt == nil || permit.EndedAt != nil || permit.ResolvedAt != nil || permit.ResolvedBy != "" || permit.ResolutionReason != "" {
			return validationError("ambiguous effect permit fields are invalid")
		}
	case EffectStateEnded:
		if permit.EndedAt == nil || permit.ResolvedAt != nil || permit.ResolvedBy != "" || permit.ResolutionReason != "" {
			return validationError("ended effect permit fields are invalid")
		}
	case EffectStateResolved:
		if permit.AmbiguousAt == nil || permit.EndedAt != nil || permit.ResolvedAt == nil ||
			permit.ResolvedBy == "" || strings.TrimSpace(permit.ResolutionReason) == "" {
			return validationError("resolved effect permit fields are invalid")
		}
	default:
		return validationError("unsupported effect permit state")
	}
	return nil
}

func ValidateEffectResolutionRequest(req EffectResolutionRequest) error {
	if err := ValidateSessionID(req.SessionID); err != nil {
		return err
	}
	if err := ValidateCommandID(req.CommandID); err != nil {
		return err
	}
	if req.Generation < 0 {
		return validationError("invalid effect generation")
	}
	if err := validateIdentifier("effect id", req.EffectID, MaxEffectIDBytes); err != nil {
		return err
	}
	if err := validateIdentifier("effect resolver", req.Actor, MaxPrincipalBytes); err != nil {
		return err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return validationError("effect resolution reason is required")
	}
	return validateBody("effect resolution reason", req.Reason, MaxEffectResolutionReasonBytes)
}

// NormalizeCompletion validates and canonicalizes the bounded completion
// projection. References are sorted and deduplicated for stable replay.
func NormalizeCompletion(value Completion) (Completion, error) {
	if !value.State.Terminal() {
		return Completion{}, validationError("completion state is not terminal")
	}
	if value.ErrorCode != "" {
		if err := validateCode("error code", value.ErrorCode, MaxErrorCodeBytes); err != nil {
			return Completion{}, err
		}
	}
	if err := validateBody("error text", value.Error, MaxErrorTextBytes); err != nil {
		return Completion{}, err
	}
	if value.Outcome.Code != "" {
		if err := validateCode("outcome code", value.Outcome.Code, MaxOutcomeCodeBytes); err != nil {
			return Completion{}, err
		}
	}
	evidence, err := normalizeReferences("evidence id", value.Outcome.EvidenceIDs)
	if err != nil {
		return Completion{}, err
	}
	artifacts, err := normalizeReferences("artifact id", value.Outcome.ArtifactIDs)
	if err != nil {
		return Completion{}, err
	}
	value.Outcome.EvidenceIDs = evidence
	value.Outcome.ArtifactIDs = artifacts
	return value, nil
}

func (state State) Terminal() bool {
	switch state {
	case StateSucceeded, StateFailed, StateBlocked, StateInterrupted, StateCancelled:
		return true
	default:
		return false
	}
}

func (state State) Valid() bool {
	return state == StateAccepted || state == StateRunning || state.Terminal()
}

// ValidateTranscriptEntries returns a canonical copy. Ordinals must be
// contiguous from nextOrdinal so a retry cannot create gaps or reorder turns.
func ValidateTranscriptEntries(entries []TranscriptEntry, nextOrdinal int) ([]TranscriptEntry, error) {
	if nextOrdinal < 0 || nextOrdinal > MaxTranscriptOrdinal {
		return nil, validationError("transcript next ordinal out of range")
	}
	if len(entries) > MaxTranscriptEntries {
		return nil, validationError("too many transcript entries")
	}
	canonical := append([]TranscriptEntry(nil), entries...)
	var totalBytes int64
	var totalTokens int64
	for i := range canonical {
		entry := &canonical[i]
		if entry.Ordinal != nextOrdinal+i || entry.Ordinal > MaxTranscriptOrdinal {
			return nil, validationError("transcript ordinals must be contiguous")
		}
		switch entry.Role {
		case "user", "assistant", "system", "tool":
		default:
			return nil, validationError("unsupported transcript role")
		}
		if entry.ContentType == "" {
			entry.ContentType = "text"
		}
		if err := validateCode("content type", entry.ContentType, MaxCommandTypeBytes); err != nil {
			return nil, err
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"transcript content", entry.Content},
			{"transcript content json", entry.ContentJSON},
			{"transcript tool calls", entry.ToolCalls},
			{"transcript reasoning", entry.Reasoning},
			{"transcript reasoning details", entry.ReasoningDetails},
		} {
			if err := validateBody(field.name, field.value, MaxTranscriptEntryBytes); err != nil {
				return nil, err
			}
			fieldBytes := int64(len(field.value))
			if totalBytes > int64(MaxTranscriptTotalBytes)-fieldBytes {
				return nil, validationError("transcript payload too large")
			}
			totalBytes += fieldBytes
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"tool call id", entry.ToolCallID},
			{"message name", entry.Name},
		} {
			if field.value != "" {
				if err := validateIdentifier(field.name, field.value, MaxReferenceIDBytes); err != nil {
					return nil, err
				}
			}
		}
		for _, raw := range []string{entry.ContentJSON, entry.ToolCalls, entry.ReasoningDetails} {
			if raw != "" && !json.Valid([]byte(raw)) {
				return nil, validationError("transcript JSON field is invalid")
			}
		}
		if entry.Tokens < 0 || entry.Tokens > MaxTranscriptEntryTokens {
			return nil, validationError("transcript tokens out of range")
		}
		if totalTokens > MaxCompletionTokens-entry.Tokens {
			return nil, validationError("completion tokens out of range")
		}
		totalTokens += entry.Tokens
		if err := validateTranscriptRoleFields(*entry); err != nil {
			return nil, err
		}
	}
	if totalBytes > int64(MaxTranscriptTotalBytes) {
		return nil, validationError("transcript payload too large")
	}
	return canonical, nil
}

func TranscriptEntryDigest(entry TranscriptEntry) (string, error) {
	_, _, digest, err := TranscriptEntryPayload(entry)
	return digest, err
}

// TranscriptEntryPayload returns the canonical retained projection and its
// digest. The payload is independently durable from the projected message.
func TranscriptEntryPayload(entry TranscriptEntry) (TranscriptEntry, string, string, error) {
	entries, err := ValidateTranscriptEntries([]TranscriptEntry{entry}, entry.Ordinal)
	if err != nil {
		return TranscriptEntry{}, "", "", err
	}
	encoded, err := json.Marshal(entries[0])
	if err != nil {
		return TranscriptEntry{}, "", "", validationError("encoding transcript entry")
	}
	if len(encoded) > MaxTranscriptEntryJSONBytes {
		return TranscriptEntry{}, "", "", validationError("encoded transcript entry too large")
	}
	payload := string(encoded)
	return entries[0], payload, hashParts("transcript-entry", payload), nil
}

// DecodeTranscriptEntryPayload accepts only the exact canonical JSON form.
// Unknown, duplicate, reordered, or non-canonical fields therefore fail
// closed rather than changing the meaning of a retained mapping.
func DecodeTranscriptEntryPayload(payload string) (TranscriptEntry, string, error) {
	if len(payload) == 0 || len(payload) > MaxTranscriptEntryJSONBytes || !utf8.ValidString(payload) {
		return TranscriptEntry{}, "", validationError("retained transcript payload is invalid")
	}
	var entry TranscriptEntry
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return TranscriptEntry{}, "", validationError("retained transcript payload is invalid")
	}
	canonical, encoded, digest, err := TranscriptEntryPayload(entry)
	if err != nil {
		return TranscriptEntry{}, "", err
	}
	if encoded != payload {
		return TranscriptEntry{}, "", validationError("retained transcript payload is non-canonical")
	}
	return canonical, digest, nil
}

func CompletionDigest(value Completion, entries []TranscriptEntry, nextOrdinal int) (Completion, []TranscriptEntry, string, error) {
	canonicalCompletion, err := NormalizeCompletion(value)
	if err != nil {
		return Completion{}, nil, "", err
	}
	canonicalEntries, err := ValidateTranscriptEntries(entries, nextOrdinal)
	if err != nil {
		return Completion{}, nil, "", err
	}
	payload := struct {
		Completion Completion        `json:"completion"`
		Transcript []TranscriptEntry `json:"transcript"`
	}{canonicalCompletion, canonicalEntries}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, nil, "", validationError("encoding completion")
	}
	return canonicalCompletion, canonicalEntries, hashParts("completion", string(encoded)), nil
}

func normalizeReferences(name string, values []string) ([]string, error) {
	if len(values) > MaxOutcomeReferences {
		return nil, validationError("too many outcome references")
	}
	result := append([]string(nil), values...)
	for _, value := range result {
		if err := validateIdentifier(name, value, MaxReferenceIDBytes); err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write == 0 || result[write-1] != value {
			result[write] = value
			write++
		}
	}
	return result[:write], nil
}

func validateTranscriptRoleFields(entry TranscriptEntry) error {
	switch entry.Role {
	case "assistant":
		if entry.ToolCallID != "" || entry.Name != "" {
			return validationError("assistant transcript entry has tool response fields")
		}
	case "tool":
		if entry.ToolCallID == "" {
			return validationError("tool transcript entry requires tool call id")
		}
		if entry.ToolCalls != "" || entry.Reasoning != "" || entry.ReasoningDetails != "" {
			return validationError("tool transcript entry has assistant-only fields")
		}
	case "user", "system":
		if entry.ToolCalls != "" || entry.ToolCallID != "" || entry.Name != "" ||
			entry.Reasoning != "" || entry.ReasoningDetails != "" {
			return validationError("transcript role has incompatible fields")
		}
	}
	return nil
}

func validateIdentifier(name, value string, limit int) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > limit || !utf8.ValidString(value) || !identifierRE.MatchString(value) {
		return validationError(name + " is invalid")
	}
	return nil
}

func validateCode(name, value string, limit int) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > limit || !codeRE.MatchString(value) {
		return validationError(name + " is invalid")
	}
	return nil
}

func validateBody(name, value string, limit int) error {
	if len(value) > limit || !utf8.ValidString(value) {
		return validationError(name + " is invalid")
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return validationError(name + " contains unsupported control characters")
		}
	}
	return nil
}
