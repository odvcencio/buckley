package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type repairTestDefinition struct{ oneshot.Definition }

func (repairTestDefinition) ContextSources() []oneshot.ContextSource { return nil }
func (repairTestDefinition) BuildPrompt(*oneshot.Context) string     { return "generate" }
func (repairTestDefinition) SystemPrompt() string                    { return "generate" }
func (d repairTestDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	return d.Definition.(oneshot.RepairableDefinition).Repair(raw)
}

type repairTestInvoker struct {
	raw   json.RawMessage
	calls int
}

func (i *repairTestInvoker) Invoke(context.Context, string, string, tools.Definition, *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	i.calls++
	return &oneshot.Result{ToolCall: &tools.ToolCall{Arguments: i.raw}}, nil, nil
}

func TestHeaderRepair_PreservesOtherFields(t *testing.T) {
	for _, tt := range []struct {
		name, key string
		def       oneshot.Definition
	}{
		{"commit", "subject", CommitDefinition{}}, {"pr", "title", PRDefinition{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]any{"action": "fix", "scope": "project", tt.key: strings.Repeat("word ", 30),
				"body": []string{"  - Keep spacing  and Unicode 界.  "}, "breaking_reason": "Keep exact details.", "breaking": true,
				"issues": []string{"0012"}, "summary": "  Keep\ntext\r\nexact.  ", "changes": []string{"Keep <tags> & spaces.  "},
				"trailers": "Signed-off-by: Name <name@example.com>\r\nRefs: #12\n"}
			raw, _ := json.Marshal(fields)
			repaired, repairs := tt.def.(oneshot.RepairableDefinition).Repair(raw)
			if len(repairs) == 0 {
				t.Fatal("no repair")
			}
			if err := tt.def.Validate(repaired); err != nil {
				t.Fatal(err)
			}
			var before, after map[string]json.RawMessage
			_ = json.Unmarshal(raw, &before)
			_ = json.Unmarshal(repaired, &after)
			for key, value := range before {
				if key == "scope" || key == tt.key {
					continue
				}
				if !reflect.DeepEqual(value, after[key]) {
					t.Fatalf("changed %s: %s -> %s", key, value, after[key])
				}
			}
			// The rendered body and trailers also remain byte-exact through repair.
			original, _ := tt.def.Unmarshal(raw)
			fixed, _ := tt.def.Unmarshal(repaired)
			if tt.name == "commit" {
				_, a, _ := strings.Cut(original.(*CommitResult).Format(), "\n")
				_, b, _ := strings.Cut(fixed.(*CommitResult).Format(), "\n")
				if a != b {
					t.Fatal("changed rendered commit body or trailers")
				}
			} else if original.(*PRResult).FormatBody() != fixed.(*PRResult).FormatBody() {
				t.Fatal("changed PR body")
			}
			invoker := &repairTestInvoker{raw: raw}
			framework := oneshot.NewFramework(invoker, nil).WithValidationFallbacks(func() (oneshot.ToolInvoker, error) { t.Fatal("mechanical error escalated"); return nil, nil })
			result, err := framework.Run(context.Background(), repairTestDefinition{tt.def}, oneshot.RunOpts{})
			if err != nil || result.Attempts != 1 || invoker.calls != 1 {
				t.Fatalf("result=%+v calls=%d err=%v", result, invoker.calls, err)
			}
		})
	}
}

func TestHeaderRepair_RejectsMixedFailures(t *testing.T) {
	for _, def := range []oneshot.Definition{CommitDefinition{}, PRDefinition{}} {
		raw := json.RawMessage(`{"action":"fix","scope":"project","subject":"` + strings.Repeat("word ", 30) + `","title":"` + strings.Repeat("word ", 30) + `"}`)
		repaired, repairs := def.(oneshot.RepairableDefinition).Repair(raw)
		if string(repaired) != string(raw) || len(repairs) != 0 {
			t.Fatalf("%s repaired a payload with invalid body", def.Name())
		}
	}
}

func TestPRDefinition_UnicodeTitle(t *testing.T) {
	raw, _ := json.Marshal(PRResult{Action: "fix", Title: strings.Repeat("界", 95), Summary: "Summary", Changes: []string{"Change"}})
	if err := (PRDefinition{}).Validate(raw); err != nil {
		t.Fatal(err)
	}
}
