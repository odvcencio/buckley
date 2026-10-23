package rules

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/strategy"
)

const compiledRulesPath = "rules/compiled.json.gz"
const compiledRulesVersion = 1

type compiledRuleArchive struct {
	Version int                           `json:"version"`
	Domains map[string]compiledRuleDomain `json:"domains"`
}

type compiledRuleDomain struct {
	SourceSHA256 string                `json:"source_sha256"`
	Ruleset      []byte                `json:"ruleset"`
	IR           *ir.Program           `json:"ir"`
	Segments     []compiledRuleSegment `json:"segments,omitempty"`
	Warnings     []arbiter.Diagnostic  `json:"warnings,omitempty"`
}

type compiledRuleSegment struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Ruleset []byte `json:"ruleset"`
}

// Decode per engine so VM pools and IR indexes remain independent.
func loadCompiledDefaults() (map[string]compiledRuleDomain, error) {
	data, err := defaultRules.ReadFile(compiledRulesPath)
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	var archive compiledRuleArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return nil, err
	}
	if archive.Version != compiledRulesVersion {
		return nil, fmt.Errorf("unsupported compiled rules version %d", archive.Version)
	}
	return archive.Domains, nil
}

func (d compiledRuleDomain) program() (*arbiter.Program, error) {
	if d.IR == nil {
		return nil, fmt.Errorf("compiled rule domain has no IR")
	}
	ruleset, err := bundle.Unmarshal(d.Ruleset)
	if err != nil {
		return nil, err
	}
	segments := govern.NewSegmentSet()
	for _, cached := range d.Segments {
		ruleset, err := bundle.Unmarshal(cached.Ruleset)
		if err != nil {
			return nil, fmt.Errorf("load segment %q: %w", cached.Name, err)
		}
		segments.Add(&govern.CompiledSegment{Name: cached.Name, Source: cached.Source, Ruleset: ruleset})
	}
	d.IR.RebuildIndexes()
	strategies, err := strategy.Compile(d.IR, segments)
	if err != nil {
		return nil, err
	}
	return &arbiter.Program{Ruleset: ruleset, Segments: segments, Strategies: strategies, IR: d.IR, Input: d.IR.Input, Warnings: d.Warnings}, nil
}
