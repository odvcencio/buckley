package dapr

import (
	"testing"

	"m31labs.dev/buckley/pkg/goalloop"
)

func TestResolveEndpoint_Precedence(t *testing.T) {
	t.Setenv("DAPR_GRPC_ENDPOINT", "remote:4001")
	t.Setenv("DAPR_GRPC_PORT", "4002")
	if got := ResolveEndpoint("explicit:4000"); got != "explicit:4000" {
		t.Fatalf("explicit endpoint = %s", got)
	}
	if got := ResolveEndpoint(""); got != "remote:4001" {
		t.Fatalf("env endpoint = %s", got)
	}
	t.Setenv("DAPR_GRPC_ENDPOINT", "")
	if got := ResolveEndpoint(""); got != "localhost:4002" {
		t.Fatalf("port endpoint = %s", got)
	}
	t.Setenv("DAPR_GRPC_PORT", "")
	if got := ResolveEndpoint(""); got != DefaultEndpoint {
		t.Fatalf("default endpoint = %s", got)
	}
}

func TestNextTurnIdentity(t *testing.T) {
	cases := []struct {
		kind           goalloop.StepKind
		gen, idx       int
		wantGen, wanti int
	}{
		{goalloop.StepContinue, 0, 3, 0, 4},
		{goalloop.StepVerify, 1, 0, 1, 1},
		{goalloop.StepCheckpoint, 1, 5, 2, 0},
	}
	for _, tc := range cases {
		gen, idx := nextTurnIdentity(tc.kind, tc.gen, tc.idx)
		if gen != tc.wantGen || idx != tc.wanti {
			t.Fatalf("nextTurnIdentity(%s, %d, %d) = (%d, %d), want (%d, %d)", tc.kind, tc.gen, tc.idx, gen, idx, tc.wantGen, tc.wanti)
		}
	}
}

func TestTurnDone(t *testing.T) {
	running := []goalloop.StepKind{goalloop.StepContinue, goalloop.StepVerify, goalloop.StepCheckpoint}
	for _, kind := range running {
		if turnDone(kind) {
			t.Fatalf("turnDone(%s) = true, want false", kind)
		}
	}
	done := []goalloop.StepKind{goalloop.StepCompleted, goalloop.StepBlocked, goalloop.StepPark, goalloop.StepYield}
	for _, kind := range done {
		if !turnDone(kind) {
			t.Fatalf("turnDone(%s) = false, want true", kind)
		}
	}
}

func TestDeferredTasks_SortedAndBounded(t *testing.T) {
	yields := map[string]int{"task-c": 2, "task-a": 3, "task-b": 1}
	got := deferredTasks(yields, 2)
	if len(got) != 2 || got[0] != "task-a" || got[1] != "task-c" {
		t.Fatalf("deferredTasks = %v, want [task-a task-c]", got)
	}
}
