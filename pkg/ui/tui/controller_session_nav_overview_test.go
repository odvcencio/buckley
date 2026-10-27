package tui

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/runledger"
)

type overviewOnlyLedger struct {
	*runledger.SQLiteStore
}

func (s overviewOnlyLedger) ListEvents(context.Context, runledger.EventQuery) ([]runledger.Event, error) {
	return nil, fmt.Errorf("navigation must not load event bodies")
}

func (s overviewOnlyLedger) ListRuns(context.Context, runledger.RunQuery) ([]runledger.AgentRun, error) {
	return nil, fmt.Errorf("navigation must not load historical run bodies")
}

func TestBuildRunTreeNav_OverviewMatchesFallback(t *testing.T) {
	store, runID := sessionNavHistory(t, 10)
	ctx := context.Background()
	fastNodes, fastRuns, ok := buildRunTreeNav(ctx, overviewOnlyLedger{store}, "session-history")
	if !ok {
		t.Fatal("overview could not load navigation without full history reads")
	}
	// An adapter exposing only the existing Store port retains compatibility.
	fallback := struct{ runledger.Store }{store}
	fullNodes, fullRuns, ok := buildRunTreeNav(ctx, fallback, "session-history")
	if !ok || !reflect.DeepEqual(fastNodes, fullNodes) {
		t.Fatalf("overview changed navigator rows: %v / %v", fastNodes, fullNodes)
	}
	if !reflect.DeepEqual(runNodeToActivityRecord(fastRuns[runID]), runNodeToActivityRecord(fullRuns[runID])) {
		t.Fatal("overview changed the selected run's inspector record")
	}
}
