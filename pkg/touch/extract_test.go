package touch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromJSONRanges(t *testing.T) {
	patch := "diff --git a/foo.txt b/foo.txt\n" +
		"index 0000000..1111111 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -2,0 +3,2 @@\n" +
		"+hello\n" +
		"+world\n"
	raw, err := json.Marshal(map[string]any{"patch": patch})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rich := ExtractFromJSON("apply_patch", string(raw))
	if len(rich.Ranges) != 1 {
		t.Fatalf("ranges=%d want 1", len(rich.Ranges))
	}
	if rich.Ranges[0].Start != 3 || rich.Ranges[0].End != 4 {
		t.Fatalf("range=%+v want 3-4", rich.Ranges[0])
	}
}

func TestExtractFromArgs_PreservesMissingNewlineEvidence(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": path, "content": "new", "old_string": "old", "new_string": "new"}
			rich := ExtractFromArgs(name, args)
			markers := 0
			for _, line := range rich.DiffLines {
				if line.Type == "context" && line.Content == `\ No newline at end of file` {
					markers++
				}
			}
			if markers != 2 || rich.AddedLines != 1 || rich.RemovedLines != 1 {
				t.Fatalf("newline evidence missing: %+v", rich)
			}
			if len(rich.Ranges) != 1 || rich.Ranges[0] != (LineRange{Start: 1, End: 1}) {
				t.Fatalf("range includes phantom file lines: %+v", rich.Ranges)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "old" {
				t.Fatalf("telemetry changed source: %q %v", got, err)
			}
		})
	}
}
