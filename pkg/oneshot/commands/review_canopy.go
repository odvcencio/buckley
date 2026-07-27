package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxCanopyReviewInput  = 4 << 20
	maxCanopyHotspots     = 8
	maxCanopyCapabilities = 8
	maxCanopyProjectBytes = 24 << 10
	canopyReviewTimeout   = 12 * time.Second
	canopyProjectTimeout  = 45 * time.Second
	canopyPipeWaitDelay   = 2 * time.Second
)

type canopyReviewEvidence struct {
	Output           string
	Status           string
	Runtime          time.Duration
	IndexScope       string
	IndexScopeSource string
}

type canopyReviewReport struct {
	Base            string                    `json:"base"`
	ChangedFiles    int                       `json:"changed_files"`
	IndexScope      string                    `json:"index_scope"`
	ComplexityDelta []canopyComplexityHotspot `json:"complexity_delta"`
	NewCapabilities []canopyReviewCapability  `json:"new_capabilities"`
	BlastRadius     int                       `json:"blast_radius"`
}

type canopyComplexityHotspot struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	Cyclomatic int    `json:"cyclomatic"`
	Cognitive  int    `json:"cognitive"`
	Lines      int    `json:"lines"`
}

type canopyReviewCapability struct {
	Name       string `json:"name"`
	Category   string `json:"category"`
	Confidence string `json:"confidence"`
	AttackID   string `json:"attack_id,omitempty"`
}

type compactCanopyReview struct {
	Base               string                    `json:"base"`
	ChangedFiles       int                       `json:"changed_files"`
	IndexScope         string                    `json:"index_scope"`
	IndexScopeSource   string                    `json:"index_scope_source,omitempty"`
	BlastRadius        int                       `json:"blast_radius"`
	ComplexityHotspots []canopyComplexityHotspot `json:"complexity_hotspots,omitempty"`
	Capabilities       []canopyReviewCapability  `json:"credible_capabilities,omitempty"`
}

func collectCanopyReview(parent context.Context, repoRoot, baseCommit string) (string, string) {
	evidence := collectCanopyReviewEvidence(parent, repoRoot, baseCommit)
	return evidence.Output, evidence.Status
}

func collectCanopyReviewEvidence(parent context.Context, repoRoot, baseCommit string) canopyReviewEvidence {
	executable, err := findCanopyExecutable()
	if err != nil {
		return canopyReviewEvidence{Status: "not installed"}
	}
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return canopyReviewEvidence{Status: "base commit unavailable"}
	}

	ctx, cancel := context.WithTimeout(nonNilReviewContext(parent), canopyReviewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "analyze", "review", "--base", baseCommit, "--json", ".")
	cmd.Dir = repoRoot
	cmd.WaitDelay = canopyPipeWaitDelay
	started := time.Now()
	output, err := cmd.Output()
	elapsed := time.Since(started)
	if ctx.Err() != nil {
		return canopyReviewEvidence{Status: "timed out", Runtime: elapsed}
	}
	if err != nil {
		return canopyReviewEvidence{Status: "analysis failed", Runtime: elapsed}
	}
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return canopyReviewEvidence{Status: "returned no evidence", Runtime: elapsed}
	}
	if len(output) > maxCanopyReviewInput {
		return canopyReviewEvidence{Status: "evidence exceeded input limit", Runtime: elapsed}
	}
	var report canopyReviewReport
	if err := json.Unmarshal(output, &report); err != nil {
		return canopyReviewEvidence{Status: "returned invalid JSON", Runtime: elapsed}
	}
	report.Base = strings.TrimSpace(report.Base)
	if report.Base != baseCommit {
		return canopyReviewEvidence{Status: "base revision mismatch", Runtime: elapsed}
	}
	indexScope, indexScopeSource, valid := resolveCanopyIndexScope(report.IndexScope)
	if !valid {
		return canopyReviewEvidence{Status: "index scope unavailable", Runtime: elapsed}
	}
	compact := compactCanopyReview{
		Base:               report.Base,
		ChangedFiles:       report.ChangedFiles,
		IndexScope:         indexScope,
		IndexScopeSource:   indexScopeSource,
		BlastRadius:        report.BlastRadius,
		ComplexityHotspots: selectCanopyHotspots(report.ComplexityDelta),
		Capabilities:       selectCredibleCanopyCapabilities(report.NewCapabilities),
	}
	output, err = json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return canopyReviewEvidence{Status: "could not compact evidence", Runtime: elapsed}
	}
	return canopyReviewEvidence{
		Output:           string(output),
		Status:           "available",
		Runtime:          elapsed,
		IndexScope:       indexScope,
		IndexScopeSource: indexScopeSource,
	}
}

func resolveCanopyIndexScope(value string) (scope, source string, valid bool) {
	switch scope = strings.TrimSpace(value); scope {
	case "":
		return "repository", "repository-root invocation", true
	case "changed", "repository":
		return scope, "Canopy report", true
	default:
		return "", "", false
	}
}

func selectCanopyHotspots(all []canopyComplexityHotspot) []canopyComplexityHotspot {
	selected := make([]canopyComplexityHotspot, 0, len(all))
	for _, hotspot := range all {
		if hotspot.Cognitive < 15 && hotspot.Cyclomatic < 10 && hotspot.Lines < 100 {
			continue
		}
		selected = append(selected, hotspot)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		switch {
		case left.Cognitive != right.Cognitive:
			return left.Cognitive > right.Cognitive
		case left.Cyclomatic != right.Cyclomatic:
			return left.Cyclomatic > right.Cyclomatic
		case left.Lines != right.Lines:
			return left.Lines > right.Lines
		case left.File != right.File:
			return left.File < right.File
		default:
			return left.Name < right.Name
		}
	})
	if len(selected) > maxCanopyHotspots {
		selected = selected[:maxCanopyHotspots]
	}
	return selected
}

func selectCredibleCanopyCapabilities(all []canopyReviewCapability) []canopyReviewCapability {
	selected := make([]canopyReviewCapability, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, capability := range all {
		capability.Name = strings.TrimSpace(capability.Name)
		capability.Category = strings.TrimSpace(capability.Category)
		capability.Confidence = strings.ToLower(strings.TrimSpace(capability.Confidence))
		capability.AttackID = strings.TrimSpace(capability.AttackID)
		if capability.Confidence != "high" || capability.Name == "" {
			continue
		}
		key := capability.Name + "\x00" + capability.Category + "\x00" + capability.AttackID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, capability)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		switch {
		case left.Category != right.Category:
			return left.Category < right.Category
		case left.Name != right.Name:
			return left.Name < right.Name
		default:
			return left.AttackID < right.AttackID
		}
	})
	if len(selected) > maxCanopyCapabilities {
		selected = selected[:maxCanopyCapabilities]
	}
	return selected
}

func collectCanopyProjectSummary(parent context.Context, repoRoot string) (string, string) {
	executable, err := findCanopyExecutable()
	if err != nil {
		return "", "not installed"
	}

	ctx, cancel := context.WithTimeout(nonNilReviewContext(parent), canopyProjectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "analyze", "summary", "--json", ".")
	cmd.Dir = repoRoot
	cmd.WaitDelay = canopyPipeWaitDelay
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", "timed out after 45s"
	}
	if err != nil {
		return "", "analysis failed"
	}
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return "", "returned no evidence"
	}
	if len(output) > maxCanopyProjectBytes {
		output = append(output[:maxCanopyProjectBytes], []byte("\n... (truncated)")...)
	}
	status := "available"
	if note := strings.TrimSpace(stderr.String()); note != "" {
		status += "; " + compactCanopyStatus(note, 200)
	}
	return string(output), status
}

func nonNilReviewContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func compactCanopyStatus(value string, maxLen int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

func findCanopyExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CANOPY_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("CANOPY_BIN is not executable")
	}
	if path, err := exec.LookPath("canopy"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "canopy")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("canopy executable not found")
}
