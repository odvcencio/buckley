package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/reviewpolicy"
	"m31labs.dev/buckley/pkg/transparency"
)

type retentionShardRunner struct {
	responses map[string]retentionShardResponse
	calls     atomic.Int32
}

type retentionShardResponse struct {
	review     string
	err        error
	incomplete bool
	trace      *transparency.Trace
	tools      []oneshot.AgentToolCall
	commands   []model.CommandExecutionEvidence
	delay      time.Duration
	waitFor    <-chan struct{}
	notifyDone chan<- struct{}
}

func (r *retentionShardRunner) Run(ctx context.Context, _, task string, _ []string, _ oneshot.AgentExecutionOpts) (*oneshot.AgentResult, error) {
	r.calls.Add(1)
	shard := retentionPromptShard(task)
	response := r.responses[shard]
	if response.notifyDone != nil {
		defer close(response.notifyDone)
	}
	if response.waitFor != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-response.waitFor:
		}
	}
	if response.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(response.delay):
		}
	}
	return &oneshot.AgentResult{
		Response:          response.review,
		Incomplete:        response.incomplete,
		Trace:             response.trace,
		ToolCalls:         response.tools,
		ExecutionEvidence: response.commands,
	}, response.err
}

func (r *retentionShardRunner) CollectAgentEvidence(context.Context, []oneshot.AgentEvidenceRequest, oneshot.AgentExecutionOpts) ([]oneshot.AgentToolCall, error) {
	return nil, nil
}

func retentionPromptShard(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "## Pull Request (shard ") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				return parts[4]
			}
		}
	}
	return ""
}

func TestRunPRReviewShardedRetainsOutOfOrderShardTraceAndEvidence(t *testing.T) {
	head := retentionTempGitHead(t)
	withFakeGH(t, head)

	exit := 0
	shard2Done := make(chan struct{})
	runner := &retentionShardRunner{responses: map[string]retentionShardResponse{
		"1": {
			review: retentionReview("a.go", "B", false),
			trace:  retentionTrace("s1-primary", 10, 2),
			tools:  []oneshot.AgentToolCall{{ID: "tool-s1", Name: "read_file", Success: true}},
			commands: []model.CommandExecutionEvidence{{
				Command: "go test ./a", Status: "completed", ExitCode: &exit,
			}},
			waitFor: shard2Done,
		},
		"2": {
			review:     retentionReview("b.go", "B", false),
			trace:      retentionTrace("s2-primary", 40, 5),
			tools:      []oneshot.AgentToolCall{{ID: "tool-s2", Name: "search_text", Success: true}},
			notifyDone: shard2Done,
		},
	}}
	framework := oneshot.NewFramework(nil, nil).WithAgentRunner(runner).WithApprovalCriticRunner(runner)

	result, _, err := runPRReviewSharded(context.Background(), framework, retentionOptions(), retentionPRContext(head, "a.go", "b.go"), nil, retentionShards("a.go", "b.go"), silentReviewProgress{})
	if err != nil {
		t.Fatalf("runPRReviewSharded() error = %v", err)
	}
	if result == nil || result.parsed == nil || result.incomplete {
		t.Fatalf("result = %#v, want complete parsed result", result)
	}
	if result.attempts != 2 || result.primary != 2 || result.criticAttempts != 0 {
		t.Fatalf("attempt counts = %d/%d/%d, want total 2 primary 2 critic 0", result.attempts, result.primary, result.criticAttempts)
	}
	if got := result.trace.Attempts; len(got) != 2 ||
		got[0].Phase != "shard 1/primary" ||
		got[1].Phase != "shard 2/primary" {
		t.Fatalf("trace attempts = %#v, want deterministic flattened shard order", got)
	}
	if result.trace.Tokens.Input != 50 || result.trace.Tokens.Output != 7 {
		t.Fatalf("tokens = %#v, want aggregate input/output 50/7", result.trace.Tokens)
	}
	if len(result.toolEvidence) != 2 || len(result.commandEvidence) != 1 {
		t.Fatalf("evidence counts = tools:%d commands:%d, want 2/1", len(result.toolEvidence), len(result.commandEvidence))
	}
}

func TestRunPRReviewShardedFailureRetainsPriorAndFailedShardWork(t *testing.T) {
	head := retentionTempGitHead(t)
	exit := 1
	runner := &retentionShardRunner{responses: map[string]retentionShardResponse{
		"1": {
			review: retentionReview("a.go", "B", false),
			trace:  retentionTrace("s1-primary", 5, 1),
			tools:  []oneshot.AgentToolCall{{ID: "tool-s1", Name: "read_file", Success: true}},
		},
		"2": {
			review:     retentionReview("b.go", "B", false),
			err:        errors.New("provider stopped mid-run"),
			incomplete: true,
			trace:      retentionTrace("s2-primary", 7, 1),
			commands:   []model.CommandExecutionEvidence{{Command: "go test ./b", Status: "failed", ExitCode: &exit}},
		},
		"3": {review: retentionReview("c.go", "B", false), delay: 100 * time.Millisecond},
	}}
	framework := oneshot.NewFramework(nil, nil).WithAgentRunner(runner)
	opts := retentionOptions()
	opts.concurrency = 1

	result, _, err := runPRReviewSharded(context.Background(), framework, opts, retentionPRContext(head, "a.go", "b.go", "c.go"), nil, retentionShards("a.go", "b.go", "c.go"), silentReviewProgress{})
	if err == nil {
		t.Fatal("runPRReviewSharded() error = nil, want shard failure")
	}
	if result == nil || !result.incomplete || result.parsed != nil {
		t.Fatalf("result = %#v, want incomplete unparsed result", result)
	}
	for _, want := range []string{"Incomplete review", "Shard 1 draft", "Shard 2 draft", "tool-s1", "go test ./b", "provider stopped mid-run"} {
		if !strings.Contains(result.reviewText, want) {
			t.Fatalf("salvaged review missing %q:\n%s", want, result.reviewText)
		}
	}
	if strings.Contains(result.reviewText, "Shard 3 draft") {
		t.Fatalf("not-started shard was rendered:\n%s", result.reviewText)
	}
	if got := result.trace.Attempts; len(got) != 2 || got[0].Phase != "shard 1/primary" || got[1].Phase != "shard 2/primary" {
		t.Fatalf("trace attempts = %#v, want completed shards only", got)
	}
}

func TestRunPRReviewShardedRetainsEvidenceOnRevalidationFailure(t *testing.T) {
	head := retentionTempGitHead(t)
	runner := &retentionShardRunner{responses: map[string]retentionShardResponse{
		"1": {
			review: retentionReview("a.go", "B", false),
			trace:  retentionTrace("s1-primary", 11, 2),
			tools:  []oneshot.AgentToolCall{{ID: "tool-s1", Name: "read_file", Success: true}},
		},
	}}
	framework := oneshot.NewFramework(nil, nil).WithAgentRunner(runner)
	ctx := retentionPRContext(head, "a.go")
	ctx.CIAdmission = reviewpolicy.CIAdmissionReceipt{}

	result, _, err := runPRReviewSharded(context.Background(), framework, retentionOptions(), ctx, nil, retentionShards("a.go"), silentReviewProgress{})
	if err == nil {
		t.Fatal("runPRReviewSharded() error = nil, want revalidation failure")
	}
	if result == nil || !result.incomplete || result.parsed != nil {
		t.Fatalf("result = %#v, want incomplete unparsed result", result)
	}
	if result.trace == nil || len(result.trace.Attempts) != 1 || result.trace.Attempts[0].Phase != "shard 1/primary" {
		t.Fatalf("trace = %#v, want retained shard trace", result.trace)
	}
	if !strings.Contains(result.reviewText, "captured CI admission receipt") || !strings.Contains(result.reviewText, "tool-s1") {
		t.Fatalf("incomplete revalidation review missing retained diagnostics:\n%s", result.reviewText)
	}
}

func TestAggregatePRShardTracesFlattensNestedPrimaryAndCriticAttempts(t *testing.T) {
	primary1 := retentionTrace("s1-primary-1", 10, 2)
	primary1.ModelExecutions = []transparency.ExecutionIdentityTrace{retentionExecutionIdentity("req-1", "sel-1", "provider-1", "resp-model-1", "resp-1", false)}
	primary2 := retentionTrace("s1-primary-2", 20, 3)
	primary2.ModelExecutions = []transparency.ExecutionIdentityTrace{retentionExecutionIdentity("req-2", "sel-2", "provider-2", "resp-model-2", "resp-2", false)}
	critic := retentionTrace("s1-critic-1", 30, 4)
	critic.ModelExecutions = []transparency.ExecutionIdentityTrace{retentionExecutionIdentity("req-3", "sel-3", "provider-3", "resp-model-3", "resp-3", true)}
	primary3 := retentionTrace("s2-primary-1", 40, 5)
	primary3.ModelExecutions = []transparency.ExecutionIdentityTrace{retentionExecutionIdentity("req-4", "sel-4", "provider-4", "resp-model-4", "resp-4", false)}

	shardResults := []*reviewCommandResult{
		{
			attempts:       3,
			primary:        2,
			criticAttempts: 1,
			trace: &transparency.Trace{Attempts: []transparency.TraceAttempt{
				{Phase: "primary", Attempt: 1, ValidationError: "repair", Trace: primary1},
				{Phase: "primary", Attempt: 2, Trace: primary2},
				{Phase: "approval critic", Attempt: 1, Trace: critic},
			}},
		},
		{
			attempts: 1,
			primary:  1,
			trace: &transparency.Trace{Attempts: []transparency.TraceAttempt{
				{Phase: "primary", Attempt: 1, Trace: primary3},
			}},
		},
	}
	result := (&reviewCommandResult{trace: aggregatePRShardTraces(shardResults)}).withShardedReviewEvidence(shardResults)

	if result.attempts != 4 || result.primary != 3 || result.criticAttempts != 1 {
		t.Fatalf("counts = %d/%d/%d, want total 4 primary 3 critic 1", result.attempts, result.primary, result.criticAttempts)
	}
	if got := result.trace.Attempts; len(got) != 4 ||
		got[0].Phase != "shard 1/primary" ||
		got[1].Phase != "shard 1/primary" ||
		got[2].Phase != "shard 1/approval critic" ||
		got[3].Phase != "shard 2/primary" {
		t.Fatalf("trace attempts = %#v, want flattened primary/critic attempts", got)
	}
	if result.trace.Tokens.Input != 100 || result.trace.Tokens.Output != 14 {
		t.Fatalf("tokens = %#v, want aggregate input/output 100/14", result.trace.Tokens)
	}
	for _, attempt := range result.trace.Attempts {
		if len(attempt.Trace.Attempts) != 0 {
			t.Fatalf("nested attempts leaked into aggregate: %#v", attempt)
		}
	}
	want := []transparency.ExecutionIdentityTrace{
		retentionExecutionIdentity("req-1", "sel-1", "provider-1", "resp-model-1", "resp-1", false),
		retentionExecutionIdentity("req-2", "sel-2", "provider-2", "resp-model-2", "resp-2", false),
		retentionExecutionIdentity("req-3", "sel-3", "provider-3", "resp-model-3", "resp-3", true),
		retentionExecutionIdentity("req-4", "sel-4", "provider-4", "resp-model-4", "resp-4", false),
	}
	if got := result.trace.ModelExecutions; len(got) != len(want) {
		t.Fatalf("model executions = %#v, want %d identities", got, len(want))
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("model execution %d = %#v, want %#v", i, got[i], want[i])
			}
		}
	}
	primary1.ModelExecutions[0].ResponseID = "mutated"
	if result.trace.ModelExecutions[0].ResponseID != "resp-1" ||
		result.trace.Attempts[0].Trace.ModelExecutions[0].ResponseID != "resp-1" {
		t.Fatalf("aggregate retained aliased identities: top=%#v attempt=%#v",
			result.trace.ModelExecutions, result.trace.Attempts[0].Trace.ModelExecutions)
	}
}

func TestPRShardTraceRetentionHandlesNilAndTypedNilResults(t *testing.T) {
	var typedNil *commands.ReviewAgentResult
	shards := []*reviewCommandResult{
		nil,
		reviewResultFromAgent(&oneshot.RunResult{Value: typedNil}, nil),
		{trace: &transparency.Trace{ID: "plain", Tokens: transparency.TokenUsage{Input: 3, Output: 4}}},
	}

	trace := aggregatePRShardTraces(shards)
	if trace == nil || len(trace.Attempts) != 1 {
		t.Fatalf("aggregate trace = %#v, want only nonnil trace", trace)
	}
	if got := trace.Attempts[0].Phase; got != "shard 3" {
		t.Fatalf("phase = %q, want shard 3", got)
	}
	if shards[1].reviewText != "" || shards[1].parsed != nil {
		t.Fatalf("typed nil shard result = %#v, want empty result", shards[1])
	}
}

func retentionOptions() automatedReviewOptions {
	return automatedReviewOptions{
		concurrency:          2,
		maxRetries:           1,
		modelID:              "test-model",
		reasoningEffort:      "low",
		depth:                reviewDepthSpot,
		costPerMillionTokens: 0,
	}
}

func retentionShards(paths ...string) diffsignal.ShardResult {
	shards := make([]diffsignal.Shard, 0, len(paths))
	for _, path := range paths {
		shards = append(shards, diffsignal.Shard{Files: []string{path}, Context: "diff --git a/" + path + " b/" + path})
	}
	return diffsignal.ShardResult{Shards: shards}
}

func retentionPRContext(head string, paths ...string) *commands.PRContext {
	pr := &commands.PRInfo{
		Number:     1,
		Title:      "Retention",
		Author:     "octo",
		State:      "OPEN",
		URL:        "https://github.com/acme/repo/pull/1",
		Host:       "github.com",
		Repository: "acme/repo",
		BaseBranch: "main",
		BaseSHA:    "base-sha",
		HeadBranch: "feature",
		HeadSHA:    head,
		CIStatus:   "unknown",
	}
	ctx := &commands.PRContext{PR: pr, Files: paths, CIProvenance: "pull request head", CIRevision: head}
	admission, err := reviewpolicy.NewCIAdmissionReceipt(reviewpolicy.CIAdmissionInput{
		Expectation:               ctx.CIAdmissionExpectation(),
		RequiredContextsAvailable: true,
	})
	if err != nil {
		panic(err)
	}
	ctx.CIAdmission = admission
	return ctx
}

func retentionReview(path, grade string, approved bool) string {
	approvedLine := "NO"
	if approved {
		approvedLine = "YES"
	}
	return fmt.Sprintf(`## Grade: %s

## Summary
Shard coverage is complete.

## CI Status
- Build: PASS
- Tests: PASS

## Coverage
- **File**: %s%s%s - reviewed this shard.
- **Feedback disposition**: %sNONE_SUPPLIED%s - no prior feedback was supplied.
- **Verification**: CI.
- **Completeness**: COMPLETE

## Invariant Audit
- Checked the shard contract.

## Falsification
- **Strongest plausible failure**: this shard was skipped.
- **Evidence**: the shard coverage ledger names the file.
- **Conclusion**: DISPROVED.

## Findings
None.

## Verdict
- **Approved**: %s
- **Blockers**: None
- **Suggestions**: None`, grade, "`", path, "`", "`", "`", approvedLine)
}

func retentionTrace(id string, input, output int) *transparency.Trace {
	return &transparency.Trace{
		ID:        id,
		Timestamp: time.Unix(1, 0),
		Duration:  time.Second,
		Tokens:    transparency.TokenUsage{Input: input, Output: output},
		Content:   id + " content",
	}
}

func retentionExecutionIdentity(requested, selected, provider, responseModel, responseID string, conflicted bool) transparency.ExecutionIdentityTrace {
	return transparency.ExecutionIdentityTrace{
		RequestedModel: requested,
		SelectedModel:  selected,
		ProviderID:     provider,
		ResponseModel:  responseModel,
		ResponseID:     responseID,
		Conflicted:     conflicted,
	}
}

func retentionTempGitHead(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	retentionRun(t, dir, "git", "init")
	retentionRun(t, dir, "git", "config", "user.email", "retention@example.test")
	retentionRun(t, dir, "git", "config", "user.name", "Retention Test")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write c.go: %v", err)
	}
	retentionRun(t, dir, "git", "add", ".")
	retentionRun(t, dir, "git", "commit", "-m", "initial")
	output := retentionRun(t, dir, "git", "rev-parse", "HEAD")
	t.Chdir(dir)
	return strings.TrimSpace(string(output))
}

func retentionRun(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return output
}

func withFakeGH(t *testing.T, head string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	body := fmt.Sprintf(`#!/bin/sh
args="$*"
if echo "$args" | grep -q 'pr view'; then
  printf '%%s\n' '{"number":1,"url":"https://github.com/acme/repo/pull/1","baseRefName":"main","baseRefOid":"base-sha","headRefName":"feature","headRefOid":"%s","reviewDecision":""}'
  exit 0
fi
if echo "$args" | grep -q 'pr checks'; then
  printf '[]\n'
  exit 0
fi
if echo "$args" | grep -q 'issues/1/comments'; then
  printf '[]\n'
  exit 0
fi
if echo "$args" | grep -q 'pulls/1/reviews'; then
  printf '[]\n'
  exit 0
fi
if echo "$args" | grep -q 'pulls/1/comments'; then
  printf '[]\n'
  exit 0
fi
if echo "$args" | grep -q 'reviewThreads'; then
  printf '%%s\n' '{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}'
  exit 0
fi
if echo "$args" | grep -q 'reviews'; then
  printf '%%s\n' '{"data":{"repository":{"pullRequest":{"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}'
  exit 0
fi
if echo "$args" | grep -q 'comments'; then
  printf '%%s\n' '{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}'
  exit 0
fi
printf 'unexpected gh args: %%s\n' "$args" >&2
exit 1
`, head)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
