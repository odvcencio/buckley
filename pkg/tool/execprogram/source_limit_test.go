package execprogram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
)

type sourceLimitEvidence struct {
	evidence.Store
	object evidence.Object
	puts   int
}

func (s *sourceLimitEvidence) Get(context.Context, string) (evidence.Object, error) {
	return s.object, nil
}

func (s *sourceLimitEvidence) Put(context.Context, evidence.Object) (evidence.Object, error) {
	s.puts++
	return evidence.Object{}, errors.New("unexpected source evidence write")
}

func TestProgramSourceBudget_NoEvidenceWrite(t *testing.T) {
	source := strings.Repeat("x", execmode.MaxSourceBytes+1)
	for _, language := range []string{"go", "fw"} {
		for _, reuse := range []bool{false, true} {
			t.Run(language+"_reuse_"+strconv.FormatBool(reuse), func(t *testing.T) {
				store := &sourceLimitEvidence{object: evidence.Object{InlineBody: []byte(source), Metadata: map[string]any{"language": language}}}
				program := &ProgramTool{evidence: store}
				params := map[string]any{"source": source, "language": language}
				if reuse {
					params = map[string]any{"reuse": "ev_oversized"}
				}
				result, err := program.ExecuteWithContext(context.Background(), params)
				if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "source exceeds") || store.puts != 0 {
					t.Fatalf("result=%v err=%v evidence writes=%d", result, err, store.puts)
				}
			})
		}
	}
}
