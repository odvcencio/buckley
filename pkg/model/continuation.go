package model

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrIncompatibleContinuation indicates that opaque state cannot be used with
// the provider/model selected for the current request.
var ErrIncompatibleContinuation = errors.New("incompatible provider continuation")

// ProviderContinuation carries provider-native working state between model
// turns without interpreting or exposing its contents.
type ProviderContinuation struct {
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	State      json.RawMessage `json:"state"`
}

// Compatible reports whether continuation state belongs to the provider and
// model selected for the next turn.
func (c *ProviderContinuation) Compatible(providerID, modelID string) bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.ProviderID) == strings.TrimSpace(providerID) &&
		strings.TrimSpace(c.ModelID) == strings.TrimSpace(modelID)
}

// Clone returns an independent copy suitable for handing across persistence
// and runtime boundaries.
func (c *ProviderContinuation) Clone() *ProviderContinuation {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.State = append(json.RawMessage(nil), c.State...)
	return &cloned
}

// ContinuationRequest combines a portable chat request with optional opaque
// state from an earlier turn by the same provider and model. When Continuation
// is present, Request.Messages contains only messages added since that state
// was returned.
type ContinuationRequest struct {
	Request      ChatRequest           `json:"request"`
	Continuation *ProviderContinuation `json:"continuation,omitempty"`
}

// ContinuationResponse returns the portable completion plus the opaque state
// needed to continue the provider-native reasoning window.
type ContinuationResponse struct {
	Response     *ChatResponse         `json:"response"`
	Continuation *ProviderContinuation `json:"continuation,omitempty"`
}

// ContinuationCursor tracks the portable message prefix already represented by
// provider-native state. It intentionally remains runtime-local; persistence is
// handled separately from incremental turn bookkeeping.
type ContinuationCursor struct {
	continuation *ProviderContinuation
	represented  [][sha256.Size]byte
}

// Prepare returns only messages added after the prefix represented by the
// current continuation. If request-time compaction changed that prefix, Prepare
// resets the cursor and returns the full portable request.
func (c *ContinuationCursor) Prepare(req ChatRequest) (ContinuationRequest, error) {
	if c == nil || c.continuation == nil {
		return ContinuationRequest{Request: req}, nil
	}
	if !continuationModelMatches(c.continuation.ModelID, req.Model) {
		c.Reset()
		return ContinuationRequest{Request: req}, nil
	}

	fingerprints, err := fingerprintMessages(req.Messages)
	if err != nil {
		return ContinuationRequest{}, err
	}
	if len(fingerprints) < len(c.represented) ||
		!equalMessagePrefix(fingerprints, c.represented) {
		c.Reset()
		return ContinuationRequest{Request: req}, nil
	}

	req.Messages = req.Messages[len(c.represented):]
	return ContinuationRequest{
		Request:      req,
		Continuation: c.continuation.Clone(),
	}, nil
}

// Commit records that provider state represents the request messages followed
// by the translated assistant message. Tool results added after that assistant
// message remain unsent and become the next request's incremental suffix.
func (c *ContinuationCursor) Commit(req ChatRequest, resp *ContinuationResponse) error {
	if c == nil {
		return nil
	}
	if resp == nil || resp.Response == nil || resp.Continuation == nil ||
		len(resp.Response.Choices) == 0 {
		c.Reset()
		return nil
	}

	messages := make([]Message, 0, len(req.Messages)+1)
	messages = append(messages, req.Messages...)
	messages = append(messages, portableAssistantHistoryMessage(resp.Response.Choices[0].Message))
	fingerprints, err := fingerprintMessages(messages)
	if err != nil {
		return err
	}

	c.represented = fingerprints
	c.continuation = resp.Continuation.Clone()
	return nil
}

// Reset discards runtime-local continuation state.
func (c *ContinuationCursor) Reset() {
	if c == nil {
		return
	}
	c.continuation = nil
	c.represented = nil
}

// Active reports whether the cursor currently represents provider-native
// continuation state.
func (c *ContinuationCursor) Active() bool {
	return c != nil && c.continuation != nil
}

// RepresentedMessageCount returns how many portable messages, from the start
// of the request array at the last Commit, are already represented by the
// current continuation state. Callers use this as the projection/compaction
// pin boundary (EfficientContextOptions.PinnedFromIndex) so the represented
// prefix stays byte-identical turn over turn and Prepare's fingerprint match
// keeps holding.
func (c *ContinuationCursor) RepresentedMessageCount() int {
	if c == nil {
		return 0
	}
	return len(c.represented)
}

// Continuation returns the opaque provider state currently represented by the
// cursor, or nil if the cursor holds no continuation.
func (c *ContinuationCursor) Continuation() *ProviderContinuation {
	if c == nil {
		return nil
	}
	return c.continuation.Clone()
}

// RestoreContinuationCursor seeds a cursor with previously committed
// continuation state, computing represented fingerprints from
// representedPrefix -- the exact prefix of messages that state was committed
// against. Callers use this to resume a session's continuation window after a
// process restart. If the durable transcript's prefix has drifted since the
// last commit, Prepare's existing fingerprint-mismatch safety net resets the
// cursor cleanly on first use rather than corrupting the provider request.
func RestoreContinuationCursor(continuation *ProviderContinuation, representedPrefix []Message) (*ContinuationCursor, error) {
	if continuation == nil {
		return &ContinuationCursor{}, nil
	}
	fingerprints, err := fingerprintMessages(representedPrefix)
	if err != nil {
		return nil, err
	}
	return &ContinuationCursor{
		continuation: continuation.Clone(),
		represented:  fingerprints,
	}, nil
}

func portableAssistantHistoryMessage(message Message) Message {
	if content, ok := message.Content.(string); ok && content == "" {
		message.Content = nil
	}
	if message.Content == nil && len(message.ToolCalls) == 0 &&
		strings.TrimSpace(message.Reasoning) != "" {
		message.Content = message.Reasoning
	}
	return message
}

func continuationModelMatches(stateModel, requestModel string) bool {
	stateModel = strings.TrimSpace(stateModel)
	requestModel = strings.TrimSpace(requestModel)
	if stateModel == requestModel {
		return true
	}
	return strings.HasSuffix(stateModel, "/"+requestModel) ||
		strings.HasSuffix(requestModel, "/"+stateModel)
}

func fingerprintMessages(messages []Message) ([][sha256.Size]byte, error) {
	fingerprints := make([][sha256.Size]byte, len(messages))
	for i, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, fmt.Errorf("fingerprinting continuation message %d: %w", i, err)
		}
		fingerprints[i] = sha256.Sum256(encoded)
	}
	return fingerprints, nil
}

func equalMessagePrefix(messages, prefix [][sha256.Size]byte) bool {
	for i := range prefix {
		if messages[i] != prefix[i] {
			return false
		}
	}
	return true
}
