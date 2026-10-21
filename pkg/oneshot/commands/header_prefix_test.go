package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func TestHeaderRepair_RepeatedPrefix(t *testing.T) {
	for _, def := range []oneshot.Definition{CommitDefinition{}, PRDefinition{}} {
		for _, subject := range []string{
			"fix(clamp): clamp negative values to zero",
			"fix: clamp negative values to zero",
			"FIX(clamp): clamp negative values to zero",
			"fix(clamp): fix: clamp negative values to zero",
		} {
			t.Run(def.Name()+"/"+subject, func(t *testing.T) {
				fields := map[string]any{"action": "fix", "scope": "clamp", "subject": subject, "title": subject,
					"body": []string{"Clamp negative inputs to zero."}, "summary": "Keep values in range.",
					"changes": []string{"Clamp negative inputs to zero."}, "breaking": true,
					"breaking_reason": "Keep the declared compatibility impact.", "issues": []string{"0012"}}
				raw, _ := json.Marshal(fields)
				if err := def.Validate(raw); err == nil {
					t.Fatal("accepted a nested header prefix")
				}
				repaired, repairs := def.(oneshot.RepairableDefinition).Repair(raw)
				if len(repairs) == 0 || def.Validate(repaired) != nil {
					t.Fatalf("prefix was not repaired: %s repairs=%v", repaired, repairs)
				}
				key := "subject"
				if def.Name() == "pr" {
					key = "title"
				}
				var before, after map[string]json.RawMessage
				_ = json.Unmarshal(raw, &before)
				_ = json.Unmarshal(repaired, &after)
				for name, value := range before {
					if name != key && !reflect.DeepEqual(value, after[name]) {
						t.Fatalf("changed %s: %s -> %s", name, value, after[name])
					}
				}
				var fixedSubject string
				_ = json.Unmarshal(after[key], &fixedSubject)
				if fixedSubject != "clamp negative values to zero" {
					t.Fatalf("subject=%q", fixedSubject)
				}
				again, more := def.(oneshot.RepairableDefinition).Repair(repaired)
				if string(again) != string(repaired) || len(more) != 0 {
					t.Fatal("repair is not idempotent")
				}
				invoker := &repairTestInvoker{raw: raw}
				framework := oneshot.NewFramework(invoker, nil).WithValidationFallbacks(func() (oneshot.ToolInvoker, error) {
					t.Fatal("matching prefix escalated to another model")
					return nil, nil
				})
				result, err := framework.Run(context.Background(), repairTestDefinition{def}, oneshot.RunOpts{})
				if err != nil || result.Attempts != 1 || invoker.calls != 1 {
					t.Fatalf("result=%+v calls=%d err=%v", result, invoker.calls, err)
				}
				var header string
				switch value := result.Value.(type) {
				case *CommitResult:
					header = value.Header()
				case *PRResult:
					header = value.Header()
				}
				if header != "fix(clamp): clamp negative values to zero" {
					t.Fatalf("header=%q", header)
				}
				t.Logf("operation=%s model_calls=%d attempts=%d header=%q", def.Name(), invoker.calls, result.Attempts, header)
			})
		}
	}
}

func TestHeaderRepair_PrefixConflicts(t *testing.T) {
	for _, def := range []oneshot.Definition{CommitDefinition{}, PRDefinition{}} {
		for _, subject := range []string{
			"update(clamp): clamp negative values to zero",
			"fix(other): clamp negative values to zero",
			"fix(clamp)!: clamp negative values to zero",
			"fix(clamp): update: clamp negative values to zero",
			"fix(clamp):",
			"fix(clamp): fix the clamp",
		} {
			if def.Name() == "pr" && strings.HasSuffix(subject, "fix the clamp") {
				continue
			}
			t.Run(def.Name()+"/"+subject, func(t *testing.T) {
				fields := map[string]any{"action": "fix", "scope": "clamp", "subject": subject, "title": subject,
					"body": []string{"Clamp values."}, "summary": "Keep values in range.", "changes": []string{"Clamp values."}}
				raw, _ := json.Marshal(fields)
				if err := def.Validate(raw); err == nil {
					t.Fatal("accepted a conflicting or incomplete prefix")
				}
				repaired, repairs := def.(oneshot.RepairableDefinition).Repair(raw)
				if string(repaired) != string(raw) || len(repairs) != 0 {
					t.Fatalf("changed semantic content: %s repairs=%v", repaired, repairs)
				}
			})
		}
	}
}
