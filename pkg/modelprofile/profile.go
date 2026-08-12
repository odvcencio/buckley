// Package modelprofile owns provider-neutral, versioned empirical model facts.
// It has no dependency on protocol, storage, providers, or UI adapters.
package modelprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = "buckley.model-behavior/v1"

type Class string

const (
	ClassWeak     Class = "weak"
	ClassBalanced Class = "balanced"
	ClassFrontier Class = "frontier"
)

type Capabilities struct {
	ToolCalls            bool `json:"tool_calls"`
	NativeJSONSchema     bool `json:"native_json_schema"`
	ParallelToolCalls    bool `json:"parallel_tool_calls"`
	Continuation         bool `json:"continuation"`
	Reasoning            bool `json:"reasoning"`
	CodeMode             bool `json:"code_mode"`
	ContextWindowTokens  int  `json:"context_window_tokens,omitempty"`
	SafeVisibleToolCount int  `json:"safe_visible_tool_count,omitempty"`
}

type Metrics struct {
	TaskSuccessRate             float64 `json:"task_success_rate,omitempty"`
	ToolReliability             float64 `json:"tool_reliability"`
	ArgumentRepairReliability   float64 `json:"argument_repair_reliability"`
	StructuredOutputReliability float64 `json:"structured_output_reliability"`
	ParallelCallReliability     float64 `json:"parallel_call_reliability"`
	EditFidelity                float64 `json:"edit_fidelity"`
	VerificationPassRate        float64 `json:"verification_pass_rate"`
	EffectiveContextTokens      int     `json:"effective_context_tokens,omitempty"`
	ContinuationReliability     float64 `json:"continuation_reliability"`
	LatencyP50MS                int64   `json:"latency_p50_ms,omitempty"`
	LatencyP95MS                int64   `json:"latency_p95_ms,omitempty"`
	AverageTaskLatencyMS        float64 `json:"average_task_latency_ms,omitempty"`
	AverageTokensPerTask        float64 `json:"average_tokens_per_task,omitempty"`
	AverageCostUSDPerTask       float64 `json:"average_cost_usd_per_task,omitempty"`
	CostUSDPerSuccessfulTask    float64 `json:"cost_usd_per_successful_task,omitempty"`
	CostUSDPerMTokens           float64 `json:"cost_usd_per_m_tokens,omitempty"`
}

// SampleCounts preserves the denominator for every independently observed
// metric. Optional signals must not be diluted by unrelated task samples.
type SampleCounts struct {
	TaskSuccess      int `json:"task_success,omitempty"`
	ToolReliability  int `json:"tool_reliability,omitempty"`
	ArgumentRepair   int `json:"argument_repair,omitempty"`
	StructuredOutput int `json:"structured_output,omitempty"`
	ParallelCall     int `json:"parallel_call,omitempty"`
	EditFidelity     int `json:"edit_fidelity,omitempty"`
	Verification     int `json:"verification,omitempty"`
	Continuation     int `json:"continuation,omitempty"`
	Latency          int `json:"latency,omitempty"`
	Tokens           int `json:"tokens,omitempty"`
	Cost             int `json:"cost,omitempty"`
}

// Profile is immutable once stored. Version and Digest give every compiled
// protocol a replayable measurement identity.
type Profile struct {
	SchemaVersion string       `json:"schema_version"`
	ModelID       string       `json:"model_id"`
	Provider      string       `json:"provider,omitempty"`
	Version       string       `json:"version"`
	Class         Class        `json:"class,omitempty"`
	SampleSize    int          `json:"sample_size"`
	Confidence    float64      `json:"confidence"`
	MeasuredAt    time.Time    `json:"measured_at"`
	Capabilities  Capabilities `json:"capabilities"`
	Metrics       Metrics      `json:"metrics"`
	Samples       SampleCounts `json:"samples,omitempty"`
}

func (p Profile) Normalize() Profile {
	p.SchemaVersion = strings.TrimSpace(p.SchemaVersion)
	if p.SchemaVersion == "" {
		p.SchemaVersion = SchemaVersion
	}
	p.ModelID = strings.TrimSpace(p.ModelID)
	p.Provider = strings.TrimSpace(p.Provider)
	p.Version = strings.TrimSpace(p.Version)
	p.Class = Class(strings.ToLower(strings.TrimSpace(string(p.Class))))
	p.MeasuredAt = p.MeasuredAt.UTC().Round(0)
	return p
}

func (p Profile) Validate() error {
	p = p.Normalize()
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("profile schema_version must equal %s", SchemaVersion)
	}
	if p.ModelID == "" {
		return fmt.Errorf("profile model_id is required")
	}
	if p.Version == "" {
		return fmt.Errorf("profile version is required")
	}
	if p.SampleSize < 0 {
		return fmt.Errorf("profile sample_size must not be negative")
	}
	if !ratio(p.Confidence) {
		return fmt.Errorf("profile confidence must be between 0 and 1")
	}
	if p.Class != "" && p.Class != ClassWeak && p.Class != ClassBalanced && p.Class != ClassFrontier {
		return fmt.Errorf("profile class must be weak, balanced, or frontier")
	}
	ratioMeasurements := [...]struct {
		name  string
		value float64
	}{
		{name: "task_success_rate", value: p.Metrics.TaskSuccessRate},
		{name: "tool_reliability", value: p.Metrics.ToolReliability},
		{name: "argument_repair_reliability", value: p.Metrics.ArgumentRepairReliability},
		{name: "structured_output_reliability", value: p.Metrics.StructuredOutputReliability},
		{name: "parallel_call_reliability", value: p.Metrics.ParallelCallReliability},
		{name: "edit_fidelity", value: p.Metrics.EditFidelity},
		{name: "verification_pass_rate", value: p.Metrics.VerificationPassRate},
		{name: "continuation_reliability", value: p.Metrics.ContinuationReliability},
	}
	for _, measurement := range ratioMeasurements {
		if !ratio(measurement.value) {
			return fmt.Errorf("profile %s must be between 0 and 1", measurement.name)
		}
	}
	if p.Capabilities.ContextWindowTokens < 0 || p.Capabilities.SafeVisibleToolCount < 0 || p.Metrics.EffectiveContextTokens < 0 || p.Metrics.LatencyP50MS < 0 || p.Metrics.LatencyP95MS < 0 {
		return fmt.Errorf("profile measurements must not be negative")
	}
	finiteMeasurements := [...]struct {
		name  string
		value float64
	}{
		{name: "average_task_latency_ms", value: p.Metrics.AverageTaskLatencyMS},
		{name: "average_tokens_per_task", value: p.Metrics.AverageTokensPerTask},
		{name: "average_cost_usd_per_task", value: p.Metrics.AverageCostUSDPerTask},
		{name: "cost_usd_per_successful_task", value: p.Metrics.CostUSDPerSuccessfulTask},
		{name: "cost_usd_per_m_tokens", value: p.Metrics.CostUSDPerMTokens},
	}
	for _, measurement := range finiteMeasurements {
		if !nonnegativeFinite(measurement.value) {
			return fmt.Errorf("profile %s must be finite and non-negative", measurement.name)
		}
	}
	sampleMeasurements := [...]struct {
		name  string
		count int
	}{
		{name: "task_success", count: p.Samples.TaskSuccess},
		{name: "tool_reliability", count: p.Samples.ToolReliability},
		{name: "argument_repair", count: p.Samples.ArgumentRepair},
		{name: "structured_output", count: p.Samples.StructuredOutput},
		{name: "parallel_call", count: p.Samples.ParallelCall},
		{name: "edit_fidelity", count: p.Samples.EditFidelity},
		{name: "verification", count: p.Samples.Verification},
		{name: "continuation", count: p.Samples.Continuation},
		{name: "latency", count: p.Samples.Latency},
		{name: "tokens", count: p.Samples.Tokens},
		{name: "cost", count: p.Samples.Cost},
	}
	for _, measurement := range sampleMeasurements {
		if measurement.count < 0 || measurement.count > p.SampleSize {
			return fmt.Errorf("profile %s samples must be between 0 and sample_size", measurement.name)
		}
	}
	return nil
}

func (p Profile) ResolvedClass() Class {
	p = p.Normalize()
	if p.Class != "" {
		return p.Class
	}
	if p.SampleSize < 20 || p.Confidence < 0.70 || p.Metrics.ToolReliability < 0.85 || p.Metrics.StructuredOutputReliability < 0.90 {
		return ClassWeak
	}
	if p.Samples.TaskSuccess >= 10 && p.Metrics.TaskSuccessRate < 0.75 {
		return ClassWeak
	}
	frontierTaskEvidence := p.Samples.TaskSuccess == 0 || (p.Samples.TaskSuccess >= 20 && p.Metrics.TaskSuccessRate >= 0.90)
	if frontierTaskEvidence && p.Capabilities.Continuation && p.Capabilities.ParallelToolCalls && p.Metrics.ContinuationReliability >= 0.90 && p.Metrics.ParallelCallReliability >= 0.90 && p.Metrics.EffectiveContextTokens >= 96*1024 {
		return ClassFrontier
	}
	return ClassBalanced
}

func (p Profile) Digest() (string, error) {
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode behavior profile: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Observation contains only aggregate evaluation signals, never user source.
type Observation struct {
	Succeeded          bool
	ToolSucceeded      *bool
	ArgumentRepaired   *bool
	StructuredOutput   *bool
	ParallelCall       *bool
	EditFaithful       *bool
	VerificationPassed *bool
	ContinuationWorked *bool
	LatencyMS          int64
	PromptTokens       int
	CompletionTokens   int
	TokensObserved     bool
	CostUSD            float64
	CostObserved       bool
}

func Aggregate(base Profile, observations []Observation, measuredAt time.Time) (Profile, error) {
	base = base.Normalize()
	if err := base.Validate(); err != nil {
		return Profile{}, err
	}
	if len(observations) == 0 {
		return base, nil
	}
	for _, observation := range observations {
		if observation.LatencyMS < 0 || observation.PromptTokens < 0 || observation.CompletionTokens < 0 || !nonnegativeFinite(observation.CostUSD) {
			return Profile{}, fmt.Errorf("profile observation measurements must be finite and non-negative")
		}
	}
	next := base
	next.SampleSize += len(observations)
	next.MeasuredAt = measuredAt.UTC().Round(0)
	next.Metrics.TaskSuccessRate, next.Samples.TaskSuccess = aggregateSuccess(base.Metrics.TaskSuccessRate, base.Samples.TaskSuccess, observations)
	next.Metrics.ToolReliability, next.Samples.ToolReliability = aggregateRatio(base.Metrics.ToolReliability, metricSampleCount(base.Samples.ToolReliability, base), observations, func(o Observation) *bool { return o.ToolSucceeded })
	next.Metrics.ArgumentRepairReliability, next.Samples.ArgumentRepair = aggregateRatio(base.Metrics.ArgumentRepairReliability, metricSampleCount(base.Samples.ArgumentRepair, base), observations, func(o Observation) *bool { return o.ArgumentRepaired })
	next.Metrics.StructuredOutputReliability, next.Samples.StructuredOutput = aggregateRatio(base.Metrics.StructuredOutputReliability, metricSampleCount(base.Samples.StructuredOutput, base), observations, func(o Observation) *bool { return o.StructuredOutput })
	next.Metrics.ParallelCallReliability, next.Samples.ParallelCall = aggregateRatio(base.Metrics.ParallelCallReliability, metricSampleCount(base.Samples.ParallelCall, base), observations, func(o Observation) *bool { return o.ParallelCall })
	next.Metrics.EditFidelity, next.Samples.EditFidelity = aggregateRatio(base.Metrics.EditFidelity, metricSampleCount(base.Samples.EditFidelity, base), observations, func(o Observation) *bool { return o.EditFaithful })
	next.Metrics.VerificationPassRate, next.Samples.Verification = aggregateRatio(base.Metrics.VerificationPassRate, metricSampleCount(base.Samples.Verification, base), observations, func(o Observation) *bool { return o.VerificationPassed })
	next.Metrics.ContinuationReliability, next.Samples.Continuation = aggregateRatio(base.Metrics.ContinuationReliability, metricSampleCount(base.Samples.Continuation, base), observations, func(o Observation) *bool { return o.ContinuationWorked })
	next.Metrics.AverageTaskLatencyMS, next.Samples.Latency = aggregateMean(base.Metrics.AverageTaskLatencyMS, base.Samples.Latency, observations, func(o Observation) (float64, bool) {
		return float64(o.LatencyMS), o.LatencyMS > 0
	})
	next.Metrics.AverageTokensPerTask, next.Samples.Tokens = aggregateMean(base.Metrics.AverageTokensPerTask, base.Samples.Tokens, observations, func(o Observation) (float64, bool) {
		return float64(o.PromptTokens + o.CompletionTokens), o.TokensObserved
	})
	next.Metrics.AverageCostUSDPerTask, next.Samples.Cost = aggregateMean(base.Metrics.AverageCostUSDPerTask, base.Samples.Cost, observations, func(o Observation) (float64, bool) {
		return o.CostUSD, o.CostObserved
	})
	latencies := make([]int64, 0, len(observations))
	for _, observation := range observations {
		if observation.LatencyMS > 0 {
			latencies = append(latencies, observation.LatencyMS)
		}
	}
	if len(latencies) > 0 {
		if base.Samples.Latency == 0 {
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			next.Metrics.LatencyP50MS = percentile(latencies, 0.50)
			next.Metrics.LatencyP95MS = percentile(latencies, 0.95)
		} else {
			// Exact percentiles cannot be merged from prior percentiles. Keep the
			// cumulative mean and mark quantiles unavailable instead of publishing
			// the newest batch as if it represented the complete sample.
			next.Metrics.LatencyP50MS = 0
			next.Metrics.LatencyP95MS = 0
		}
	}
	next.Metrics.CostUSDPerSuccessfulTask = 0
	if next.Samples.Cost == next.Samples.TaskSuccess && next.Samples.TaskSuccess > 0 {
		successes := next.Metrics.TaskSuccessRate * float64(next.Samples.TaskSuccess)
		if successes > 0 {
			next.Metrics.CostUSDPerSuccessfulTask = next.Metrics.AverageCostUSDPerTask * float64(next.Samples.Cost) / successes
		}
	}
	if err := next.Validate(); err != nil {
		return Profile{}, err
	}
	return next, nil
}

func aggregateSuccess(previous float64, prior int, observations []Observation) (float64, int) {
	weighted := previous * float64(prior)
	for _, observation := range observations {
		if observation.Succeeded {
			weighted++
		}
	}
	count := prior + len(observations)
	return weighted / float64(count), count
}

func aggregateRatio(previous float64, prior int, observations []Observation, extract func(Observation) *bool) (float64, int) {
	weighted, count := previous*float64(prior), prior
	for _, observation := range observations {
		if value := extract(observation); value != nil {
			count++
			if *value {
				weighted++
			}
		}
	}
	if count == 0 {
		return previous, 0
	}
	return weighted / float64(count), count
}

func aggregateMean(previous float64, prior int, observations []Observation, extract func(Observation) (float64, bool)) (float64, int) {
	weighted, count := previous*float64(prior), prior
	for _, observation := range observations {
		if value, ok := extract(observation); ok {
			weighted += value
			count++
		}
	}
	if count == 0 {
		return previous, 0
	}
	return weighted / float64(count), count
}

func metricSampleCount(recorded int, profile Profile) int {
	if recorded > 0 {
		return recorded
	}
	// Profiles written before per-signal sample counts used SampleSize as
	// every ratio's denominator. Once task samples are recorded, zero means
	// this specific signal was genuinely not observed.
	if profile.Samples.TaskSuccess > 0 {
		return 0
	}
	return profile.SampleSize
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1)*p + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func ratio(value float64) bool { return value >= 0 && value <= 1 }

func nonnegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
