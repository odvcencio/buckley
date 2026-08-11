package runledger

import (
	"context"
	"strings"
	"testing"
)

func TestSQLiteStore_ClaimsRejectPrefixOverlapAndRelease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first, err := store.StartRun(ctx, AgentRun{RunID: "run-first", SessionID: "claims"})
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	second, err := store.StartRun(ctx, AgentRun{RunID: "run-second", SessionID: "claims"})
	if err != nil {
		t.Fatalf("start second run: %v", err)
	}
	claims, err := store.AcquireClaims(ctx, first.RunID, []string{"pkg", "pkg"})
	if err != nil {
		t.Fatalf("AcquireClaims first: %v", err)
	}
	if len(claims) != 1 || claims[0].Resource != "pkg" || claims[0].RunID != first.RunID {
		t.Fatalf("first claims = %+v", claims)
	}
	if _, err := store.AcquireClaims(ctx, second.RunID, []string{"pkg/subagent"}); err == nil || !strings.Contains(err.Error(), "workspace claim conflict") {
		t.Fatalf("prefix conflict error = %v", err)
	}
	if err := store.ReleaseClaims(ctx, first.RunID, nil, "parent complete"); err != nil {
		t.Fatalf("ReleaseClaims: %v", err)
	}
	claims, err = store.AcquireClaims(ctx, second.RunID, []string{"pkg/subagent"})
	if err != nil {
		t.Fatalf("AcquireClaims after release: %v", err)
	}
	if len(claims) != 1 || claims[0].Resource != "pkg/subagent" {
		t.Fatalf("second claims = %+v", claims)
	}
	all, err := store.ListClaims(ctx, ClaimQuery{IncludeReleased: true})
	if err != nil {
		t.Fatalf("ListClaims: %v", err)
	}
	if len(all) != 2 || all[0].ReleasedAt == nil || all[0].ReleaseReason != "parent complete" {
		t.Fatalf("all claims = %+v", all)
	}
}

func TestSQLiteStore_ClaimsWorkspaceRootConflictsWithAnyPath(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.StartRun(ctx, AgentRun{RunID: "run-root", SessionID: "claims"}); err != nil {
		t.Fatalf("start root run: %v", err)
	}
	if _, err := store.StartRun(ctx, AgentRun{RunID: "run-child", SessionID: "claims"}); err != nil {
		t.Fatalf("start child run: %v", err)
	}
	if _, err := store.AcquireClaims(ctx, "run-root", []string{"."}); err != nil {
		t.Fatalf("acquire root: %v", err)
	}
	if _, err := store.AcquireClaims(ctx, "run-child", []string{"pkg/tool"}); err == nil {
		t.Fatal("expected workspace root conflict")
	}
}
