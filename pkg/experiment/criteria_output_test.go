package experiment

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandCriterionDeadlineBoundsInheritedPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := EvaluateCriteria(ctx, t.TempDir(), "", strings.Repeat("source evidence", 10000), []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "sleep 2 & wait"}})
	if len(got) != 1 || got[0].Passed || got[0].Details == "" {
		t.Fatalf("timed-out verifier did not fail explicitly: %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("verifier deadline waited for inherited pipes: %s", elapsed)
	}
}

func TestCommandCriteriaReceiveExactOutputAsData(t *testing.T) {
	root := t.TempDir()
	payload := "  literal \\n\n$(touch SHOULD_NOT_EXIST); `touch SHOULD_NOT_EXIST`\n\x00end  "
	if err := os.WriteFile(filepath.Join(root, "expected.bin"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []CriterionType{CriterionCommand, CriterionTestPass} {
		got := EvaluateCriteria(context.Background(), root, "", payload, []SuccessCriterion{{ID: 1, Type: kind, Target: "cmp - expected.bin"}})
		if len(got) != 1 || !got[0].Passed {
			t.Errorf("%s did not receive exact stdin bytes: %+v", kind, got)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatal("model output was executed instead of passed as data")
	}
	got := EvaluateCriteria(context.Background(), root, "", strings.Repeat(payload, 20000), []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "true"}})
	if len(got) != 1 || !got[0].Passed {
		t.Fatalf("command that ignores stdin failed: %+v", got)
	}
}

func TestSourceOutputCommandCriteriaGateVerifiedWinner(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go is required to build the trusted verifier fixture")
	}
	checker := filepath.Join(t.TempDir(), "source checker")
	if output, err := exec.Command("go", "build", "-o", checker, "testdata/sourcecheck/main.go").CombinedOutput(); err != nil {
		t.Fatalf("build trusted checker: %v\n%s", err, output)
	}
	goldPath, err := filepath.Abs("testdata/sourcecheck/gold.json")
	if err != nil {
		t.Fatal(err)
	}
	gold, err := os.ReadFile(goldPath)
	if err != nil {
		t.Fatal(err)
	}
	live, err := os.ReadFile("testdata/sourcecheck/glm53-output.json")
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(change func([]map[string]any) []map[string]any) string {
		var rows []map[string]any
		if err := json.Unmarshal(gold, &rows); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(change(rows))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	shellQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	command := shellQuote(checker) + " " + shellQuote(goldPath)
	for _, tc := range []struct {
		name, output string
		pass         bool
	}{
		{"complete evidence", string(gold), true},
		{"retained Particle GLM output", string(live), true},
		{"different row order", mutate(func(r []map[string]any) []map[string]any { r[0], r[3] = r[3], r[0]; return r }), true},
		{"wrong citation", mutate(func(r []map[string]any) []map[string]any { r[0]["start_line"] = 52; return r }), false},
		{"altered literal", mutate(func(r []map[string]any) []map[string]any { r[2]["quote"] = "a paraphrase"; return r }), false},
		{"missing item", mutate(func(r []map[string]any) []map[string]any { return r[:3] }), false},
		{"duplicate item", mutate(func(r []map[string]any) []map[string]any { r[1] = r[0]; return r }), false},
		{"unrequested item", mutate(func(r []map[string]any) []map[string]any { r[0]["item"] = "unexpected"; return r }), false},
		{"unchecked field", mutate(func(r []map[string]any) []map[string]any { r[0]["conclusion"] = "unchecked claim"; return r }), false},
		{"trailing prose", string(gold) + "all verified", false},
		{"duplicate JSON field", strings.Replace(string(gold), `"item":"array"`, `"Item":"wrong","item":"array"`, 1), false},
		{"empty output", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(setupTestDB(t))
			exp := &Experiment{ID: "source-output", Name: "source output", Task: Task{Prompt: "read and extract"}, Variants: []Variant{{ID: "v", Name: "worker", ModelID: "modern-model"}}, Criteria: []SuccessCriterion{{Name: "trusted source checker", Type: CriterionCommand, Target: command, Weight: 1}}}
			if err := store.CreateExperiment(exp); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveRun(&Run{ID: "r", ExperimentID: exp.ID, VariantID: "v", Status: RunCompleted, Output: tc.output}); err != nil {
				t.Fatal(err)
			}
			evals := EvaluateCriteria(context.Background(), t.TempDir(), "", tc.output, exp.Criteria)
			if len(evals) != 1 || evals[0].Passed != tc.pass || evals[0].Details == "" {
				t.Fatalf("checker verdict=%+v, want pass=%v", evals, tc.pass)
			}
			if err := store.ReplaceEvaluations("r", evals); err != nil {
				t.Fatal(err)
			}
			report, err := NewComparator(store).Compare(exp)
			if err != nil {
				t.Fatal(err)
			}
			if report.Variants[0].Verified != tc.pass || report.Rankings[0].Winner != tc.pass {
				t.Fatalf("verification did not gate winner: %+v", report)
			}
		})
	}
}

func TestSourceOutputGoldenRowsMatchRepository(t *testing.T) {
	var rows []struct {
		Item, Path, Quote string
		StartLine         int `json:"start_line"`
		EndLine           int `json:"end_line"`
	}
	data, err := os.ReadFile("testdata/sourcecheck/gold.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		data, err := os.ReadFile(filepath.Join("..", "..", row.Path))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		if row.StartLine < 1 || row.EndLine < row.StartLine || row.EndLine > len(lines) || !strings.Contains(strings.Join(lines[row.StartLine-1:row.EndLine], "\n"), row.Quote) {
			t.Errorf("gold item %s no longer matches the cited source", row.Item)
		}
	}
}
