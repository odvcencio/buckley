package experiment

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
)

// ModelCalibration is the content-free empirical signal extracted from all
// terminal runs for one model in an experiment.
type ModelCalibration struct {
	ModelID      string
	ProviderID   string
	Observations []modelprofile.Observation
	MeasuredAt   time.Time
}

// CalibrateModelProfile merges one experiment's content-free observations
// into a model's prior profile. An empty class selects evidence-derived
// classification; a non-empty class is an explicit operator override.
func CalibrateModelProfile(base modelprofile.Profile, calibration ModelCalibration, version string, class modelprofile.Class) (modelprofile.Profile, error) {
	modelID := strings.TrimSpace(calibration.ModelID)
	if modelID == "" {
		return modelprofile.Profile{}, fmt.Errorf("model calibration model id is required")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return modelprofile.Profile{}, fmt.Errorf("model calibration version is required")
	}
	if len(calibration.Observations) == 0 {
		return modelprofile.Profile{}, fmt.Errorf("model calibration requires terminal observations")
	}
	if existing := strings.TrimSpace(base.ModelID); existing != "" && existing != modelID {
		return modelprofile.Profile{}, fmt.Errorf("model calibration %s does not match base profile %s", modelID, existing)
	}

	base.SchemaVersion = modelprofile.SchemaVersion
	base.ModelID = modelID
	base.Version = version
	base.Class = class
	if provider := strings.TrimSpace(calibration.ProviderID); provider != "" {
		base.Provider = provider
	}
	for _, observation := range calibration.Observations {
		if observation.ToolSucceeded != nil {
			base.Capabilities.ToolCalls = true
			break
		}
	}

	profile, err := modelprofile.Aggregate(base, calibration.Observations, calibration.MeasuredAt)
	if err != nil {
		return modelprofile.Profile{}, err
	}
	if confidence := sampleConfidence(profile.SampleSize); confidence > profile.Confidence {
		profile.Confidence = confidence
	}
	if err := profile.Validate(); err != nil {
		return modelprofile.Profile{}, err
	}
	return profile, nil
}

// sampleConfidence is deliberately conservative: 10 samples yield 0.5,
// 30 yield 0.75, and 90 yield 0.9. Existing stronger evidence is preserved.
func sampleConfidence(samples int) float64 {
	if samples <= 0 {
		return 0
	}
	return float64(samples) / float64(samples+10)
}

// ModelCalibrations groups terminal experiment runs by model. Prompts, output,
// file paths, and evaluator details never cross into the profile observation.
func ModelCalibrations(exp *Experiment, runs []Run, evaluations map[string][]CriterionEvaluation) []ModelCalibration {
	if exp == nil {
		return nil
	}
	variants := make(map[string]Variant, len(exp.Variants))
	for _, variant := range exp.Variants {
		variants[variant.ID] = variant
	}
	grouped := make(map[string]*ModelCalibration)
	for _, run := range runs {
		if run.Status == RunPending || run.Status == RunRunning {
			continue
		}
		variant := variants[run.VariantID]
		modelID := strings.TrimSpace(variant.ModelID)
		if modelID == "" {
			continue
		}
		calibration := grouped[modelID]
		if calibration == nil {
			calibration = &ModelCalibration{ModelID: modelID, ProviderID: strings.TrimSpace(variant.ProviderID)}
			grouped[modelID] = calibration
		}
		passed := runPassesCriteria(run, exp.Criteria, evaluations[run.ID])
		observation := modelprofile.Observation{
			Succeeded:        run.Status == RunCompleted && passed,
			LatencyMS:        run.Metrics.DurationMs,
			PromptTokens:     run.Metrics.PromptTokens,
			CompletionTokens: run.Metrics.CompletionTokens,
			TokensObserved:   true,
			CostUSD:          run.Metrics.TotalCost,
			CostObserved:     true,
		}
		if run.Metrics.ToolCalls > 0 {
			toolSucceeded := run.Metrics.ToolFailures == 0 && run.Metrics.ToolSuccesses > 0
			observation.ToolSucceeded = &toolSucceeded
		}
		if len(exp.Criteria) > 0 {
			observation.VerificationPassed = &passed
		}
		calibration.Observations = append(calibration.Observations, observation)
		measuredAt := run.StartedAt
		if run.CompletedAt != nil {
			measuredAt = *run.CompletedAt
		}
		if measuredAt.After(calibration.MeasuredAt) {
			calibration.MeasuredAt = measuredAt
		}
	}

	modelIDs := make([]string, 0, len(grouped))
	for modelID := range grouped {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	out := make([]ModelCalibration, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		out = append(out, *grouped[modelID])
	}
	return out
}

func runPassesCriteria(run Run, criteria []SuccessCriterion, evaluations []CriterionEvaluation) bool {
	if run.Status != RunCompleted {
		return false
	}
	if len(criteria) == 0 {
		return true
	}
	passed := make(map[int64]bool, len(evaluations))
	for _, evaluation := range evaluations {
		if evaluation.Passed {
			passed[evaluation.CriterionID] = true
		}
	}
	for _, criterion := range criteria {
		if !passed[criterion.ID] {
			return false
		}
	}
	return true
}
