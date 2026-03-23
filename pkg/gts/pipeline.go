package gts

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/rules"
)

// Pipeline orchestrates arbiter-governed gts context enrichment.
type Pipeline struct {
	runner  *Runner
	engine  *rules.Engine
	cache   *IndexCache
	lastOOM bool
}

// ContextEnrichment holds the collected gts analysis results.
type ContextEnrichment struct {
	Map       []MapResult
	Scope     *ScopeResult
	Callgraph *CallgraphResult
	DeadCode  []Symbol
	Impact    []Symbol
	Degraded  bool
}

// NewPipeline creates a pipeline with the given runner, rules engine, and cache.
func NewPipeline(runner *Runner, engine *rules.Engine, cache *IndexCache) *Pipeline {
	return &Pipeline{
		runner: runner,
		engine: engine,
		cache:  cache,
	}
}

// LastOOM returns whether the most recent enrichment encountered an OOM.
func (p *Pipeline) LastOOM() bool {
	return p.lastOOM
}

// Enrich runs arbiter-governed gts context enrichment for the given files.
//
// Layer 1: Always runs gts map on files (baseline).
// Layer 2: Evaluates gts_context arbiter domain with facts.
//   - Action "enrich": runs tools from the Params["tools"] list.
//   - Action "baseline_only": skips deep analysis.
//
// On OOM, sets lastOOM=true and returns a degraded enrichment.
func (p *Pipeline) Enrich(ctx context.Context, facts rules.GTSFacts, files []string) (*ContextEnrichment, error) {
	// Feed back OOM state from previous run
	facts.LastOOM = p.lastOOM

	enrichment := &ContextEnrichment{}

	// Ensure index is fresh
	if err := p.cache.EnsureFresh(ctx, p.runner); err != nil {
		// Index failure is non-fatal; continue with degraded results
		enrichment.Degraded = true
	}

	// Layer 1: always run gts map (baseline)
	if len(files) > 0 {
		mapResults, err := p.runner.Map(ctx, files...)
		if err != nil {
			if IsOOM(err) {
				p.lastOOM = true
				enrichment.Degraded = true
				return enrichment, nil
			}
			// Non-OOM map failure is still non-fatal
			enrichment.Degraded = true
		} else {
			enrichment.Map = mapResults
		}
	}

	// Layer 2: eval arbiter rules
	matched, err := rules.Eval(p.engine, "gts_context", facts)
	if err != nil {
		return enrichment, fmt.Errorf("evaluating gts_context rules: %w", err)
	}

	// Use highest-priority match
	if len(matched) == 0 {
		return enrichment, nil
	}
	top := matched[0]

	if top.Action != "enrich" {
		// baseline_only or other non-enrich actions: return map-only results
		return enrichment, nil
	}

	// Run tools specified by the arbiter
	toolList := extractToolList(top.Params)
	for _, tool := range toolList {
		if err := p.runTool(ctx, enrichment, tool, files); err != nil {
			if IsOOM(err) {
				p.lastOOM = true
				enrichment.Degraded = true
				return enrichment, nil
			}
			// Non-OOM tool failures are non-fatal; skip this tool
			continue
		}
	}

	// Successful enrichment clears OOM flag
	p.lastOOM = false
	return enrichment, nil
}

func (p *Pipeline) runTool(ctx context.Context, enrichment *ContextEnrichment, tool string, files []string) error {
	switch tool {
	case "scope":
		if len(files) > 0 {
			result, err := p.runner.Scope(ctx, files[0], 1)
			if err != nil {
				return err
			}
			enrichment.Scope = result
		}
	case "callgraph":
		// Use first file as root for callgraph
		if len(files) > 0 {
			result, err := p.runner.Callgraph(ctx, files[0])
			if err != nil {
				return err
			}
			enrichment.Callgraph = result
		}
	case "dead":
		symbols, err := p.runner.Dead(ctx)
		if err != nil {
			return err
		}
		enrichment.DeadCode = symbols
	case "impact":
		if len(files) > 0 {
			symbols, err := p.runner.Impact(ctx, files[0])
			if err != nil {
				return err
			}
			enrichment.Impact = symbols
		}
	case "context":
		// gts context enriches scope at a specific location
		if len(files) > 0 {
			_, err := p.runner.Context(ctx, files[0], 1)
			if err != nil {
				return err
			}
		}
	case "hotspot":
		// hotspot is an alias; run impact on all files
		for _, f := range files {
			symbols, err := p.runner.Impact(ctx, f)
			if err != nil {
				return err
			}
			enrichment.Impact = append(enrichment.Impact, symbols...)
		}
	}
	return nil
}

// extractToolList pulls the tools list from arbiter Params.
func extractToolList(params map[string]any) []string {
	raw, ok := params["tools"]
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case []any:
		tools := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				tools = append(tools, s)
			}
		}
		return tools
	case []string:
		return v
	default:
		return nil
	}
}
