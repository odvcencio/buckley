package gts

import (
	"context"
	"encoding/json"
	"fmt"
)

// Symbol represents a code symbol found by gts.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// CallgraphEdge represents a caller-callee relationship.
type CallgraphEdge struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// MapResult is the output of gts map for a single file.
type MapResult struct {
	File    string   `json:"file"`
	Symbols []Symbol `json:"symbols"`
}

// ScopeResult is the output of gts scope for a location.
type ScopeResult struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	InScope []Symbol `json:"in_scope"`
}

// CallgraphResult is the output of gts callgraph.
type CallgraphResult struct {
	Root  string          `json:"root"`
	Edges []CallgraphEdge `json:"edges"`
	Depth int             `json:"depth"`
}

// ImpactResult is the output of gts impact.
type ImpactResult struct {
	Target  string   `json:"target"`
	Symbols []Symbol `json:"symbols"`
}

// DeadResult is the output of gts dead.
type DeadResult struct {
	Symbols []Symbol `json:"symbols"`
}

// ContextResult is the output of gts context.
type ContextResult struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Symbols []Symbol `json:"symbols"`
}

// Map runs gts map on the given files and returns symbol listings.
func (r *Runner) Map(ctx context.Context, files ...string) ([]MapResult, error) {
	args := append([]string{"map", "--json"}, files...)
	out, err := r.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("gts map: %w", err)
	}
	var results []MapResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("gts map: parsing output: %w", err)
	}
	return results, nil
}

// Scope runs gts scope at a file:line location.
func (r *Runner) Scope(ctx context.Context, file string, line int) (*ScopeResult, error) {
	location := fmt.Sprintf("%s:%d", file, line)
	out, err := r.Run(ctx, "scope", "--json", location)
	if err != nil {
		return nil, fmt.Errorf("gts scope: %w", err)
	}
	var result ScopeResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("gts scope: parsing output: %w", err)
	}
	return &result, nil
}

// Callgraph runs gts callgraph from a root symbol.
func (r *Runner) Callgraph(ctx context.Context, root string) (*CallgraphResult, error) {
	out, err := r.Run(ctx, "callgraph", "--json", root)
	if err != nil {
		return nil, fmt.Errorf("gts callgraph: %w", err)
	}
	var result CallgraphResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("gts callgraph: parsing output: %w", err)
	}
	return &result, nil
}

// Dead runs gts dead to find unreachable symbols.
func (r *Runner) Dead(ctx context.Context) ([]Symbol, error) {
	out, err := r.Run(ctx, "dead", "--json")
	if err != nil {
		return nil, fmt.Errorf("gts dead: %w", err)
	}
	var result DeadResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("gts dead: parsing output: %w", err)
	}
	return result.Symbols, nil
}

// Context runs gts context for a file:line location.
func (r *Runner) Context(ctx context.Context, file string, line int) (*ContextResult, error) {
	location := fmt.Sprintf("%s:%d", file, line)
	out, err := r.Run(ctx, "context", "--json", location)
	if err != nil {
		return nil, fmt.Errorf("gts context: %w", err)
	}
	var result ContextResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("gts context: parsing output: %w", err)
	}
	return &result, nil
}

// Impact runs gts impact for a target symbol or file.
func (r *Runner) Impact(ctx context.Context, target string) ([]Symbol, error) {
	out, err := r.Run(ctx, "impact", "--json", target)
	if err != nil {
		return nil, fmt.Errorf("gts impact: %w", err)
	}
	var result ImpactResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("gts impact: parsing output: %w", err)
	}
	return result.Symbols, nil
}
