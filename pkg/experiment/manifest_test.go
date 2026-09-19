package experiment

import (
	"math"
	"testing"
	"time"
)

func TestRunInputManifestDigestsStableAcrossMapOrderAndStorageIDs(t *testing.T) {
	systemPrompt := "be precise"
	temp := 0.2
	maxTokens := 2048
	expA := &Experiment{
		ID: "exp-a",
		Task: Task{
			Prompt:     "implement feature",
			Context:    map[string]string{"b": "2", "a": "1"},
			WorkingDir: "/repo",
			Timeout:    time.Minute,
			Files:      []string{"b.go", "a.go"},
			Scope:      []string{"pkg/b/...", "pkg/a/..."},
		},
		Criteria: []SuccessCriterion{
			{ID: 1, Name: "tests", Type: CriterionTestPass, Target: "go test ./pkg/...", Weight: 1},
			{ID: 2, Name: "contains", Type: CriterionContains, Target: "needle", Weight: 2},
		},
	}
	variantA := Variant{
		ID:           "generated-a",
		Name:         "candidate",
		ModelID:      "provider/model-release-a",
		ProviderID:   "openrouter",
		SystemPrompt: &systemPrompt,
		Temperature:  &temp,
		MaxTokens:    &maxTokens,
		ToolsAllowed: []string{"write", "read"},
		CustomConfig: map[string]any{"z": float64(9), "a": "first"},
		Files:        []string{"override-b.go", "override-a.go"},
	}
	expB := &Experiment{
		ID: "exp-b",
		Task: Task{
			Prompt:     expA.Task.Prompt,
			Context:    map[string]string{"a": "1", "b": "2"},
			WorkingDir: expA.Task.WorkingDir,
			Timeout:    expA.Task.Timeout,
			Files:      []string{"a.go", "b.go"},
			Scope:      []string{"pkg/a/...", "pkg/b/..."},
		},
		Criteria: []SuccessCriterion{
			{ID: 101, Name: "tests", Type: CriterionTestPass, Target: "go test ./pkg/...", Weight: 1},
			{ID: 202, Name: "contains", Type: CriterionContains, Target: "needle", Weight: 2},
		},
	}
	variantB := variantA
	variantB.ID = "generated-b"
	variantB.ToolsAllowed = []string{"read", "write"}
	variantB.Files = []string{"override-a.go", "override-b.go"}
	variantB.CustomConfig = map[string]any{"a": "first", "z": float64(9)}

	manifestA, err := buildRunInputManifest(expA, variantA, expA.Task.Timeout)
	if err != nil {
		t.Fatalf("build manifest A: %v", err)
	}
	manifestB, err := buildRunInputManifest(expB, variantB, expB.Task.Timeout)
	if err != nil {
		t.Fatalf("build manifest B: %v", err)
	}
	if manifestA.InputDigest != manifestB.InputDigest {
		t.Fatalf("input digest changed for map/order/storage IDs:\nA=%s\nB=%s", manifestA.InputDigest, manifestB.InputDigest)
	}
	if manifestA.WorkloadDigest != manifestB.WorkloadDigest {
		t.Fatalf("workload digest changed for map/order/storage IDs:\nA=%s\nB=%s", manifestA.WorkloadDigest, manifestB.WorkloadDigest)
	}
}

func TestRunInputManifestDigestsChangeOnMeaningfulInputs(t *testing.T) {
	base := &Experiment{
		Task: Task{Prompt: "implement feature", Context: map[string]string{"mode": "safe"}, Timeout: time.Minute},
		Criteria: []SuccessCriterion{
			{Name: "tests", Type: CriterionTestPass, Target: "go test ./...", Weight: 1},
		},
	}
	variant := Variant{Name: "candidate", ModelID: "model-a", ProviderID: "provider-a", CustomConfig: map[string]any{"effort": "medium"}}
	original, err := buildRunInputManifest(base, variant, time.Minute)
	if err != nil {
		t.Fatalf("build original manifest: %v", err)
	}

	taskChanged := *base
	taskChanged.Task.Prompt = "implement different feature"
	changedTask, _ := buildRunInputManifest(&taskChanged, variant, time.Minute)
	if changedTask.WorkloadDigest == original.WorkloadDigest || changedTask.InputDigest == original.InputDigest {
		t.Fatalf("task change did not affect digests")
	}

	modelChanged := variant
	modelChanged.ModelID = "model-b"
	changedModel, _ := buildRunInputManifest(base, modelChanged, time.Minute)
	if changedModel.WorkloadDigest != original.WorkloadDigest {
		t.Fatalf("model change affected workload digest")
	}
	if changedModel.InputDigest == original.InputDigest {
		t.Fatalf("model change did not affect input digest")
	}

	criteriaChanged := *base
	criteriaChanged.Criteria = []SuccessCriterion{{Name: "tests", Type: CriterionTestPass, Target: "go test ./pkg/...", Weight: 1}}
	changedCriteria, _ := buildRunInputManifest(&criteriaChanged, variant, time.Minute)
	if changedCriteria.WorkloadDigest == original.WorkloadDigest || changedCriteria.InputDigest == original.InputDigest {
		t.Fatalf("criteria change did not affect digests")
	}
}

func TestRunInputManifestCustomConfigPreservesLargeIntegerIdentity(t *testing.T) {
	exp := &Experiment{Task: Task{Prompt: "compare"}}
	left, err := buildRunInputManifest(exp, Variant{
		Name:         "left",
		ModelID:      "model-a",
		CustomConfig: map[string]any{"seed": uint64(9007199254740992)},
	}, time.Minute)
	if err != nil {
		t.Fatalf("build left manifest: %v", err)
	}
	right, err := buildRunInputManifest(exp, Variant{
		Name:         "left",
		ModelID:      "model-a",
		CustomConfig: map[string]any{"seed": uint64(9007199254740993)},
	}, time.Minute)
	if err != nil {
		t.Fatalf("build right manifest: %v", err)
	}
	if left.InputDigest == right.InputDigest {
		t.Fatalf("adjacent large integer custom configs produced same input digest %s", left.InputDigest)
	}
}

func TestRunInputManifestRejectsNonFiniteNumbers(t *testing.T) {
	exp := &Experiment{Task: Task{Prompt: "compare"}}
	temp := math.NaN()
	if _, err := buildRunInputManifest(exp, Variant{Name: "nan-temp", ModelID: "model-a", Temperature: &temp}, time.Minute); err == nil {
		t.Fatalf("build manifest with NaN temperature error = nil, want error")
	}
	exp.Criteria = []SuccessCriterion{{Name: "bad weight", Type: CriterionTestPass, Target: "go test", Weight: math.Inf(1)}}
	if _, err := buildRunInputManifest(exp, Variant{Name: "bad-weight", ModelID: "model-a"}, time.Minute); err == nil {
		t.Fatalf("build manifest with infinite criterion weight error = nil, want error")
	}
}
