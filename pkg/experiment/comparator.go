package experiment

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

// Comparator analyzes experiment results and computes rankings.
type Comparator struct {
	store *Store
}

// ComparisonReport summarizes results for an experiment.
type ComparisonReport struct {
	ExperimentID string
	Variants     []VariantReport
	Rankings     []Ranking
	Summary      string
}

// VariantReport captures metrics and criteria results per variant.
type VariantReport struct {
	VariantID          string
	RunID              string
	SessionID          string
	Branch             string
	VariantName        string
	ModelID            string
	ProviderID         string
	InputDigest        string
	WorkloadDigest     string
	ProvenanceStatus   string
	Status             RunStatus
	Metrics            RunMetrics
	CostEvidence       CostEvidence
	CriteriaScore      float64
	CriteriaPassed     []string
	CriteriaFailed     []string
	CriteriaPending    []string
	EvaluatedCriteria  int
	AutomatedCriteria  int
	VerificationStatus string
	Verified           bool
	RankEligible       bool
	ModelExecutions    []model.ExecutionIdentity
	OutputPreview      string
	Error              string
}

// Ranking captures ordering for variants.
type Ranking struct {
	VariantID string
	RunID     string
	Score     float64
	Rank      int
	Winner    bool
}

// NewComparator constructs a comparator for experiment results.
func NewComparator(store *Store) *Comparator {
	if store == nil {
		return nil
	}
	return &Comparator{store: store}
}

// Compare loads runs + evaluations and produces a comparison report.
func (c *Comparator) Compare(exp *Experiment) (*ComparisonReport, error) {
	if c == nil || c.store == nil {
		return nil, ErrStoreUnavailable
	}
	if exp == nil {
		return nil, errors.New("experiment is nil")
	}

	runs, err := c.store.ListRuns(exp.ID)
	if err != nil {
		return nil, err
	}
	evalsByRun, err := c.store.ListEvaluationsByExperiment(exp.ID)
	if err != nil {
		return nil, err
	}

	return CompareRuns(exp, runs, evalsByRun)
}

// CompareRuns produces the same deterministic comparison report as Comparator
// from caller-supplied experiment inputs, without doing storage I/O. Run input
// manifest validation only establishes self-consistency for requested-input
// provenance; it does not prove actual backend/model revision, repository
// baseline, harness, tool versions, signature authenticity, or execution. The
// report preserves unknown outcomes such as no criteria, manual-only criteria,
// missing automated evaluations, and non-completed runs as unverified evidence
// rather than verified winners. The caller is responsible for supplying
// aligned runs and evaluations for the experiment and for not mutating those
// inputs concurrently during comparison. Evaluations for run IDs not supplied
// in runs are ignored. CompareRuns does not mutate its inputs.
func CompareRuns(exp *Experiment, runs []Run, evaluations map[string][]CriterionEvaluation) (*ComparisonReport, error) {
	if exp == nil {
		return nil, errors.New("experiment is nil")
	}
	if err := validateCompareRunsInputs(exp, runs, evaluations); err != nil {
		return nil, err
	}

	variantByID := make(map[string]Variant, len(exp.Variants))
	for _, variant := range exp.Variants {
		variantByID[variant.ID] = variant
	}

	var reports []VariantReport
	for _, run := range runs {
		variant := variantByID[run.VariantID]
		criteriaDefinitions := exp.Criteria
		variantNameValue := variantName(&variant)
		modelID := variant.ModelID
		providerID := variant.ProviderID
		inputDigest := ""
		workloadDigest := ""
		provenanceStatus := "legacy run: input manifest unavailable; actual backend revision, baseline, harness, and tool versions not captured"
		if run.InputManifest != nil {
			criteriaDefinitions = run.InputManifest.criteriaDefinitions()
			variantNameValue = run.InputManifest.Variant.Name
			modelID = run.InputManifest.Variant.RequestedModelID
			providerID = run.InputManifest.Variant.RequestedProviderID
			inputDigest = run.InputManifest.InputDigest
			workloadDigest = run.InputManifest.WorkloadDigest
			provenanceStatus = "requested input manifest captured; actual backend revision, baseline, harness, and tool versions not captured"
		}
		if strings.TrimSpace(variantNameValue) == "" {
			variantNameValue = "unknown"
		}
		if strings.TrimSpace(modelID) == "" {
			modelID = "unknown requested model"
		}
		if err := validateComparisonCriteria(run.ID, criteriaDefinitions); err != nil {
			return nil, err
		}
		criteria := assessCriteria(criteriaDefinitions, evaluations[run.ID])
		verificationStatus := criteria.Status
		if run.Status != RunCompleted && criteria.Verified {
			verificationStatus = "unverified: run not completed (criteria passed)"
		}
		errorText := ""
		if run.Error != nil {
			errorText = *run.Error
		}
		reports = append(reports, VariantReport{
			VariantID:          run.VariantID,
			RunID:              run.ID,
			SessionID:          run.SessionID,
			Branch:             run.Branch,
			VariantName:        variantNameValue,
			ModelID:            modelID,
			ProviderID:         providerID,
			InputDigest:        inputDigest,
			WorkloadDigest:     workloadDigest,
			ProvenanceStatus:   provenanceStatus,
			Status:             run.Status,
			Metrics:            cloneRunMetrics(run.Metrics),
			CostEvidence:       runCostEvidence(run.Metrics),
			CriteriaScore:      criteria.Score,
			CriteriaPassed:     criteria.Passed,
			CriteriaFailed:     criteria.Failed,
			CriteriaPending:    criteria.Pending,
			EvaluatedCriteria:  criteria.EvaluatedAutomated,
			AutomatedCriteria:  criteria.AutomatedTotal,
			VerificationStatus: verificationStatus,
			Verified:           run.Status == RunCompleted && criteria.Verified,
			RankEligible:       run.Status == RunCompleted && criteria.RankEligible,
			ModelExecutions:    cloneModelExecutions(run.ModelExecutions),
			OutputPreview:      truncate(run.Output, 500),
			Error:              errorText,
		})
	}

	rankings := rankVariants(reports)
	summary := summarize(exp, rankings, reports)

	return &ComparisonReport{
		ExperimentID: exp.ID,
		Variants:     reports,
		Rankings:     rankings,
		Summary:      summary,
	}, nil
}

func validateCompareRunsInputs(exp *Experiment, runs []Run, evaluations map[string][]CriterionEvaluation) error {
	variantIDs := make(map[string]struct{}, len(exp.Variants))
	for _, variant := range exp.Variants {
		if strings.TrimSpace(variant.ID) == "" {
			continue
		}
		if _, ok := variantIDs[variant.ID]; ok {
			return fmt.Errorf("compare experiment contains duplicate variant id %q", variant.ID)
		}
		variantIDs[variant.ID] = struct{}{}
	}
	seenRunIDs := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		if strings.TrimSpace(run.ID) == "" {
			return errors.New("compare runs require non-empty run ids")
		}
		if _, ok := seenRunIDs[run.ID]; ok {
			return fmt.Errorf("compare runs contain duplicate run id %q", run.ID)
		}
		seenRunIDs[run.ID] = struct{}{}
		if run.ExperimentID != "" && exp.ID != "" && run.ExperimentID != exp.ID {
			return fmt.Errorf("compare run %s belongs to experiment %s, not %s", run.ID, run.ExperimentID, exp.ID)
		}
		if run.ModelExecutions != nil {
			if err := validateModelExecutions(run.ModelExecutions); err != nil {
				return fmt.Errorf("compare run %s model executions: %w", run.ID, err)
			}
		}
		if run.InputManifest != nil {
			if err := run.InputManifest.Validate(); err != nil {
				return fmt.Errorf("compare run %s input manifest: %w", run.ID, err)
			}
			if run.InputManifest.Variant.ID != "" && run.InputManifest.Variant.ID != run.VariantID {
				return fmt.Errorf("compare run %s input manifest variant id %s does not match run variant id %s", run.ID, run.InputManifest.Variant.ID, run.VariantID)
			}
		} else if _, ok := variantIDs[run.VariantID]; !ok {
			return fmt.Errorf("compare legacy run %s references missing variant id %s", run.ID, run.VariantID)
		}
	}
	for runID, evals := range evaluations {
		if _, ok := seenRunIDs[runID]; !ok {
			continue
		}
		seenCriterionIDs := make(map[int64]struct{}, len(evals))
		for _, eval := range evals {
			if eval.RunID != "" && eval.RunID != runID {
				return fmt.Errorf("compare evaluation for run %s has mismatched run id %s", runID, eval.RunID)
			}
			if _, ok := seenCriterionIDs[eval.CriterionID]; ok {
				return fmt.Errorf("compare evaluations for run %s contain duplicate criterion evaluation for criterion id %d", runID, eval.CriterionID)
			}
			seenCriterionIDs[eval.CriterionID] = struct{}{}
		}
	}
	return nil
}

func validateComparisonCriteria(runID string, criteria []SuccessCriterion) error {
	seen := make(map[int64]string, len(criteria))
	for _, criterion := range criteria {
		if existing, ok := seen[criterion.ID]; ok {
			return fmt.Errorf("compare run %s has duplicate criterion id %d for %q and %q", runID, criterion.ID, existing, criterion.Name)
		}
		seen[criterion.ID] = criterion.Name
	}
	return nil
}

type criteriaAssessment struct {
	Score              float64
	Passed             []string
	Failed             []string
	Pending            []string
	AutomatedTotal     int
	EvaluatedAutomated int
	Status             string
	RankEligible       bool
	Verified           bool
}

func assessCriteria(criteria []SuccessCriterion, evaluations []CriterionEvaluation) criteriaAssessment {
	if len(criteria) == 0 {
		return criteriaAssessment{
			Status: "unverified: no success criteria configured",
		}
	}

	evalByCriterion := make(map[int64]CriterionEvaluation, len(evaluations))
	for _, eval := range evaluations {
		evalByCriterion[eval.CriterionID] = eval
	}

	totalWeight := 0.0
	earnedWeight := 0.0
	var passed []string
	var failed []string
	var pending []string
	automatedTotal := 0
	evaluatedAutomated := 0

	for _, crit := range criteria {
		if crit.Type == CriterionManual {
			pending = append(pending, crit.Name)
			continue
		}
		automatedTotal++
		weight := crit.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight

		if crit.Target == "" && !crit.targetKnownNonempty && (crit.Type == CriterionContains || crit.Type == CriterionFileExists) {
			pending = append(pending, crit.Name+" (empty target)")
			continue
		}

		eval, ok := evalByCriterion[crit.ID]
		if !ok {
			pending = append(pending, crit.Name)
			continue
		}
		evaluatedAutomated++
		if eval.Passed {
			earnedWeight += weight
			passed = append(passed, crit.Name)
		} else {
			failed = append(failed, crit.Name)
		}
	}

	score := 0.0
	if totalWeight == 0 {
		return criteriaAssessment{
			Passed:  passed,
			Failed:  failed,
			Pending: pending,
			Status:  "manual review pending",
		}
	}
	score = earnedWeight / totalWeight
	status := "unverified: missing automated evaluation"
	if evaluatedAutomated == automatedTotal {
		if len(pending) > 0 {
			status = "manual review pending"
		} else if len(failed) > 0 {
			status = "evaluated: criteria failed"
		} else {
			status = "verified"
		}
	}
	return criteriaAssessment{
		Score:              score,
		Passed:             passed,
		Failed:             failed,
		Pending:            pending,
		AutomatedTotal:     automatedTotal,
		EvaluatedAutomated: evaluatedAutomated,
		Status:             status,
		RankEligible:       automatedTotal > 0 && evaluatedAutomated == automatedTotal,
		Verified:           automatedTotal > 0 && evaluatedAutomated == automatedTotal && len(failed) == 0 && len(pending) == 0,
	}
}

func rankVariants(reports []VariantReport) []Ranking {
	if len(reports) == 0 {
		return nil
	}

	ordered := append([]VariantReport(nil), reports...)
	// Apply the same cost rule to every eligible run in an equal-score group.
	unpricedScores := make(map[float64]bool)
	for _, report := range ordered {
		if !report.RankEligible {
			continue
		}
		cost := report.CostEvidence
		if cost.Status == "" {
			cost = runCostEvidence(report.Metrics)
		}
		if !cost.Comparable {
			unpricedScores[report.CriteriaScore] = true
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		leftEligible := ordered[i].RankEligible
		rightEligible := ordered[j].RankEligible
		if leftEligible != rightEligible {
			return leftEligible
		}
		if !leftEligible && !rightEligible {
			if ordered[i].Status != ordered[j].Status {
				return runStatusOrder(ordered[i].Status) < runStatusOrder(ordered[j].Status)
			}
			if ordered[i].RunID != ordered[j].RunID {
				return ordered[i].RunID < ordered[j].RunID
			}
			return ordered[i].VariantID < ordered[j].VariantID
		}
		if ordered[i].CriteriaScore != ordered[j].CriteriaScore {
			return ordered[i].CriteriaScore > ordered[j].CriteriaScore
		}
		if !unpricedScores[ordered[i].CriteriaScore] && ordered[i].Metrics.TotalCost != ordered[j].Metrics.TotalCost {
			return ordered[i].Metrics.TotalCost < ordered[j].Metrics.TotalCost
		}
		if ordered[i].Metrics.DurationMs != ordered[j].Metrics.DurationMs {
			return ordered[i].Metrics.DurationMs < ordered[j].Metrics.DurationMs
		}
		if ordered[i].RunID != ordered[j].RunID {
			return ordered[i].RunID < ordered[j].RunID
		}
		return ordered[i].VariantID < ordered[j].VariantID
	})

	rankings := make([]Ranking, 0, len(reports))
	nextRank := 1
	winnerAssigned := false
	for _, report := range ordered {
		rank := 0
		if report.RankEligible {
			rank = nextRank
			nextRank++
		}
		winner := false
		if !winnerAssigned && report.Verified {
			winner = true
			winnerAssigned = true
		}
		rankings = append(rankings, Ranking{
			VariantID: report.VariantID,
			RunID:     report.RunID,
			Score:     report.CriteriaScore,
			Rank:      rank,
			Winner:    winner,
		})
	}
	return rankings
}

func runStatusOrder(status RunStatus) int {
	switch status {
	case RunCompleted:
		return 0
	case RunFailed:
		return 1
	case RunCancelled:
		return 2
	case RunRunning:
		return 3
	case RunPending:
		return 4
	default:
		return 5
	}
}

func summarize(exp *Experiment, rankings []Ranking, reports []VariantReport) string {
	if exp == nil || len(rankings) == 0 {
		return ""
	}
	winner := findWinnerRanking(rankings)
	if winner == nil {
		return "No verified winner: no completed run satisfied all configured criteria with complete automated evidence"
	}
	best := findReport(reports, winner.RunID)
	if best == nil {
		return ""
	}
	return fmt.Sprintf("Best verified run: %s / %s (%s, %.1f%% score)",
		best.VariantName, best.RunID, best.ModelID, best.CriteriaScore*100)
}

func findWinnerRanking(rankings []Ranking) *Ranking {
	for i := range rankings {
		if rankings[i].Winner {
			return &rankings[i]
		}
	}
	return nil
}

func findReport(reports []VariantReport, id string) *VariantReport {
	return findVariantReport(reports, id)
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
