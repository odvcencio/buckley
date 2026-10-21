package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSelectCanopyProjectSeeds_SeparatesAndEnrichesMetricKinds(t *testing.T) {
	integer := func(value int) *int { return &value }
	for _, tc := range []struct {
		name    string
		summary string
		want    []canopyProjectSeed
	}{
		{
			"native_value_fields",
			`{"top_fan_in":[{"file":"a.go","name":"Shared","start_line":1,"value":5}],"top_complex":[{"file":"a.go","name":"Shared","start_line":1,"value":9},{"file":"a.go","name":"Risk","start_line":10,"value":7}]}`,
			[]canopyProjectSeed{
				{File: "a.go", Name: "Shared", StartLine: 1, Signal: "high-fan-in", FanIn: integer(5), Cyclomatic: integer(9)},
				{File: "a.go", Name: "Risk", StartLine: 10, Signal: "complexity-hotspot", Cyclomatic: integer(7)},
			},
		},
		{
			"enriched_fields_and_measured_zero",
			`{"top_fan_in":[{"file":"a.go","name":"Shared","start_line":1,"value":0,"cyclomatic":4,"cognitive":0}],"top_complex":[{"file":"a.go","name":"Shared","start_line":1,"value":4,"cyclomatic":4,"cognitive":0},{"file":"a.go","name":"Risk","start_line":10,"value":99,"cyclomatic":3,"cognitive":0,"lines":12}]}`,
			[]canopyProjectSeed{
				{File: "a.go", Name: "Shared", StartLine: 1, Signal: "high-fan-in", FanIn: integer(0), Cyclomatic: integer(4), Cognitive: integer(0)},
				{File: "a.go", Name: "Risk", StartLine: 10, Signal: "complexity-hotspot", Cyclomatic: integer(3), Cognitive: integer(0)},
			},
		},
		{
			"unreported_metrics",
			`{"top_fan_in":[{"file":"a.go","name":"Shared","start_line":1}],"top_complex":[{"file":"a.go","name":"Shared","start_line":1,"value":null,"cognitive":null}]}`,
			[]canopyProjectSeed{{File: "a.go", Name: "Shared", StartLine: 1, Signal: "high-fan-in"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var summary canopyProjectSummary
			if err := json.Unmarshal([]byte(tc.summary), &summary); err != nil {
				t.Fatal(err)
			}
			if got := selectCanopyProjectSeeds(summary); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("metric kind, availability, priority, or enrichment changed: got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSelectCanopyProjectSeeds_EnrichesExistingRootAtSeedLimit(t *testing.T) {
	one, score := 1, 9
	var summary canopyProjectSummary
	for i := 0; i < maxCanopyProjectSeeds; i++ {
		summary.TopFanIn = append(summary.TopFanIn, canopySummaryMetric{File: "a.go", Name: "Root", StartLine: i + 1, Value: &one})
	}
	summary.TopComplex = []canopySummaryMetric{
		{File: "a.go", Name: "Root", StartLine: maxCanopyProjectSeeds, Value: &score},
		{File: "a.go", Name: "Extra", StartLine: 100, Value: &score},
	}
	seeds := selectCanopyProjectSeeds(summary)
	if len(seeds) != maxCanopyProjectSeeds || seeds[len(seeds)-1].Cyclomatic == nil || *seeds[len(seeds)-1].Cyclomatic != score {
		t.Fatalf("full seed budget prevented metadata enrichment: %+v", seeds)
	}
	for i, seed := range seeds {
		if seed.StartLine != i+1 || seed.Signal != "high-fan-in" || seed.FanIn == nil || *seed.FanIn != one {
			t.Fatalf("seed order, priority, or fan-in changed: %+v", seed)
		}
	}
}

func TestCanopyProjectMetricEncoding_DistinguishesUnknownFromMeasuredZero(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name  string
		value *int
	}{
		{"unknown", nil}, {"measured_zero", &zero},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := canopyGraphSymbol{ID: "root", File: "a.go", Name: "Root", StartLine: 1}
			report := canopyGraphReport{Roots: []canopyGraphSymbol{root}, Nodes: []canopyGraphNode{{ID: root.ID}}}
			flow := collectCanopyProjectFlow(context.Background(), "", "", "", canopyProjectSeed{
				File: root.File, Name: root.Name, StartLine: root.StartLine, FanIn: tc.value, Cyclomatic: tc.value, Cognitive: tc.value,
			}, canopyProjectGraphs{forward: newCanopyGraphBatch(report, false), reverse: newCanopyGraphBatch(report, true)})
			if flow.Status != "available" {
				t.Fatalf("fixture did not use the cached graphs: %+v", flow)
			}
			hotspot := canopyTOCHotspot{File: root.File, Name: root.Name, StartLine: root.StartLine,
				Cyclomatic: tc.value, Cognitive: tc.value, Lines: tc.value}
			for _, record := range []struct {
				value any
				keys  []string
			}{
				{flow, []string{"fan_in", "cyclomatic", "cognitive"}},
				{hotspot, []string{"cyclomatic", "cognitive", "lines"}},
			} {
				encoded, err := json.Marshal(record.value)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				for _, key := range record.keys {
					field, exists := fields[key]
					if exists != (tc.value != nil) || (exists && string(field) != "0") {
						t.Fatalf("metric %s confused unavailable and measured zero: %s", key, encoded)
					}
				}
			}
		})
	}
}
