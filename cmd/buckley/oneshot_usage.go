package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
)

// oneShotUsageSummary counts what the one-shot run's model calls reported, so
// the final "One-shot usage:" line can say which numbers came from the provider
// and which are local estimates. A nil summary ignores every call.
type oneShotUsageSummary struct {
	mu        sync.Mutex
	reported  int // responses that carried a provider usage record
	estimated int // responses priced from Buckley's local estimate
	costKnown int // responses that also carried the provider's own cost
	costSum   float64
	usage     model.Usage // sum over all recorded responses
	printed   bool
}

// record counts one response. providerUsage is the provider's own record from
// the stream (nil when none arrived); AddUsage drops its cost field, so the
// cost is read from it directly.
func (s *oneShotUsageSummary) record(resp *model.ChatResponse, providerUsage *model.Usage) {
	if s == nil || resp == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if resp.UsagePresent && !resp.Usage.Estimated {
		s.reported++
		if providerUsage != nil {
			if cost, ok := providerUsage.TotalCostUSD(); ok {
				s.costKnown++
				s.costSum += cost
			}
		}
	} else {
		s.estimated++
	}
	s.usage = model.AddUsage(s.usage, resp.Usage)
}

// markPrinted returns true the first time it is called.
func (s *oneShotUsageSummary) markPrinted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.printed {
		return false
	}
	s.printed = true
	return true
}

// oneShotUsageInputs is everything the usage line needs besides the summary.
type oneShotUsageInputs struct {
	result     *agentloop.Result // nil when the loop never produced a result
	ceilingUSD float64           // 0 when no --budget
	elapsed    time.Duration
	listCost   func(model.Usage) (float64, error) // nil when no price is known
}

// formatOneShotUsageLine renders the single "One-shot usage:" line.
func formatOneShotUsageLine(s *oneShotUsageSummary, in oneShotUsageInputs) string {
	var snapshot oneShotUsageSummary
	if s != nil {
		s.mu.Lock()
		snapshot = oneShotUsageSummary{reported: s.reported, estimated: s.estimated, costKnown: s.costKnown, costSum: s.costSum, usage: s.usage}
		s.mu.Unlock()
	}
	responses := snapshot.reported + snapshot.estimated

	requests, tools := 0, 0
	usage := snapshot.usage
	charged := "none"
	if in.result != nil {
		requests, tools = in.result.ModelRequests, in.result.ToolCalls
		usage = in.result.Usage
		if in.ceilingUSD > 0 {
			charged = fmt.Sprintf("%.6f", in.result.CostUSD)
		}
	}

	kind := "none"
	switch {
	case responses == 0:
	case snapshot.estimated == 0:
		kind = "reported"
	case snapshot.reported == 0:
		kind = "estimated"
	default:
		kind = "mixed"
	}

	basis := "none"
	if in.ceilingUSD > 0 && charged != "none" {
		switch kind {
		case "reported":
			basis = "usage"
		case "estimated":
			basis = "reserved"
		case "mixed":
			basis = "mixed"
		}
	}

	cached, reasoning, list, provider := "unknown", "unknown", "unknown", "unknown"
	if kind == "reported" {
		cachedTokens, reasoningTokens := 0, 0
		if usage.PromptTokensDetails != nil {
			cachedTokens = usage.PromptTokensDetails.CachedTokens
		}
		if usage.CompletionTokenDetails != nil {
			reasoningTokens = usage.CompletionTokenDetails.ReasoningTokens
		}
		cached, reasoning = fmt.Sprint(cachedTokens), fmt.Sprint(reasoningTokens)
		if in.listCost != nil {
			if cost, err := in.listCost(usage); err == nil {
				list = fmt.Sprintf("%.6f", cost)
			}
		}
		if snapshot.costKnown == responses && responses > 0 {
			provider = fmt.Sprintf("%.6f", snapshot.costSum)
		}
	}

	ceiling := "none"
	if in.ceilingUSD > 0 {
		ceiling = fmt.Sprintf("%.6f", in.ceilingUSD)
	}
	return fmt.Sprintf("One-shot usage: requests=%d tools=%d input_tokens=%d cached_input_tokens=%s output_tokens=%d reasoning_tokens=%s usage=%s charged_usd=%s charge_basis=%s ceiling_usd=%s list_cost_usd=%s provider_cost_usd=%s elapsed_s=%d",
		requests, tools, usage.PromptTokens, cached, usage.CompletionTokens, reasoning, kind, charged, basis, ceiling, list, provider, int(in.elapsed.Round(time.Second)/time.Second))
}

// printOneShotUsage writes the usage line once per run, also under --quiet.
func printOneShotUsage(w io.Writer, s *oneShotUsageSummary, in oneShotUsageInputs) {
	if s == nil || !s.markPrinted() {
		return
	}
	fmt.Fprintln(w, strings.TrimRight(formatOneShotUsageLine(s, in), "\n"))
}
