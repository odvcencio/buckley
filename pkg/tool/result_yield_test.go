package tool

import (
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestResultYieldForTool_RepositoryExplorationCounts(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		result   *builtin.Result
		execErr  error
		want     ResultYield
		summary  string
	}{
		{
			name:     "zero search matches remain successful",
			toolName: "search_text",
			result:   &builtin.Result{Success: true, Data: map[string]any{"count": 0}},
			want:     ResultYield{Observed: true, Count: 0, Unit: "match"},
			summary:  "0 matches",
		},
		{
			name:     "one file uses singular form",
			toolName: "find_files",
			result:   &builtin.Result{Success: true, Data: map[string]any{"count": 1}},
			want:     ResultYield{Observed: true, Count: 1, Unit: "file"},
			summary:  "1 file",
		},
		{
			name:     "directory entries use plural spelling",
			toolName: "list_directory",
			result:   &builtin.Result{Success: true, Data: map[string]any{"count": float64(2)}},
			want:     ResultYield{Observed: true, Count: 2, Unit: "entry"},
			summary:  "2 entries",
		},
		{
			name:     "failed query has no yield",
			toolName: "search_text",
			result:   &builtin.Result{Success: false, Error: "missing"},
			want:     ResultYield{},
		},
		{
			name:     "execution error has no yield",
			toolName: "search_text",
			result:   &builtin.Result{Success: true, Data: map[string]any{"count": 0}},
			execErr:  errors.New("interrupted"),
			want:     ResultYield{},
		},
		{
			name:     "unrelated tool is not guessed",
			toolName: "read_file",
			result:   &builtin.Result{Success: true, Data: map[string]any{"count": 0}},
			want:     ResultYield{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResultYieldForTool(tt.toolName, tt.result, tt.execErr)
			if got != tt.want {
				t.Fatalf("ResultYieldForTool() = %+v, want %+v", got, tt.want)
			}
			if summary := got.Summary(); summary != tt.summary {
				t.Fatalf("Summary() = %q, want %q", summary, tt.summary)
			}
		})
	}
}
