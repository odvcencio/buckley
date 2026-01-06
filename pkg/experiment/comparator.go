package experiment

import (
	"errors"
	"fmt"
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
	VariantID      string
	VariantName    string
	ModelID        string
	Status         RunStatus
	Metrics        RunMetrics
	CriteriaScore  float64
	CriteriaPassed []string
	CriteriaFailed []string
	OutputPreview  string
	Error          string
}

// Ranking captures ordering for variants.
type Ranking struct {
	VariantID string
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
		score, passed, failed := scoreCriteria(exp.Criteria, evalsByRun[run.ID])
		errorText := ""
		if run.Error != nil {
			errorText = *run.Error
		}
		reports = append(reports, VariantReport{
			VariantID:      run.VariantID,
			VariantName:    variantName(&variant),
			ModelID:        variant.ModelID,
			Status:         run.Status,
			Metrics:        run.Metrics,
			CriteriaScore:  score,
			CriteriaPassed: passed,
			CriteriaFailed: failed,
			OutputPreview:  truncate(run.Output, 500),
			Error:          errorText,
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

func scoreCriteria(criteria []SuccessCriterion, evaluations []CriterionEvaluation) (float64, []string, []string) {
	if len(criteria) == 0 {
		return 1.0, nil, nil
	}

	evalByCriterion := make(map[int64]CriterionEvaluation, len(evaluations))
	for _, eval := range evaluations {
		evalByCriterion[eval.CriterionID] = eval
	}

	totalWeight := 0.0
	earnedWeight := 0.0
	var passed []string
	var failed []string

	for _, crit := range criteria {
		if crit.Type == CriterionManual {
			continue
		}
		weight := crit.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight

		eval, ok := evalByCriterion[crit.ID]
		if ok && eval.Passed {
			earnedWeight += weight
			passed = append(passed, crit.Name)
		} else {
			failed = append(failed, crit.Name)
		}
	}

	if totalWeight == 0 {
		return 1.0, passed, failed
	}
	return earnedWeight / totalWeight, passed, failed
}

func rankVariants(reports []VariantReport) []Ranking {
	if len(reports) == 0 {
		return nil
	}

	sort.Slice(reports, func(i, j int) bool {
		if reports[i].CriteriaScore != reports[j].CriteriaScore {
			return reports[i].CriteriaScore > reports[j].CriteriaScore
		}
		if reports[i].Metrics.TotalCost != reports[j].Metrics.TotalCost {
			return reports[i].Metrics.TotalCost < reports[j].Metrics.TotalCost
		}
		return reports[i].Metrics.DurationMs < reports[j].Metrics.DurationMs
	})

	rankings := make([]Ranking, 0, len(reports))
	for i, report := range reports {
		rankings = append(rankings, Ranking{
			VariantID: report.VariantID,
			Score:     report.CriteriaScore,
			Rank:      i + 1,
		})
	}
	return rankings
}

func summarize(exp *Experiment, rankings []Ranking, reports []VariantReport) string {
	if exp == nil || len(rankings) == 0 {
		return ""
	}
	bestID := rankings[0].VariantID
	best := findReport(reports, bestID)
	if best == nil {
		return ""
	}
	return fmt.Sprintf("Best variant: %s (%.1f%% score)", best.VariantName, best.CriteriaScore*100)
}

func findReport(reports []VariantReport, id string) *VariantReport {
	for i := range reports {
		if reports[i].VariantID == id {
			return &reports[i]
		}
	}
	return nil
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
