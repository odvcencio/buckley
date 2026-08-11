package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCollectCanopyProjectSummaryBuildsStructuralTOC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	tempDir := t.TempDir()
	helper := filepath.Join(tempDir, "canopy")
	script := `#!/bin/sh
case "$1:$2" in
  index:build)
    out=""
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "--out" ]; then out="$arg"; fi
      previous="$arg"
    done
    if [ -n "$out" ]; then : > "$out"; fi
    exit 0
    ;;
  analyze:summary)
    printf '%s\n' '{"files":{"total":12,"generated":1,"by_language":{"go":11}},"symbols":{"total":40},"references":{"total":90},"parse_errors":{"count":0},"call_graph":{"total_edges":70,"unresolved":4,"resolution_rate":0.94},"complexity":{"avg_cyclomatic":3.2,"max_cyclomatic":18,"p90_cyclomatic":8,"avg_cognitive":4.1,"max_cognitive":27},"top_fan_in":[{"file":"pkg/a.go","name":"Start","start_line":10,"value":21},{"file":"pkg/b.go","name":"Serve","start_line":20,"value":11}],"top_complex":[{"file":"pkg/c.go","name":"Validate","start_line":30,"value":0,"cyclomatic":18,"cognitive":27,"lines":88}]}'
    ;;
  graph:calls)
    reverse=false
    for arg in "$@"; do
      if [ "$arg" = "--reverse" ]; then reverse=true; fi
    done
    if $reverse; then
      printf '%s\n' '{"roots":[{"id":"root","file":"pkg/a.go","package":"p","kind":"function_definition","name":"Start","signature":"func Start()","start_line":10,"end_line":15}],"nodes":[],"edges":[{"caller":{"id":"caller","file":"pkg/entry.go","package":"p","kind":"function_definition","name":"Main","start_line":3},"callee":{"id":"root","file":"pkg/a.go","package":"p","kind":"function_definition","name":"Start","start_line":10},"resolution":"package","count":2,"samples":[{"file":"pkg/entry.go","start_line":4}]}]}'
    else
      printf '%s\n' '{"roots":[{"id":"root","file":"pkg/a.go","package":"p","kind":"function_definition","name":"Start","signature":"func Start()","start_line":10,"end_line":15}],"nodes":[],"edges":[{"caller":{"id":"root","file":"pkg/a.go","package":"p","kind":"function_definition","name":"Start","start_line":10},"callee":{"id":"callee","file":"pkg/service.go","package":"p","kind":"function_definition","name":"Run","start_line":44},"resolution":"package","count":1,"samples":[{"file":"pkg/a.go","start_line":12}]}]}'
    fi
    ;;
esac
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)

	output, status := collectCanopyProjectSummary(context.Background(), tempDir)
	if status != "available" {
		t.Fatalf("status = %q, want available", status)
	}
	if len(output) > maxCanopyProjectBytes {
		t.Fatalf("TOC length = %d, want <= %d", len(output), maxCanopyProjectBytes)
	}
	var toc canopyProjectTOC
	if err := json.Unmarshal([]byte(output), &toc); err != nil {
		t.Fatalf("decode structural TOC: %v\n%s", err, output)
	}
	if toc.Version != 1 {
		t.Fatalf("TOC version = %d, want 1", toc.Version)
	}
	if toc.Summary.Files.Total != 12 || toc.Summary.CallGraph.TotalEdges != 70 {
		t.Fatalf("summary = %#v", toc.Summary)
	}
	if len(toc.MajorCallSites) != 2 {
		t.Fatalf("major call sites = %d, want 2", len(toc.MajorCallSites))
	}
	if len(toc.ImportantFlows) != 3 {
		t.Fatalf("important flows = %d, want 3", len(toc.ImportantFlows))
	}
	flow := toc.ImportantFlows[0]
	if flow.Status != "available" || len(flow.Callers) != 1 || len(flow.Callees) != 1 {
		t.Fatalf("first flow = %#v", flow)
	}
	if flow.Callers[0].File != "pkg/entry.go" || flow.Callees[0].File != "pkg/service.go" {
		t.Fatalf("flow edges = %#v", flow)
	}
	if !toc.Coverage.CallGraphsComplete || toc.Coverage.FlowsCollected != 3 {
		t.Fatalf("coverage = %#v", toc.Coverage)
	}
}

func TestCollectCanopyReviewEvidenceUsesCacheAndReportsMetrics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	tempDir := t.TempDir()
	helper := filepath.Join(tempDir, "canopy")
	argsFile := filepath.Join(tempDir, "args")
	countFile := filepath.Join(tempDir, "count")
	script := `#!/bin/sh
printf 'run\n' >> "$CANOPY_COUNT_FILE"
printf '%s\n' "$@" > "$CANOPY_ARGS_FILE"
printf '{"base":"base-sha","changed_files":1,"files":["a.go"],"index_scope":"changed","blast_radius":2,"complexity_delta":[{"file":"a.go","name":"small","cyclomatic":2,"cognitive":3,"lines":12},{"file":"a.go","name":"hot","cyclomatic":12,"cognitive":18,"lines":80}],"new_capabilities":[{"name":"Medium signal","category":"network","confidence":"medium"},{"name":"High signal","category":"execution","confidence":"high","attack_id":"T1001"}]}\n'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)
	t.Setenv("CANOPY_ARGS_FILE", argsFile)
	t.Setenv("CANOPY_COUNT_FILE", countFile)

	evidence := collectCanopyReviewEvidence(context.Background(), tempDir, "base-sha")
	if evidence.Status != "available" {
		t.Fatalf("status = %q, want available", evidence.Status)
	}
	if evidence.IndexScope != "changed" {
		t.Fatalf("index scope = %q, want changed", evidence.IndexScope)
	}
	if evidence.IndexScopeSource != "Canopy report" {
		t.Fatalf("index scope source = %q, want Canopy report", evidence.IndexScopeSource)
	}
	if evidence.BlastRadius != 2 {
		t.Fatalf("blast radius = %d, want 2", evidence.BlastRadius)
	}
	if evidence.Runtime <= 0 {
		t.Fatalf("runtime = %s, want a measured duration", evidence.Runtime)
	}
	if !strings.Contains(evidence.Output, `"blast_radius": 2`) {
		t.Fatalf("output = %q", evidence.Output)
	}
	for _, want := range []string{`"complexity_hotspots"`, `"name": "hot"`, `"credible_capabilities"`, `"name": "High signal"`} {
		if !strings.Contains(evidence.Output, want) {
			t.Errorf("compact evidence missing %q:\n%s", want, evidence.Output)
		}
	}
	for _, unwanted := range []string{`"name": "small"`, `"name": "Medium signal"`, `"files"`} {
		if strings.Contains(evidence.Output, unwanted) {
			t.Errorf("compact evidence contains %q:\n%s", unwanted, evidence.Output)
		}
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read arguments: %v", err)
	}
	wantArgs := "analyze\nreview\n--base\nbase-sha\n--json\n.\n"
	if string(args) != wantArgs {
		t.Fatalf("arguments = %q, want %q", args, wantArgs)
	}
	count, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read invocation count: %v", err)
	}
	if string(count) != "run\n" {
		t.Fatalf("invocations = %q, want one", count)
	}
}

func TestCollectCanopyReviewEvidenceAcceptsLegacyRepositoryReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	tempDir := t.TempDir()
	helper := filepath.Join(tempDir, "canopy")
	script := `#!/bin/sh
printf '{"base":"base-sha","changed_files":15,"blast_radius":1733,"complexity_delta":[{"file":"parser.go","name":"parse","cyclomatic":31,"cognitive":39,"lines":98}]}\n'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)

	evidence := collectCanopyReviewEvidence(context.Background(), tempDir, "base-sha")
	if evidence.Status != "available" {
		t.Fatalf("status = %q, want available", evidence.Status)
	}
	if evidence.IndexScope != "repository" {
		t.Fatalf("index scope = %q, want repository", evidence.IndexScope)
	}
	if evidence.IndexScopeSource != "repository-root invocation" {
		t.Fatalf("index scope source = %q, want repository-root invocation", evidence.IndexScopeSource)
	}
	if evidence.BlastRadius != 1733 {
		t.Fatalf("blast radius = %d, want 1733", evidence.BlastRadius)
	}
	for _, want := range []string{
		`"changed_files": 15`,
		`"index_scope": "repository"`,
		`"index_scope_source": "repository-root invocation"`,
		`"blast_radius": 1733`,
		`"name": "parse"`,
	} {
		if !strings.Contains(evidence.Output, want) {
			t.Errorf("compact evidence missing %q:\n%s", want, evidence.Output)
		}
	}
}

func TestCollectCanopyReviewEvidenceRejectsInvalidExplicitScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	tempDir := t.TempDir()
	helper := filepath.Join(tempDir, "canopy")
	script := `#!/bin/sh
printf '{"base":"base-sha","index_scope":"workspace"}\n'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)

	evidence := collectCanopyReviewEvidence(context.Background(), tempDir, "base-sha")
	if evidence.Status != "index scope unavailable" {
		t.Fatalf("status = %q, want index scope unavailable", evidence.Status)
	}
	if evidence.Output != "" {
		t.Fatalf("output = %q, want empty", evidence.Output)
	}
}

func TestCollectCanopyReviewBoundsInheritedPipeWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	helper := filepath.Join(t.TempDir(), "canopy")
	script := "#!/bin/sh\n(sleep 5) &\nwait\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, status := collectCanopyReview(ctx, t.TempDir(), "HEAD")
	if status != "timed out" {
		t.Fatalf("status = %q, want timed out", status)
	}
	if elapsed := time.Since(started); elapsed > canopyPipeWaitDelay+time.Second {
		t.Fatalf("collectCanopyReview returned after %s", elapsed)
	}
}
