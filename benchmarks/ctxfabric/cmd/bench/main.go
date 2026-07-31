// Command bench is the PR 0 measurement-only baseline for the M31 Context
// Fabric spec. It captures prompt/tool/context metrics for the curated
// agent-eval corpus, a gotreesitter compatibility matrix for Buckley and
// Canopy, and build-time/binary-size baselines for both repositories, and
// writes the result as one JSON artifact. It changes no runtime behavior;
// it only reads the corpus manifest and the two repositories on disk.
//
// Usage (from the Buckley repository root):
//
//	go run ./benchmarks/ctxfabric/cmd/bench \
//	  --canopy-dir /path/to/canopy \
//	  --out benchmarks/ctxfabric/artifacts/baseline.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"m31labs.dev/buckley/v2/benchmarks/ctxfabric/corpus"
	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// ScenarioMetrics is the recorded baseline for a single corpus scenario.
type ScenarioMetrics struct {
	ID                      string                     `json:"id"`
	Category                string                     `json:"category"`
	Name                    string                     `json:"name"`
	ToolCallCount           int                        `json:"tool_call_count"`
	MessageCount            int                        `json:"message_count"`
	ContextCompilationBytes int                        `json:"context_compilation_bytes"`
	TokenEstimate           model.RequestTokenEstimate `json:"token_estimate"`
}

// ToolSchemaSummary is the current built-in tool schema payload, measured
// against the release gate in spec section 32 ("Tool schema payload <=10
// KiB default working set").
type ToolSchemaSummary struct {
	ToolCount    int  `json:"tool_count"`
	PayloadBytes int  `json:"payload_bytes"`
	BudgetBytes  int  `json:"budget_bytes"`
	WithinBudget bool `json:"within_budget"`
}

// ConfigSnapshot records the PR 0 feature-flag scaffolding defaults, so the
// baseline artifact is self-evidencing that no behavior changed.
type ConfigSnapshot struct {
	ContextFabricEnabled bool   `json:"context_fabric_enabled"`
	ContextFabricShadow  bool   `json:"context_fabric_shadow"`
	AgentControllerMode  string `json:"agent_controller_mode"`
	MetricsEnabled       bool   `json:"metrics_enabled"`
}

// CorpusSummary is the full curated-corpus result set.
type CorpusSummary struct {
	Manifest       string            `json:"manifest"`
	ScenarioCount  int               `json:"scenario_count"`
	FixtureCount   int               `json:"fixture_count"`
	CategoryCounts map[string]int    `json:"category_counts"`
	Scenarios      []ScenarioMetrics `json:"scenarios"`
}

// Baseline is the complete PR 0 artifact.
type Baseline struct {
	GeneratedAt string            `json:"generated_at"`
	GoVersion   string            `json:"go_version"`
	Buckley     *RepoInfo         `json:"buckley"`
	Canopy      *RepoInfo         `json:"canopy"`
	ToolSchema  ToolSchemaSummary `json:"tool_schema"`
	Corpus      CorpusSummary     `json:"corpus"`
	Config      ConfigSnapshot    `json:"config"`
}

func main() {
	var (
		buckleyDir = flag.String("buckley-dir", "", "path to the buckley repository (default: current directory)")
		canopyDir  = flag.String("canopy-dir", os.Getenv("CTXFABRIC_CANOPY_DIR"), "path to the canopy repository (optional)")
		corpusPath = flag.String("corpus", "testdata/agent_eval/corpus.yaml", "path to the corpus manifest, relative to buckley-dir")
		outPath    = flag.String("out", "", "output JSON path (default: benchmarks/ctxfabric/artifacts/baseline-<timestamp>.json)")
		skipBuild  = flag.Bool("skip-build", false, "skip the go build wall-time/binary-size measurement")
	)
	flag.Parse()

	if *buckleyDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		*buckleyDir = wd
	}

	baseline := Baseline{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		GoVersion:   runtime.Version(),
	}

	baseline.Buckley = inspectRepo("buckley", *buckleyDir)
	baseline.Canopy = inspectRepo("canopy", *canopyDir)

	if !*skipBuild {
		if baseline.Buckley.Available {
			baseline.Buckley.Build = measureBuild(*buckleyDir, "./cmd/buckley")
		}
		if baseline.Canopy.Available {
			baseline.Canopy.Build = measureBuild(*canopyDir, "./cmd/canopy")
		}
	} else {
		if baseline.Buckley.Available {
			baseline.Buckley.Build = &BuildResult{Package: "./cmd/buckley", Skipped: true}
		}
		if baseline.Canopy.Available {
			baseline.Canopy.Build = &BuildResult{Package: "./cmd/canopy", Skipped: true}
		}
	}

	registry := tool.NewRegistry()
	tools := make([]map[string]any, 0)
	for _, t := range registry.List() {
		tools = append(tools, tool.ToOpenAIFunction(t))
	}
	toolPayload, err := json.Marshal(tools)
	if err != nil {
		fatal(fmt.Errorf("marshaling tool schema: %w", err))
	}
	const toolSchemaBudgetBytes = 10 * 1024 // gate: section 32, "<=10 KiB default working set"
	baseline.ToolSchema = ToolSchemaSummary{
		ToolCount:    len(tools),
		PayloadBytes: len(toolPayload),
		BudgetBytes:  toolSchemaBudgetBytes,
		WithinBudget: len(toolPayload) <= toolSchemaBudgetBytes,
	}

	manifestPath := filepath.Join(*buckleyDir, *corpusPath)
	manifest, err := corpus.Load(manifestPath)
	if err != nil {
		fatal(fmt.Errorf("loading corpus manifest: %w", err))
	}
	baseline.Corpus = summarizeCorpus(manifest, manifestPath, tools)

	cfg := config.DefaultConfig()
	baseline.Config = ConfigSnapshot{
		ContextFabricEnabled: cfg.ContextFabric.Enabled,
		ContextFabricShadow:  cfg.ContextFabric.Shadow,
		AgentControllerMode:  cfg.AgentController.Mode,
		MetricsEnabled:       cfg.Metrics.Enabled,
	}

	if *outPath == "" {
		*outPath = filepath.Join(*buckleyDir, "benchmarks", "ctxfabric", "artifacts",
			fmt.Sprintf("baseline-%s.json", time.Now().UTC().Format("20060102-150405")))
	}
	if err := writeJSON(*outPath, baseline); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outPath)
}

func summarizeCorpus(manifest *corpus.Manifest, manifestPath string, tools []map[string]any) CorpusSummary {
	summary := CorpusSummary{
		Manifest:       manifestPath,
		ScenarioCount:  len(manifest.Scenarios),
		CategoryCounts: map[string]int{},
	}
	for _, s := range manifest.Scenarios {
		summary.CategoryCounts[s.Category]++
	}
	for _, s := range manifest.WithFixtures() {
		req, toolCallCount, err := s.BuildRequest(tools)
		if err != nil {
			fatal(fmt.Errorf("building request for scenario %s: %w", s.ID, err))
		}
		compiled, err := json.Marshal(req)
		if err != nil {
			fatal(fmt.Errorf("marshaling request for scenario %s: %w", s.ID, err))
		}
		summary.Scenarios = append(summary.Scenarios, ScenarioMetrics{
			ID:                      s.ID,
			Category:                s.Category,
			Name:                    s.Name,
			ToolCallCount:           toolCallCount,
			MessageCount:            len(req.Messages),
			ContextCompilationBytes: len(compiled),
			TokenEstimate:           model.EstimateRequestTokens(req),
		})
	}
	summary.FixtureCount = len(summary.Scenarios)
	return summary
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating artifacts directory: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling baseline: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ctxfabric bench: "+err.Error())
	os.Exit(1)
}
