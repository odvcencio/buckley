package commands

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
