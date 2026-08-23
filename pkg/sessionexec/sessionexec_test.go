package sessionexec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validAcceptRequest() AcceptRequest {
	return AcceptRequest{
		SessionID: "session-01", CommandID: "command-01", Type: "input",
		Content: "hello\nworld", AcceptedBy: "operator@example.test",
	}
}

func TestIdentityHelpers_StableOpaqueAndGenerationBound(t *testing.T) {
	run := RunIDForSession("session-01")
	if run != RunIDForSession("session-01") || run == RunIDForSession("session-02") {
		t.Fatalf("run identity is not stable and session-bound: %q", run)
	}
	if strings.Contains(run, "session-01") || len(run) > MaxRunIDBytes {
		t.Fatalf("run identity exposed input or exceeded bound: %q", run)
	}
	turn0 := TurnID("command-01", 0)
	if turn0 != TurnID("command-01", 0) || turn0 == TurnID("command-01", 1) || len(turn0) > MaxTurnIDBytes {
		t.Fatalf("turn identity is not stable and generation-bound: %q", turn0)
	}
	if id := NewCommandID(); ValidateCommandID(id) != nil || len(id) > MaxCommandIDBytes {
		t.Fatalf("generated command id is invalid: %q", id)
	}
}

func TestInputDigest_BindsCanonicalAcceptanceTuple(t *testing.T) {
	req := validAcceptRequest()
	identity := Identity{
		SessionID: req.SessionID, RunID: RunIDForSession(req.SessionID), TaskID: ForegroundTaskID,
		CommandID: req.CommandID, TurnID: TurnID(req.CommandID, 0), Generation: 0, Sequence: 1,
	}
	digest, err := InputDigest(req, identity, LaneWork, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d", len(digest))
	}
	changed := req
	changed.AcceptedBy = "other@example.test"
	other, err := InputDigest(changed, identity, LaneWork, "")
	if err != nil {
		t.Fatal(err)
	}
	if digest == other {
		t.Fatal("principal drift did not change digest")
	}
	withTarget, err := InputDigest(req, identity, LaneWork, "command-target")
	if err != nil {
		t.Fatal(err)
	}
	if digest == withTarget {
		t.Fatal("target drift did not change digest")
	}
	identity.Sequence++
	withSequence, err := InputDigest(req, identity, LaneWork, "")
	if err != nil {
		t.Fatal(err)
	}
	if digest == withSequence {
		t.Fatal("sequence drift did not change digest")
	}
}

func TestLaneFor_BoundedVocabulary(t *testing.T) {
	for _, commandType := range []string{"input", "queue", "steer", "model", "slash"} {
		lane, err := LaneFor(commandType)
		if err != nil || lane != LaneWork {
			t.Fatalf("LaneFor(%q) = %q, %v", commandType, lane, err)
		}
	}
	for _, commandType := range []string{"interrupt", "approval", "pause", "resume"} {
		lane, err := LaneFor(commandType)
		if err != nil || lane != LaneControl {
			t.Fatalf("LaneFor(%q) = %q, %v", commandType, lane, err)
		}
	}
	if _, err := LaneFor("execute-arbitrary"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown lane error = %v", err)
	}
}

func TestValidateAcceptRequest_RejectsBoundsUTF8AndControls(t *testing.T) {
	tests := []AcceptRequest{
		{SessionID: "", CommandID: "command-01", Type: "input", Content: "x", AcceptedBy: "operator"},
		{SessionID: "session-01", CommandID: "command 01", Type: "input", Content: "x", AcceptedBy: "operator"},
		{SessionID: "session-01", CommandID: "command-01", Type: "input", Content: string([]byte{0xff}), AcceptedBy: "operator"},
		{SessionID: "session-01", CommandID: "command-01", Type: "input", Content: "x\x00y", AcceptedBy: "operator"},
		{SessionID: "session-01", CommandID: "command-01", Type: "input", Content: strings.Repeat("x", MaxContentBytes+1), AcceptedBy: "operator"},
		{SessionID: "session-01", CommandID: "command-01", Type: "input", Content: "x", AcceptedBy: ""},
	}
	for i, req := range tests {
		if err := ValidateAcceptRequest(req, req.CommandID); !errors.Is(err, ErrValidation) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
}

func TestCompletionDigest_CanonicalBoundedOutcome(t *testing.T) {
	first := Completion{
		State:   StateSucceeded,
		Outcome: Outcome{Code: "ok", EvidenceIDs: []string{"ev-b", "ev-a", "ev-a"}, ArtifactIDs: []string{"art-b", "art-a"}},
	}
	second := Completion{
		State:   StateSucceeded,
		Outcome: Outcome{Code: "ok", EvidenceIDs: []string{"ev-a", "ev-b"}, ArtifactIDs: []string{"art-a", "art-b"}},
	}
	canonical, _, digest1, err := CompletionDigest(first, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, digest2, err := CompletionDigest(second, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if digest1 != digest2 || len(canonical.Outcome.EvidenceIDs) != 2 {
		t.Fatalf("completion was not canonical: %#v %q %q", canonical, digest1, digest2)
	}
	tooMany := make([]string, MaxOutcomeReferences+1)
	for i := range tooMany {
		tooMany[i] = "ev-x"
	}
	if _, err := NormalizeCompletion(Completion{State: StateSucceeded, Outcome: Outcome{EvidenceIDs: tooMany}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized outcome error = %v", err)
	}
}

func TestTranscriptValidation_ContiguousCanonicalAndBounded(t *testing.T) {
	entries := []TranscriptEntry{{Ordinal: 1, Role: "assistant", Content: "ok", Tokens: 2}}
	canonical, err := ValidateTranscriptEntries(entries, 1)
	if err != nil {
		t.Fatal(err)
	}
	if canonical[0].ContentType != "text" {
		t.Fatalf("default content type = %q", canonical[0].ContentType)
	}
	if _, err := ValidateTranscriptEntries(entries, 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("ordinal error = %v", err)
	}
	entries[0].ContentJSON = "{bad"
	if _, err := ValidateTranscriptEntries(entries, 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("JSON error = %v", err)
	}
}

func TestTranscriptValidation_TokenBoundsAndCheckedAggregate(t *testing.T) {
	entry := TranscriptEntry{Ordinal: 0, Role: "assistant", Content: "ok", Tokens: MaxTranscriptEntryTokens}
	if _, err := ValidateTranscriptEntries([]TranscriptEntry{entry}, 0); err != nil {
		t.Fatalf("exact per-entry maximum: %v", err)
	}
	entry.Tokens++
	if _, err := ValidateTranscriptEntries([]TranscriptEntry{entry}, 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("per-entry maximum+1 error = %v", err)
	}

	exact := make([]TranscriptEntry, MaxCompletionTokens/MaxTranscriptEntryTokens)
	for i := range exact {
		exact[i] = TranscriptEntry{Ordinal: i, Role: "assistant", Content: "ok", Tokens: MaxTranscriptEntryTokens}
	}
	if _, err := ValidateTranscriptEntries(exact, 0); err != nil {
		t.Fatalf("exact completion maximum: %v", err)
	}
	over := append(append([]TranscriptEntry(nil), exact...), TranscriptEntry{
		Ordinal: len(exact), Role: "assistant", Content: "over", Tokens: 1,
	})
	if _, err := ValidateTranscriptEntries(over, 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("completion maximum+1 error = %v", err)
	}
}

func TestTranscriptValidation_RoleFieldCoherence(t *testing.T) {
	valid := []TranscriptEntry{
		{Ordinal: 0, Role: "user", Content: "question"},
		{Ordinal: 1, Role: "assistant", Content: "checking", ToolCalls: `[]`, Reasoning: "why", ReasoningDetails: `[]`},
		{Ordinal: 2, Role: "tool", Content: "result", ToolCallID: "call-1", Name: "read_file"},
		{Ordinal: 3, Role: "system", Content: "summary", IsSummary: true},
	}
	if _, err := ValidateTranscriptEntries(valid, 0); err != nil {
		t.Fatalf("current conversation shapes rejected: %v", err)
	}
	invalid := []TranscriptEntry{
		{Ordinal: 0, Role: "tool", Content: "missing id"},
		{Ordinal: 0, Role: "user", Content: "x", ToolCalls: `[]`},
		{Ordinal: 0, Role: "system", Content: "x", Reasoning: "private"},
		{Ordinal: 0, Role: "assistant", Content: "x", ToolCallID: "call-1"},
		{Ordinal: 0, Role: "tool", Content: "x", ToolCallID: "call-1", ReasoningDetails: `[]`},
	}
	for i, entry := range invalid {
		if _, err := ValidateTranscriptEntries([]TranscriptEntry{entry}, 0); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid role case %d error = %v", i, err)
		}
	}
}

func TestTranscriptEntryPayload_CanonicalAndSelfAuthenticating(t *testing.T) {
	entry := TranscriptEntry{Ordinal: 3, Role: "assistant", Content: "answer", Tokens: 2}
	canonical, payload, digest, err := TranscriptEntryPayload(entry)
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedDigest, err := DecodeTranscriptEntryPayload(payload)
	if err != nil || decoded != canonical || decodedDigest != digest {
		t.Fatalf("decoded payload = %#v %q, %v", decoded, decodedDigest, err)
	}
	if _, _, err := DecodeTranscriptEntryPayload(" \n" + payload); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-canonical payload error = %v", err)
	}
}

func TestLeaseValidation_RejectsCallerEdgeDurations(t *testing.T) {
	if err := ValidateLeaseDuration(time.Nanosecond); !errors.Is(err, ErrValidation) {
		t.Fatalf("sub-millisecond duration error = %v", err)
	}
	if err := ValidateLeaseDuration(MaxLeaseDuration + time.Millisecond); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized duration error = %v", err)
	}
}

func TestEffectPermitValidation_StateAndResolutionBounds(t *testing.T) {
	created := time.Now().UTC()
	expires := created.Add(time.Minute)
	ambiguous := created.Add(time.Second)
	ended := created.Add(2 * time.Second)
	resolved := expires.Add(time.Second)
	base := EffectPermit{
		EffectRequest: EffectRequest{
			Lease:    LeaseRef{SessionID: "session-effect", CommandID: "command-effect", Generation: 0, Owner: "worker", LeaseGeneration: 1},
			EffectID: "step-effect", Kind: EffectKindModel,
		},
		State: EffectStateActive, CreatedAt: created, ExpiresAt: expires,
	}
	valid := []EffectPermit{
		base,
		func() EffectPermit {
			value := base
			value.State = EffectStateAmbiguous
			value.AmbiguousAt = &ambiguous
			return value
		}(),
		func() EffectPermit {
			value := base
			value.State = EffectStateEnded
			value.EndedAt = &ended
			return value
		}(),
		func() EffectPermit {
			value := base
			value.State, value.AmbiguousAt, value.ResolvedAt, value.ResolvedBy = EffectStateResolved, &ambiguous, &resolved, "operator"
			value.ResolutionReason = "reviewed"
			return value
		}(),
	}
	for index, permit := range valid {
		if err := ValidateEffectPermit(permit); err != nil {
			t.Fatalf("valid permit %d: %v", index, err)
		}
	}
	invalid := base
	invalid.State = EffectStateAmbiguous
	if err := ValidateEffectPermit(invalid); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing ambiguous timestamp = %v", err)
	}
	invalid = base
	invalid.State, invalid.EndedAt, invalid.ResolvedBy = EffectStateEnded, &ended, "unexpected"
	if err := ValidateEffectPermit(invalid); !errors.Is(err, ErrValidation) {
		t.Fatalf("ended resolution fields = %v", err)
	}
	if err := ValidateEffectResolutionRequest(EffectResolutionRequest{
		SessionID: "session-effect", CommandID: "command-effect", Generation: 0,
		EffectID: "step-effect", Actor: "operator", Reason: strings.Repeat("x", MaxEffectResolutionReasonBytes+1),
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized resolution reason = %v", err)
	}
	if err := ValidateEffectResolutionRequest(EffectResolutionRequest{
		SessionID: "session-effect", CommandID: "command-effect", Generation: 0,
		EffectID: "step-effect", Actor: "operator", Reason: " \t",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty resolution reason = %v", err)
	}
}

func TestEffectResolver_IsSeparateFromWorkerJournal(t *testing.T) {
	journal := reflect.TypeOf((*Journal)(nil)).Elem()
	if _, exposed := journal.MethodByName("ResolveAmbiguousEffect"); exposed {
		t.Fatal("ordinary worker Journal exposes privileged effect resolution")
	}
	resolver := reflect.TypeOf((*EffectResolver)(nil)).Elem()
	if _, exposed := resolver.MethodByName("ResolveAmbiguousEffect"); !exposed {
		t.Fatal("EffectResolver is missing its privileged operation")
	}
}
