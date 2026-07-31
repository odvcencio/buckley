package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ContinuationStore persists provider-native continuation state so a session
// can resume its provider continuation window after a process restart. It
// mirrors ProviderThreadStore's (session, provider) keying, extended with
// model because continuation invalidates on model change too (Compatible()).
type ContinuationStore interface {
	LoadProviderContinuation(sessionID, providerID, modelID string) (string, error)
	SaveProviderContinuation(sessionID, providerID, modelID, state string) error
	DeleteProviderContinuation(sessionID, providerID string) error
}

// persistedContinuation is the durable envelope for a ContinuationCursor: the
// opaque provider state plus how many portable messages it represents.
// Restoring both lets a resumed cursor re-derive its represented fingerprints
// from the current durable transcript.
type persistedContinuation struct {
	Continuation     ProviderContinuation `json:"continuation"`
	RepresentedCount int                  `json:"represented_count"`
}

// ContinuationCoordinator manages one session's ContinuationCursor lifecycle
// end to end: restoring persisted state, exposing the projection pin
// boundary, running one continuation-aware turn, persisting the result, and
// applying the epoch rule (decision 0001) on Reset. Callers (the TUI tool
// loop and the headless runner) share this so continuation wiring is not
// duplicated per presentation layer.
type ContinuationCoordinator struct {
	manager   *Manager
	store     ContinuationStore
	sessionID string

	cursor *ContinuationCursor
	hit    bool
}

// NewContinuationCoordinator creates a coordinator for one session. store may
// be nil to disable persistence (runtime-only cursor for that process).
func NewContinuationCoordinator(manager *Manager, store ContinuationStore, sessionID string) *ContinuationCoordinator {
	return &ContinuationCoordinator{
		manager:   manager,
		store:     store,
		sessionID: strings.TrimSpace(sessionID),
		cursor:    &ContinuationCursor{},
	}
}

// Eligible reports whether the coordinator's manager resolves modelID to a
// provider that supports native continuation.
func (cc *ContinuationCoordinator) Eligible(modelID string) bool {
	if cc == nil || cc.manager == nil {
		return false
	}
	return cc.manager.SupportsContinuation(modelID)
}

// Restore loads persisted continuation state for (providerID, modelID) and
// seeds the cursor, re-deriving represented fingerprints from
// currentMessages (the session's full portable transcript). It is a no-op if
// the cursor already holds state, the store is unset, no state is persisted,
// the persisted state belongs to a different provider/model (decision 0001
// invalidation), or the persisted prefix no longer fits the transcript.
func (cc *ContinuationCoordinator) Restore(providerID, modelID string, currentMessages []Message) {
	if cc == nil || cc.store == nil || cc.sessionID == "" || cc.cursor.Active() {
		return
	}
	raw, err := cc.store.LoadProviderContinuation(cc.sessionID, providerID, modelID)
	if err != nil || strings.TrimSpace(raw) == "" {
		return
	}
	var persisted persistedContinuation
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return
	}
	if !persisted.Continuation.Compatible(providerID, modelID) {
		return
	}
	if persisted.RepresentedCount < 0 || persisted.RepresentedCount > len(currentMessages) {
		return
	}
	restored, err := RestoreContinuationCursor(&persisted.Continuation, currentMessages[:persisted.RepresentedCount])
	if err != nil {
		return
	}
	cc.cursor = restored
}

// PinnedFromIndex returns the request-array index projection/compaction must
// leave untouched, or 0 when no continuation window is active.
func (cc *ContinuationCoordinator) PinnedFromIndex() int {
	if cc == nil {
		return 0
	}
	return cc.cursor.RepresentedMessageCount()
}

// Active reports whether the coordinator currently holds continuation state.
func (cc *ContinuationCoordinator) Active() bool {
	return cc != nil && cc.cursor.Active()
}

// Hit reports whether the most recent Call reused the represented prefix
// (an incremental request) rather than recompiling the full request.
func (cc *ContinuationCoordinator) Hit() bool {
	return cc != nil && cc.hit
}

// Call executes one continuation-aware turn against req: Prepare, call the
// provider, Commit, persist. req.Messages should already be projected with
// PinnedFromIndex() as the pin boundary. On any error it Resets the cursor
// (and any persisted state) so the caller can retry through the normal
// ChatCompletion path -- a broken continuation never fails the turn.
func (cc *ContinuationCoordinator) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if cc == nil || cc.manager == nil {
		return nil, fmt.Errorf("continuation coordinator unavailable")
	}
	prepared, err := cc.cursor.Prepare(req)
	if err != nil {
		cc.Reset()
		return nil, err
	}
	cc.hit = prepared.Continuation != nil

	resp, err := cc.manager.ChatCompletionWithContinuation(ctx, prepared)
	if err != nil {
		cc.Reset()
		return nil, err
	}
	if err := cc.cursor.Commit(prepared.Request, resp); err != nil {
		cc.Reset()
		return nil, err
	}
	cc.persist()
	return resp.Response, nil
}

func (cc *ContinuationCoordinator) persist() {
	if cc == nil || cc.store == nil || cc.sessionID == "" {
		return
	}
	continuation := cc.cursor.Continuation()
	if continuation == nil {
		return
	}
	envelope := persistedContinuation{
		Continuation:     *continuation,
		RepresentedCount: cc.cursor.RepresentedMessageCount(),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	_ = cc.store.SaveProviderContinuation(cc.sessionID, continuation.ProviderID, continuation.ModelID, string(encoded))
}

// Reset discards runtime cursor state and deletes any persisted state for
// this session/provider, forcing the next turn to recompile the full
// request. Callers use this for the epoch rule (decision 0001): reset
// deliberately before a turn that would otherwise need to compact inside the
// represented region, rather than letting a fingerprint mismatch discover it
// mid-turn.
func (cc *ContinuationCoordinator) Reset() {
	if cc == nil {
		return
	}
	var providerID string
	if continuation := cc.cursor.Continuation(); continuation != nil {
		providerID = continuation.ProviderID
	}
	cc.cursor.Reset()
	cc.hit = false
	if cc.store != nil && cc.sessionID != "" && providerID != "" {
		_ = cc.store.DeleteProviderContinuation(cc.sessionID, providerID)
	}
}
