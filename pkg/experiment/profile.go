package experiment

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelprofile"
)

// ModelCalibration is the content-free empirical signal extracted from all
// terminal runs for one model in an experiment.
type ModelCalibration struct {
	ModelID            string
	ProviderID         string
	Observations       []modelprofile.Observation
	MeasuredAt         time.Time
	AttributionCaveats []string
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
		if len(calibration.AttributionCaveats) > 0 {
			return modelprofile.Profile{}, fmt.Errorf("model calibration has no attributable terminal observations: %s", strings.Join(calibration.AttributionCaveats, "; "))
		}
		return modelprofile.Profile{}, fmt.Errorf("model calibration requires terminal observations")
	}
	if existing := strings.TrimSpace(base.ModelID); existing != "" && existing != modelID {
		return modelprofile.Profile{}, fmt.Errorf("model calibration %s does not match base profile %s", modelID, existing)
	}
	if existingProvider := strings.TrimSpace(base.Provider); existingProvider != "" && strings.TrimSpace(calibration.ProviderID) != "" && existingProvider != strings.TrimSpace(calibration.ProviderID) {
		return modelprofile.Profile{}, fmt.Errorf("model calibration provider %s does not match base profile provider %s", calibration.ProviderID, existingProvider)
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
		criteria := exp.Criteria
		modelID := strings.TrimSpace(variant.ModelID)
		providerID := strings.TrimSpace(variant.ProviderID)
		if run.InputManifest != nil {
			modelID = strings.TrimSpace(run.InputManifest.Variant.RequestedModelID)
			providerID = strings.TrimSpace(run.InputManifest.Variant.RequestedProviderID)
			criteria = run.InputManifest.criteriaDefinitions()
		}
		if modelID == "" {
			continue
		}
		ok, caveat, observedProvider := executionIdentitiesAttributable(run.ModelExecutions, modelID, providerID)
		if providerID == "" {
			providerID = observedProvider
		}
		groupKey := modelID + "\x00" + providerID
		calibration := grouped[groupKey]
		if calibration == nil {
			calibration = &ModelCalibration{ModelID: modelID, ProviderID: providerID}
			grouped[groupKey] = calibration
		}
		if !ok {
			calibration.AttributionCaveats = append(calibration.AttributionCaveats, fmt.Sprintf("run %s skipped: %s", run.ID, caveat))
			continue
		}
		evidence := calibrationEvidenceForRun(run, criteria, evaluations[run.ID])
		// Legacy zero costs are ambiguous; failed-run totals may be partial.
		observation := modelprofile.Observation{
			Succeeded:           evidence.taskSucceeded,
			TaskSuccessObserved: &evidence.taskSuccessObserved,
			LatencyMS:           run.Metrics.DurationMs,
			PromptTokens:        run.Metrics.PromptTokens,
			CompletionTokens:    run.Metrics.CompletionTokens,
			TokensObserved:      true,
			CostUSD:             run.Metrics.TotalCost,
			CostObserved:        run.Status == RunCompleted && run.Metrics.TotalCost > 0,
		}
		if run.Metrics.ToolCalls > 0 {
			toolSucceeded := run.Metrics.ToolFailures == 0 && run.Metrics.ToolSuccesses > 0
			observation.ToolSucceeded = &toolSucceeded
		}
		if evidence.verificationObserved {
			observation.VerificationPassed = &evidence.verificationPassed
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

	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := grouped[keys[i]]
		right := grouped[keys[j]]
		if left.ModelID != right.ModelID {
			return left.ModelID < right.ModelID
		}
		return left.ProviderID < right.ProviderID
	})
	out := make([]ModelCalibration, 0, len(keys))
	for _, key := range keys {
		if len(grouped[key].Observations) == 0 && len(grouped[key].AttributionCaveats) == 0 {
			continue
		}
		out = append(out, *grouped[key])
	}
	return out
}

func executionIdentitiesAttributable(identities []model.ExecutionIdentity, requestedModel, requestedProvider string) (bool, string, string) {
	if identities == nil {
		return true, "", strings.TrimSpace(requestedProvider)
	}
	if len(identities) == 0 {
		return false, "no model response identity evidence was captured", ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	requestedProvider = strings.TrimSpace(requestedProvider)
	var selectedModel string
	var providerID string
	var responseModel string
	for _, identity := range identities {
		if identity == (model.ExecutionIdentity{}) {
			return false, "one or more observed model responses had unavailable identity", ""
		}
		if identity.Conflicted {
			return false, "model execution identity was conflicted", ""
		}
		if identity.RequestedModel == "" {
			return false, "model execution identity did not record the requested model", ""
		}
		if requestedModel != "" && identity.RequestedModel != requestedModel {
			return false, fmt.Sprintf("requested model evidence %q differs from manifest %q", identity.RequestedModel, requestedModel), ""
		}
		if identity.SelectedModel == "" {
			return false, "model execution identity did not record the selected model", ""
		}
		if requestedModel != "" && identity.SelectedModel != requestedModel {
			return false, fmt.Sprintf("selected model %q differs from requested model %q", identity.SelectedModel, requestedModel), ""
		}
		if selectedModel == "" {
			selectedModel = identity.SelectedModel
		} else if identity.SelectedModel != selectedModel {
			return false, "mixed selected model identities", ""
		}
		if identity.ProviderID == "" {
			return false, "model execution identity did not record the selected provider", ""
		}
		if requestedProvider != "" && identity.ProviderID != requestedProvider {
			return false, fmt.Sprintf("provider %q differs from requested provider %q", identity.ProviderID, requestedProvider), ""
		}
		if providerID == "" {
			providerID = identity.ProviderID
		} else if identity.ProviderID != providerID {
			return false, "mixed provider identities", ""
		}
		if identity.ResponseModel == "" {
			return false, "model execution identity did not record the provider-reported model", ""
		}
		if requestedModel != "" && identity.ResponseModel != requestedModel {
			return false, fmt.Sprintf("provider-reported model %q differs from requested model %q", identity.ResponseModel, requestedModel), ""
		}
		if responseModel == "" {
			responseModel = identity.ResponseModel
		} else if identity.ResponseModel != responseModel {
			return false, "mixed provider-reported model identities", ""
		}
	}
	return true, "", providerID
}

type calibrationEvidence struct {
	taskSuccessObserved  bool
	taskSucceeded        bool
	verificationObserved bool
	verificationPassed   bool
}

func calibrationEvidenceForRun(run Run, criteria []SuccessCriterion, evaluations []CriterionEvaluation) calibrationEvidence {
	switch run.Status {
	case RunCompleted, RunFailed, RunCancelled:
	default:
		return calibrationEvidence{}
	}

	assessment := assessCriteria(criteria, evaluations)
	evidence := calibrationEvidence{}
	if assessment.Verified {
		evidence.verificationObserved = true
		evidence.verificationPassed = true
	} else if assessment.RankEligible && len(assessment.Failed) > 0 && len(assessment.Pending) == 0 {
		evidence.verificationObserved = true
		evidence.verificationPassed = false
	}

	switch run.Status {
	case RunFailed:
		evidence.taskSuccessObserved = true
		return evidence
	case RunCancelled:
		return evidence
	case RunCompleted:
	}
	if assessment.Verified {
		evidence.taskSuccessObserved = true
		evidence.taskSucceeded = true
		return evidence
	}
	if assessment.RankEligible && len(assessment.Failed) > 0 {
		evidence.taskSuccessObserved = true
		return evidence
	}
	return evidence
}
