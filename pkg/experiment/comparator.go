package experiment

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
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
	VariantName        string
	ModelID            string
	Status             RunStatus
	Metrics            RunMetrics
	CriteriaScore      float64
	CriteriaPassed     []string
	CriteriaFailed     []string
	CriteriaPending    []string
	VerificationStatus string
	Verified           bool
	RankEligible       bool
	OutputPreview      string
	Error              string
}

// Ranking captures ordering for variants.
type Ranking struct {
	VariantID string
	RunID     string
	Winner    bool
	Score     float64
	Rank      int
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

	variantByID := make(map[string]Variant, len(exp.Variants))
	for _, variant := range exp.Variants {
		variantByID[variant.ID] = variant
	}

	var reports []VariantReport
	for _, run := range runs {
		variant := variantByID[run.VariantID]
		criteria := assessCriteria(exp.Criteria, evalsByRun[run.ID])
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
			VariantName:        variantName(&variant),
			ModelID:            variant.ModelID,
			Status:             run.Status,
			Metrics:            run.Metrics,
			CriteriaScore:      criteria.Score,
			CriteriaPassed:     criteria.Passed,
			CriteriaFailed:     criteria.Failed,
			CriteriaPending:    criteria.Pending,
			VerificationStatus: verificationStatus,
			Verified:           run.Status == RunCompleted && criteria.Verified,
			RankEligible:       run.Status == RunCompleted && criteria.RankEligible,
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
	// Cost comparability is grouped by CriteriaScore so that an entire
	// equal-score eligible group uses the same rule, avoiding pairwise
	// nontransitive sorting when some costs are unknown.
	unpricedScores := make(map[float64]bool)
	for _, report := range ordered {
		if !report.RankEligible {
			continue
		}
		cost := report.Metrics.TotalCost
		if !(cost > 0 && !math.IsInf(cost, 0) && !math.IsNaN(cost)) {
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
