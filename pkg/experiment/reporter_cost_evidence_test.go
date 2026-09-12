package experiment

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestTerminalReporter_BarPreservesRequestedFormat(t *testing.T) {
	var out bytes.Buffer
	r := NewTerminalReporterWithOutput(&out, nil)
	r.SetNoColor(true)
	r.renderBar("zero", 0, 1, 3, "%.1f units")
	if !strings.Contains(out.String(), "░░░ 0.0 units") {
		t.Fatalf("requested format replaced: %s", out.String())
	}
}

func TestTerminalReporter_CostChartEvidence(t *testing.T) {
	store := NewStore(setupTestDB(t))
	exp := &Experiment{
		ID: "cost-chart", Name: "cost-chart", Status: ExperimentCompleted,
		Task:     Task{Prompt: "test"},
		Variants: []Variant{{ID: "v", ModelID: "provider/model-with-full-identity"}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(&Run{
		ID: "run-full-identity", ExperimentID: exp.ID, VariantID: "v",
		Status: RunCompleted, Metrics: RunMetrics{TotalCost: 0.125},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := NewComparator(store).Compare(exp)
	if err != nil {
		t.Fatal(err)
	}
	known := report.Variants[0]
	for _, tc := range []struct {
		name   string
		cost   float64
		status RunStatus
	}{
		{"zero", 0, RunCompleted},
		{"negative", -1, RunCompleted},
		{"nan", math.NaN(), RunCompleted},
		{"positive infinity", math.Inf(1), RunCompleted},
		{"negative infinity", math.Inf(-1), RunCompleted},
		{"failed subtotal", 100, RunFailed},
		{"running subtotal", 100, RunRunning},
		{"cancelled subtotal", 100, RunCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unknown := known
			unknown.RunID = "unknown-run"
			unknown.Metrics.TotalCost = tc.cost
			unknown.Status = tc.status
			for _, mixed := range []bool{false, true} {
				variants := []VariantReport{unknown}
				if mixed {
					variants = append(variants, known)
				}
				var out bytes.Buffer
				r := NewTerminalReporterWithOutput(&out, nil)
				r.SetNoColor(true)
				r.renderCostChart(&ComparisonReport{Variants: variants})
				text := out.String()
				if !strings.Contains(text, "provider/model-with-full-identity / unknown-run cost unknown or incomplete (not compared)") {
					t.Fatalf("unknown row lost or mislabeled: %s", text)
				}
				if strings.Contains(text, "$0.00") || strings.Contains(text, "NaN") || strings.Contains(text, "Inf") || strings.Contains(text, "$100") {
					t.Fatalf("invalid or partial cost shown as comparable: %s", text)
				}
				if mixed {
					if !strings.Contains(text, "run-full-identity "+strings.Repeat("█", r.chartBarWidth())+" $0.1250") {
						t.Fatalf("known cost lost or scale contaminated: %s", text)
					}
				} else if !strings.Contains(text, "No comparable cost evidence.") {
					t.Fatalf("missing no-evidence explanation: %s", text)
				}
			}
		})
	}
}
