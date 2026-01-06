package experiment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// EvaluateCriteria evaluates success criteria for a run and returns evaluations.
func EvaluateCriteria(ctx context.Context, worktreePath string, workingDir string, output string, criteria []SuccessCriterion) []CriterionEvaluation {
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" || len(criteria) == 0 {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	workDir := resolveWorkingDir(worktreePath, workingDir)
	evals := make([]CriterionEvaluation, 0, len(criteria))

	for _, crit := range criteria {
		if crit.ID == 0 {
			continue
		}
		if crit.Type == CriterionManual {
			evals = append(evals, CriterionEvaluation{
				CriterionID: crit.ID,
				Passed:      false,
				Score:       0,
				Details:     "manual check required",
				EvaluatedAt: time.Now(),
			})
			continue
		}

		passed, details := evaluateCriterion(ctx, workDir, output, crit)
		score := 0.0
		if passed {
			score = 1.0
		}
		evals = append(evals, CriterionEvaluation{
			CriterionID: crit.ID,
			Passed:      passed,
			Score:       score,
			Details:     details,
			EvaluatedAt: time.Now(),
		})
	}

	return evals
}

func evaluateCriterion(ctx context.Context, workDir string, output string, crit SuccessCriterion) (bool, string) {
	switch crit.Type {
	case CriterionContains:
		return strings.Contains(output, crit.Target), ""
	case CriterionFileExists:
		path := crit.Target
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		_, err := os.Stat(path)
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	case CriterionCommand, CriterionTestPass:
		return runCriterionCommand(ctx, workDir, crit.Target)
	default:
		return false, fmt.Sprintf("unsupported criterion type: %s", crit.Type)
	}
}

func runCriterionCommand(ctx context.Context, workDir string, command string) (bool, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return false, "empty command"
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	details := strings.TrimSpace(string(output))
	if err != nil {
		if details == "" {
			details = err.Error()
		} else {
			details = fmt.Sprintf("%s\n%v", details, err)
		}
		return false, truncateDetails(details, 800)
	}
	return true, truncateDetails(details, 800)
}

func resolveWorkingDir(worktreePath string, workingDir string) string {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return worktreePath
	}
	if filepath.IsAbs(workingDir) {
		return workingDir
	}
	return filepath.Join(worktreePath, workingDir)
}

func truncateDetails(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
