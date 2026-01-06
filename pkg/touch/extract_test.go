package touch

import (
	"encoding/json"
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
