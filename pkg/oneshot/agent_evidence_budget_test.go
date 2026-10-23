package oneshot

import (
	"context"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

// slowEvidenceExecutor models a verification suite that would run past the
// whole review deadline: the collector returns only when its context ends, as
// the real collector does (it records an INCONCLUSIVE call). The model run
// fails if its context has already ended.
type slowEvidenceExecutor struct {
	*scriptedAgentExecutor
	modelContextErr error
	modelRan        bool
}

func (s *slowEvidenceExecutor) CollectAgentEvidence(ctx context.Context, _ []AgentEvidenceRequest, _ AgentExecutionOpts) ([]AgentToolCall, error) {
	<-ctx.Done()
	return []AgentToolCall{{
		ID: "host-evidence-1", Name: "run_verification",
		Result: "status: UNAVAILABLE error: verification timed out",
	}}, nil
}

func (s *slowEvidenceExecutor) Run(ctx context.Context, system, task string, tools []string, opts AgentExecutionOpts) (*AgentResult, error) {
	s.modelRan = true
	s.modelContextErr = ctx.Err()
	if s.modelContextErr != nil {
		return nil, s.modelContextErr
	}
	return s.scriptedAgentExecutor.Run(ctx, system, task, tools, opts)
}

// tolerantEvidenceDefinition plans one verification but accepts a review whose
// verification came back inconclusive, as a real review does.
type tolerantEvidenceDefinition struct{ plannedEvidenceAgentDefinition }

func (tolerantEvidenceDefinition) ValidateAgentExecution(any, *AgentResult) error { return nil }

func TestRunAgentSlowHostEvidenceDoesNotStarveModel(t *testing.T) {
	runner := &slowEvidenceExecutor{scriptedAgentExecutor: &scriptedAgentExecutor{responses: []string{"valid"}}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	result, err := framework.RunAgent(ctx, tolerantEvidenceDefinition{}, AgentRunOpts{
		UserPrompt:           "review this exact diff",
		MaxRetries:           1,
		MaxVerificationCalls: 1,
		ReviewSnapshot:       testReviewSnapshot(t, model.ReviewSnapshotHead, strings.Repeat("a", 40)),
		SnapshotPolicy:       model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead},
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v (model ran=%v, model ctx err=%v)", err, runner.modelRan, runner.modelContextErr)
	}
	if !runner.modelRan || result.Attempts != 1 {
		t.Fatalf("model ran=%v attempts=%d, want one model attempt", runner.modelRan, result.Attempts)
	}
}

func TestRunAgentHostEvidenceTimeoutOptionBoundsCollection(t *testing.T) {
	runner := &slowEvidenceExecutor{scriptedAgentExecutor: &scriptedAgentExecutor{responses: []string{"valid"}}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	start := time.Now()
	_, err := framework.RunAgent(ctx, tolerantEvidenceDefinition{}, AgentRunOpts{
		UserPrompt:           "review this exact diff",
		MaxRetries:           1,
		MaxVerificationCalls: 1,
		HostEvidenceTimeout:  200 * time.Millisecond,
		ReviewSnapshot:       testReviewSnapshot(t, model.ReviewSnapshotHead, strings.Repeat("a", 40)),
		SnapshotPolicy:       model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead},
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("collection took %s, want about the 200ms evidence limit", elapsed)
	}
}

func TestHostEvidenceDeadline(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name      string
		remaining time.Duration // 0 = no deadline
		explicit  time.Duration
		want      time.Duration // 0 = no limit
	}{
		{"no deadline, no limit", 0, 0, 0},
		{"no deadline, explicit", 0, 30 * time.Second, 30 * time.Second},
		{"default reserves half for the model", 4 * time.Minute, 0, 2 * time.Minute},
		{"explicit within the reserve", 4 * time.Minute, time.Minute, time.Minute},
		{"explicit cannot take the model's quarter", 4 * time.Minute, 10 * time.Minute, 3 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var deadline time.Time
			if tc.remaining > 0 {
				deadline = now.Add(tc.remaining)
			}
			if got := hostEvidenceBudget(now, deadline, tc.explicit); got != tc.want {
				t.Fatalf("hostEvidenceBudget = %s, want %s", got, tc.want)
			}
		})
	}
}
