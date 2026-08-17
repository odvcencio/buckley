package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/durability/modelstep"
)

func TestACPLifecycleLineageIsStableAndTurnsAreUniquePerPrompt(t *testing.T) {
	firstLineage := acpLifecycleSessionID("editor-session-1")
	if firstLineage == "" || firstLineage != acpLifecycleSessionID("editor-session-1") {
		t.Fatalf("session lineage is not stable: %q", firstLineage)
	}
	secondLineage := acpLifecycleSessionID("editor-session-2")
	if secondLineage == firstLineage {
		t.Fatalf("concurrent session lineages collided: %q", firstLineage)
	}

	firstSession := &acpSessionState{lifecycleSessionID: firstLineage}
	firstTurn := firstSession.nextLifecycleTurnID()
	secondTurn := firstSession.nextLifecycleTurnID()
	if firstTurn == "" || secondTurn == "" || firstTurn == secondTurn {
		t.Fatalf("turn generations = %q, %q, want unique non-empty IDs", firstTurn, secondTurn)
	}

	secondSession := &acpSessionState{lifecycleSessionID: secondLineage}
	if otherTurn := secondSession.nextLifecycleTurnID(); otherTurn == firstTurn || otherTurn == secondTurn {
		t.Fatalf("session-scoped turn ID collided: %q", otherTurn)
	}
}

func TestACPLifecycleTurnIDNilStateIsSafe(t *testing.T) {
	var state *acpSessionState
	if got := state.nextLifecycleTurnID(); got != "" {
		t.Fatalf("nil state turn ID = %q, want empty", got)
	}
}

func TestACPProjectedErrorBoundsDisplayAndPreservesRawCause(t *testing.T) {
	secret := "sk-" + strings.Repeat("z", 30)
	raw := errors.New("provider rejected request " + secret + " " + strings.Repeat("x", modelstep.MaxPersistedErrorRunes+100))
	projected := newACPProjectedError(raw)
	if !errors.Is(projected, raw) {
		t.Fatal("projected ACP error lost raw internal cause")
	}
	if strings.Contains(projected.Error(), secret) || len([]rune(projected.Error())) > modelstep.MaxPersistedErrorRunes {
		t.Fatalf("unsafe ACP projection: %q", projected.Error())
	}
	if projected.Error() != modelstep.NormalizeError(raw) {
		t.Fatalf("ACP projection = %q, want canonical %q", projected.Error(), modelstep.NormalizeError(raw))
	}
}

func TestLogACPProjectedPromptErrorExcludesRawProviderCause(t *testing.T) {
	secret := "sk-" + strings.Repeat("q", 30)
	tail := "RAW_PROVIDER_TAIL_MUST_NOT_LOG"
	raw := errors.New("provider rejected request " + secret + " " + strings.Repeat("x", modelstep.MaxPersistedErrorRunes+100) + tail)
	projected := newACPProjectedError(raw)
	var logged string
	logACPProjectedPromptError(func(format string, args ...interface{}) {
		logged = fmt.Sprintf(format, args...)
	}, projected)

	if !strings.Contains(raw.Error(), secret) || !strings.Contains(raw.Error(), tail) {
		t.Fatalf("test provider cause is not raw: %q", raw)
	}
	if strings.Contains(logged, secret) || strings.Contains(logged, tail) || strings.Contains(logged, raw.Error()) {
		t.Fatalf("debug log exposed raw provider cause: %q", logged)
	}
	if !strings.Contains(logged, projected.Error()) || len([]rune(logged)) > len([]rune("prompt error: "))+modelstep.MaxPersistedErrorRunes {
		t.Fatalf("debug log is not the bounded canonical projection: %q", logged)
	}
}
