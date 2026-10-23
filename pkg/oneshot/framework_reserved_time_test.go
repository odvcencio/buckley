package oneshot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunAgent_ReservesTimeWithoutStarvingPrimary(t *testing.T) {
	for _, tt := range []struct {
		name             string
		timeout, reserve time.Duration
	}{
		{"reserve exceeds command", 30 * time.Second, 70 * time.Second},
		{"reserve exceeds half", 2 * time.Minute, 70 * time.Second},
		{"ample time", 10 * time.Minute, 80 * time.Second},
		{"no reserve", 30 * time.Second, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedAgentExecutor{responses: []string{"approve", "approve"}}
			framework := NewFramework(nil, nil).WithAgentRunner(runner)
			started := time.Now()
			deadline := started.Add(tt.timeout)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			t.Cleanup(cancel)
			result, err := framework.RunAgent(ctx, criticAgentDefinition{}, AgentRunOpts{MaxRetries: 1, ApprovalCriticReserve: tt.reserve})
			if err != nil {
				t.Fatal(err)
			}
			if result.PrimaryAttempts != 1 || result.CriticAttempts != 1 || len(runner.deadlines) != 2 {
				t.Fatalf("both review phases did not execute: result=%+v deadlines=%v", result, runner.deadlines)
			}
			primary := runner.deadlines[0]
			if !primary.After(started) || primary.After(deadline) {
				t.Fatalf("primary deadline %s outside live command window %s..%s", primary, started, deadline)
			}
			reserve := min(tt.reserve, tt.timeout/2)
			if delta := primary.Sub(deadline.Add(-reserve)); delta < -100*time.Millisecond || delta > 100*time.Millisecond {
				t.Fatalf("primary deadline differs from bounded reserve by %s", delta)
			}
			if !runner.deadlines[1].Equal(deadline) {
				t.Fatalf("critic deadline %s extended or shortened parent %s", runner.deadlines[1], deadline)
			}
		})
	}
}

func TestContextWithReservedTime_PreservesCancellation(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"no deadline", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{"live deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 30*time.Second)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancelParent := tt.ctx()
			t.Cleanup(cancelParent)
			child, cancelChild := contextWithReservedTime(parent, 70*time.Second)
			t.Cleanup(cancelChild)
			if err := child.Err(); err != nil {
				t.Fatalf("reserve canceled live work: %v", err)
			}
			cancelChild()
			if !errors.Is(child.Err(), context.Canceled) || parent.Err() != nil {
				t.Fatal("canceling phase did not preserve parent for critic")
			}
			child, cancelChild = contextWithReservedTime(parent, 70*time.Second)
			t.Cleanup(cancelChild)
			cancelParent()
			if !errors.Is(child.Err(), context.Canceled) {
				t.Fatal("parent cancellation did not reach reserved phase")
			}
		})
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	child, stop := contextWithReservedTime(ctx, 70*time.Second)
	t.Cleanup(stop)
	if !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatal("expired parent became live")
	}
	before, _ := ctx.Deadline()
	after, _ := child.Deadline()
	if !before.Equal(after) {
		t.Fatal("expired parent deadline changed")
	}
}
