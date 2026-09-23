package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/reviewsandbox"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// batchableGoTestEvidenceRequest reports whether request is eligible for
// batched remote `go test -json` execution: a plain run_verification
// kind=test, language=go call with no test-name pattern (exactly the shape
// reviewVerificationEvidenceRequests generates for a changed Go package).
// Requests outside this shape -- build/check kinds, other languages,
// patterned test runs -- keep using the existing per-request tool-call
// path, which itself still runs through a configured wrapper (see
// reviewsandbox.Executor.SetWrapper) just without batching several packages
// into one remote invocation.
func batchableGoTestEvidenceRequest(request AgentEvidenceRequest) (path string, ok bool) {
	if strings.TrimSpace(request.Tool) != "run_verification" {
		return "", false
	}
	kind, _ := request.Parameters["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "test") {
		return "", false
	}
	language, _ := request.Parameters["language"].(string)
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" && language != "go" {
		return "", false
	}
	if pattern, _ := request.Parameters["pattern"].(string); strings.TrimSpace(pattern) != "" {
		return "", false
	}
	path, _ = request.Parameters["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	return path, true
}

// collectBatchedGoTestEvidence runs every batchable Go test evidence request
// through as few remote `go test -json` wrapper invocations as parallelism
// allows -- in place of one invocation per package -- and fills calls[index]
// for each request in indices. Requests are grouped by their exact path; a
// path requested more than once (unusual, but not forbidden by the planner
// interface) reuses that path's one verification result for every matching
// index.
func collectBatchedGoTestEvidence(
	ctx context.Context,
	root string,
	wrapper []string,
	timeout time.Duration,
	parallelism int,
	requests []AgentEvidenceRequest,
	indices []int,
	calls []AgentToolCall,
) {
	if len(indices) == 0 {
		return
	}
	byPath := make(map[string][]int, len(indices))
	order := make([]string, 0, len(indices))
	for _, index := range indices {
		path, _ := batchableGoTestEvidenceRequest(requests[index])
		if _, seen := byPath[path]; !seen {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], index)
	}

	targets := make([]reviewsandbox.BatchTarget, len(order))
	for i, path := range order {
		targets[i] = reviewsandbox.BatchTarget{Path: path}
	}
	chunks := chunkBatchTargets(targets, parallelism)

	executor := reviewsandbox.NewExecutorWithCodexCommand("")
	executor.SetWrapper(wrapper)

	var wg sync.WaitGroup
	wg.Add(len(chunks))
	for _, chunk := range chunks {
		go func(chunk []reviewsandbox.BatchTarget) {
			defer wg.Done()
			results, err := executor.VerifyGoTestBatch(ctx, root, chunk, timeout, 0)
			for _, target := range chunk {
				verification, ok := results[target.Path]
				if err != nil || !ok {
					// A batch-wide setup failure (for example an unreadable
					// go.mod) or a target missing from the returned map both
					// mean the harness has no real evidence for this
					// package; grade it UNAVAILABLE rather than dropping it
					// or guessing a pass.
					reason := "batched verification produced no result for this package"
					if err != nil {
						reason = fmt.Sprintf("batched verification failed: %v", err)
					}
					verification = reviewsandbox.Result{
						Kind:     reviewsandbox.KindTest,
						Language: reviewsandbox.LanguageGo,
						Path:     target.Path,
						Command:  "go",
						ExitCode: -1,
						Status:   reviewsandbox.StatusUnavailable,
						Error:    reason,
					}
				}
				for _, index := range byPath[target.Path] {
					calls[index] = agentToolCallFromVerification(index, requests[index], verification)
				}
			}
		}(chunk)
	}
	wg.Wait()
}

// chunkBatchTargets splits targets into at most parallelism contiguous
// groups (never more groups than targets, never fewer than one group when
// targets is non-empty), so a batched run fans out across at most
// parallelism concurrent remote wrapper invocations instead of one
// invocation per package.
func chunkBatchTargets(targets []reviewsandbox.BatchTarget, parallelism int) [][]reviewsandbox.BatchTarget {
	if len(targets) == 0 {
		return nil
	}
	if parallelism < 1 {
		parallelism = 1
	}
	groups := min(parallelism, len(targets))
	size := (len(targets) + groups - 1) / groups
	chunks := make([][]reviewsandbox.BatchTarget, 0, groups)
	for start := 0; start < len(targets); start += size {
		end := min(start+size, len(targets))
		chunks = append(chunks, targets[start:end])
	}
	return chunks
}

// agentToolCallFromVerification builds the same AgentToolCall shape
// collectAgentEvidenceRequest does for a direct run_verification tool call
// (same Data fields, same encoded Result string, same evidence/proves
// grading through builtin.BuildVerificationToolResult), so batched and
// per-request evidence are indistinguishable to every downstream consumer:
// aggregateHostVerification, formatHostAgentEvidence, approval validation.
func agentToolCallFromVerification(index int, request AgentEvidenceRequest, verification reviewsandbox.Result) AgentToolCall {
	callID := fmt.Sprintf("host-evidence-%d", index+1)
	arguments, _ := json.Marshal(request.Parameters)
	result := builtin.BuildVerificationToolResult(verification)
	call := AgentToolCall{
		ID:        callID,
		Name:      strings.TrimSpace(request.Tool),
		Arguments: string(arguments),
		Duration:  verification.Duration,
		Success:   result.Success,
		Data:      make(map[string]any, len(result.Data)),
	}
	for key, value := range result.Data {
		call.Data[key] = value
	}
	encoded, err := tool.ToModelOutput(result)
	if err != nil {
		call.Result = fmt.Sprintf("encode evidence result: %v", err)
		return call
	}
	call.Result = encoded
	return call
}

// effectiveVerificationParallelism resolves a configured verification
// parallelism cap: a positive configured value wins, otherwise
// reviewsandbox.DefaultLocalParallelism() applies -- even when no remote
// wrapper is configured, so local verification never launches more
// concurrent build/test processes than a quarter of the host's CPUs.
func effectiveVerificationParallelism(configured int) int {
	if configured > 0 {
		return configured
	}
	return reviewsandbox.DefaultLocalParallelism()
}
