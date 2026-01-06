package experiment

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/storage"
)

// ErrStoreUnavailable indicates the experiment store is not configured.
var ErrStoreUnavailable = errors.New("experiment store unavailable")

// Store manages experiment persistence.
type Store struct {
	db *sql.DB
}

// NewStore constructs an experiment store from a database handle.
func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// NewStoreFromStorage constructs an experiment store from the main storage store.
func NewStoreFromStorage(store *storage.Store) *Store {
	if store == nil {
		return nil
	}
	return NewStore(store.DB())
}

// CreateExperiment persists a new experiment along with variants and criteria.
func (s *Store) CreateExperiment(exp *Experiment) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if exp == nil {
		return errors.New("experiment is nil")
	}
	if exp.ID == "" {
		exp.ID = ulid.Make().String()
	}
	if exp.Status == "" {
		exp.Status = ExperimentPending
	}
	if exp.CreatedAt.IsZero() {
		exp.CreatedAt = time.Now()
	}

	taskContext, err := marshalJSON(exp.Task.Context)
	if err != nil {
		return fmt.Errorf("marshal task context: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`
		INSERT INTO experiments (
			id, name, description, hypothesis, task_prompt, task_context,
			task_working_dir, task_timeout_ms, status, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		exp.ID,
		exp.Name,
		nullIfEmpty(exp.Description),
		nullIfEmpty(exp.Hypothesis),
		exp.Task.Prompt,
		nullIfEmpty(taskContext),
		nullIfEmpty(exp.Task.WorkingDir),
		nullIfZeroInt64(timeoutMillis(exp.Task.Timeout)),
		string(exp.Status),
		exp.CreatedAt,
		nullTime(exp.CompletedAt),
	)
	if err != nil {
		return err
	}

	if len(exp.Variants) > 0 {
		stmt, prepErr := tx.Prepare(`
			INSERT INTO experiment_variants (
				id, experiment_id, name, model_id, provider_id,
				system_prompt, temperature, max_tokens, tools_allowed, custom_config
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if prepErr != nil {
			return prepErr
		}
		defer stmt.Close()

		for i := range exp.Variants {
			variant := &exp.Variants[i]
			if variant.ID == "" {
				variant.ID = ulid.Make().String()
			}

			toolsAllowed, jsonErr := marshalJSON(variant.ToolsAllowed)
			if jsonErr != nil {
				return fmt.Errorf("marshal tools allowed: %w", jsonErr)
			}
			customConfig, jsonErr := marshalJSON(variant.CustomConfig)
			if jsonErr != nil {
				return fmt.Errorf("marshal custom config: %w", jsonErr)
			}

			_, execErr := stmt.Exec(
				variant.ID,
				exp.ID,
				variantName(variant),
				variant.ModelID,
				nullIfEmpty(variant.ProviderID),
				nullStringPtr(variant.SystemPrompt),
				nullFloatPtr(variant.Temperature),
				nullIntPtr(variant.MaxTokens),
				nullIfEmpty(toolsAllowed),
				nullIfEmpty(customConfig),
			)
			if execErr != nil {
				return execErr
			}
		}
	}

	if len(exp.Criteria) > 0 {
		stmt, prepErr := tx.Prepare(`
			INSERT INTO experiment_criteria (experiment_id, name, criterion_type, target, weight)
			VALUES (?, ?, ?, ?, ?)
		`)
		if prepErr != nil {
			return prepErr
		}
		defer stmt.Close()

		for i := range exp.Criteria {
			criterion := &exp.Criteria[i]
			weight := criterion.Weight
			if weight <= 0 {
				weight = 1
			}
			result, execErr := stmt.Exec(
				exp.ID,
				criterion.Name,
				string(criterion.Type),
				criterion.Target,
				weight,
			)
			if execErr != nil {
				return execErr
			}
			if id, execErr := result.LastInsertId(); execErr == nil {
				criterion.ID = id
			}
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return nil
}

// UpdateExperimentStatus updates experiment status and completion timestamp.
func (s *Store) UpdateExperimentStatus(id string, status ExperimentStatus, completedAt *time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("experiment id is required")
	}
	if status == "" {
		return errors.New("status is required")
	}

	finalStatus := status == ExperimentCompleted || status == ExperimentFailed || status == ExperimentCancelled
	if finalStatus && completedAt == nil {
		now := time.Now()
		completedAt = &now
	}

	_, err := s.db.Exec(`
		UPDATE experiments
		SET status = ?, completed_at = ?
		WHERE id = ?
	`, string(status), nullTime(completedAt), id)
	return err
}

// ListExperiments returns recent experiments, optionally filtered by status.
func (s *Store) ListExperiments(limit int, status ExperimentStatus) ([]Experiment, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}

	query := `
		SELECT id, name, description, hypothesis, task_prompt, task_context,
		       task_working_dir, task_timeout_ms, status, created_at, completed_at
		FROM experiments
	`
	var args []any
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var experiments []Experiment
	for rows.Next() {
		var exp Experiment
		var desc sql.NullString
		var hypo sql.NullString
		var ctx sql.NullString
		var workdir sql.NullString
		var timeout sql.NullInt64
		var statusStr string
		var completed sql.NullTime

		if err := rows.Scan(
			&exp.ID,
			&exp.Name,
			&desc,
			&hypo,
			&exp.Task.Prompt,
			&ctx,
			&workdir,
			&timeout,
			&statusStr,
			&exp.CreatedAt,
			&completed,
		); err != nil {
			return nil, err
		}
		exp.Description = desc.String
		exp.Hypothesis = hypo.String
		exp.Task.WorkingDir = workdir.String
		exp.Task.Timeout = durationFromMillis(timeout)
		exp.Status = ExperimentStatus(statusStr)
		if completed.Valid {
			exp.CompletedAt = &completed.Time
		}
		if err := unmarshalJSON(ctx.String, &exp.Task.Context); err != nil {
			return nil, fmt.Errorf("decode task context: %w", err)
		}
		experiments = append(experiments, exp)
	}

	return experiments, rows.Err()
}

// GetExperiment loads a single experiment with variants and criteria.
func (s *Store) GetExperiment(id string) (*Experiment, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("experiment id is required")
	}

	row := s.db.QueryRow(`
		SELECT id, name, description, hypothesis, task_prompt, task_context,
		       task_working_dir, task_timeout_ms, status, created_at, completed_at
		FROM experiments WHERE id = ?
	`, id)

	var exp Experiment
	var desc sql.NullString
	var hypo sql.NullString
	var ctx sql.NullString
	var workdir sql.NullString
	var timeout sql.NullInt64
	var statusStr string
	var completed sql.NullTime

	if err := row.Scan(
		&exp.ID,
		&exp.Name,
		&desc,
		&hypo,
		&exp.Task.Prompt,
		&ctx,
		&workdir,
		&timeout,
		&statusStr,
		&exp.CreatedAt,
		&completed,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	exp.Description = desc.String
	exp.Hypothesis = hypo.String
	exp.Task.WorkingDir = workdir.String
	exp.Task.Timeout = durationFromMillis(timeout)
	exp.Status = ExperimentStatus(statusStr)
	if completed.Valid {
		exp.CompletedAt = &completed.Time
	}
	if err := unmarshalJSON(ctx.String, &exp.Task.Context); err != nil {
		return nil, fmt.Errorf("decode task context: %w", err)
	}

	variants, err := s.listVariants(id)
	if err != nil {
		return nil, err
	}
	exp.Variants = variants

	criteria, err := s.listCriteria(id)
	if err != nil {
		return nil, err
	}
	exp.Criteria = criteria

	return &exp, nil
}

// FindExperimentByName loads the most recent experiment with the given name.
func (s *Store) FindExperimentByName(name string) (*Experiment, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("experiment name is required")
	}

	row := s.db.QueryRow(`
		SELECT id, name, description, hypothesis, task_prompt, task_context,
		       task_working_dir, task_timeout_ms, status, created_at, completed_at
		FROM experiments
		WHERE name = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, name)

	var exp Experiment
	var desc sql.NullString
	var hypo sql.NullString
	var ctx sql.NullString
	var workdir sql.NullString
	var timeout sql.NullInt64
	var statusStr string
	var completed sql.NullTime

	if err := row.Scan(
		&exp.ID,
		&exp.Name,
		&desc,
		&hypo,
		&exp.Task.Prompt,
		&ctx,
		&workdir,
		&timeout,
		&statusStr,
		&exp.CreatedAt,
		&completed,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	exp.Description = desc.String
	exp.Hypothesis = hypo.String
	exp.Task.WorkingDir = workdir.String
	exp.Task.Timeout = durationFromMillis(timeout)
	exp.Status = ExperimentStatus(statusStr)
	if completed.Valid {
		exp.CompletedAt = &completed.Time
	}
	if err := unmarshalJSON(ctx.String, &exp.Task.Context); err != nil {
		return nil, fmt.Errorf("decode task context: %w", err)
	}

	variants, err := s.listVariants(exp.ID)
	if err != nil {
		return nil, err
	}
	exp.Variants = variants

	criteria, err := s.listCriteria(exp.ID)
	if err != nil {
		return nil, err
	}
	exp.Criteria = criteria

	return &exp, nil
}

// SaveRun inserts or updates a run record.
func (s *Store) SaveRun(run *Run) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if run == nil {
		return errors.New("run is nil")
	}
	if run.ID == "" {
		run.ID = ulid.Make().String()
	}
	if run.Status == "" {
		run.Status = RunPending
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}

	filesChanged, err := marshalJSON(run.Files)
	if err != nil {
		return fmt.Errorf("marshal files: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO experiment_runs (
			id, experiment_id, variant_id, session_id, branch, status,
			output, files_changed, error,
			duration_ms, prompt_tokens, completion_tokens, total_cost,
			tool_calls, tool_successes, tool_failures, files_modified, lines_changed,
			started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			experiment_id = excluded.experiment_id,
			variant_id = excluded.variant_id,
			session_id = excluded.session_id,
			branch = excluded.branch,
			status = excluded.status,
			output = excluded.output,
			files_changed = excluded.files_changed,
			error = excluded.error,
			duration_ms = excluded.duration_ms,
			prompt_tokens = excluded.prompt_tokens,
			completion_tokens = excluded.completion_tokens,
			total_cost = excluded.total_cost,
			tool_calls = excluded.tool_calls,
			tool_successes = excluded.tool_successes,
			tool_failures = excluded.tool_failures,
			files_modified = excluded.files_modified,
			lines_changed = excluded.lines_changed,
			started_at = excluded.started_at,
			completed_at = excluded.completed_at
	`,
		run.ID,
		run.ExperimentID,
		run.VariantID,
		nullIfEmpty(run.SessionID),
		run.Branch,
		string(run.Status),
		nullIfEmpty(run.Output),
		nullIfEmpty(filesChanged),
		nullStringPtr(run.Error),
		nullIfZeroInt64(run.Metrics.DurationMs),
		nullIfZeroInt(run.Metrics.PromptTokens),
		nullIfZeroInt(run.Metrics.CompletionTokens),
		nullIfZeroFloat(run.Metrics.TotalCost),
		nullIfZeroInt(run.Metrics.ToolCalls),
		nullIfZeroInt(run.Metrics.ToolSuccesses),
		nullIfZeroInt(run.Metrics.ToolFailures),
		nullIfZeroInt(run.Metrics.FilesModified),
		nullIfZeroInt(run.Metrics.LinesChanged),
		run.StartedAt,
		nullTime(run.CompletedAt),
	)
	return err
}

// ListRuns returns runs for an experiment.
func (s *Store) ListRuns(experimentID string) ([]Run, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if strings.TrimSpace(experimentID) == "" {
		return nil, errors.New("experiment id is required")
	}

	rows, err := s.db.Query(`
		SELECT id, variant_id, session_id, branch, status, output, files_changed, error,
		       duration_ms, prompt_tokens, completion_tokens, total_cost,
		       tool_calls, tool_successes, tool_failures, files_modified, lines_changed,
		       started_at, completed_at
		FROM experiment_runs
		WHERE experiment_id = ?
		ORDER BY started_at
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		var sessionID sql.NullString
		var statusStr string
		var output sql.NullString
		var filesChanged sql.NullString
		var errStr sql.NullString
		var duration sql.NullInt64
		var promptTokens sql.NullInt64
		var completionTokens sql.NullInt64
		var totalCost sql.NullFloat64
		var toolCalls sql.NullInt64
		var toolSuccesses sql.NullInt64
		var toolFailures sql.NullInt64
		var filesModified sql.NullInt64
		var linesChanged sql.NullInt64
		var completed sql.NullTime

		if err := rows.Scan(
			&run.ID,
			&run.VariantID,
			&sessionID,
			&run.Branch,
			&statusStr,
			&output,
			&filesChanged,
			&errStr,
			&duration,
			&promptTokens,
			&completionTokens,
			&totalCost,
			&toolCalls,
			&toolSuccesses,
			&toolFailures,
			&filesModified,
			&linesChanged,
			&run.StartedAt,
			&completed,
		); err != nil {
			return nil, err
		}
		run.ExperimentID = experimentID
		run.SessionID = sessionID.String
		run.Status = RunStatus(statusStr)
		run.Output = output.String
		if errStr.Valid {
			value := errStr.String
			run.Error = &value
		}
		run.Metrics.DurationMs = duration.Int64
		run.Metrics.PromptTokens = int(promptTokens.Int64)
		run.Metrics.CompletionTokens = int(completionTokens.Int64)
		run.Metrics.TotalCost = totalCost.Float64
		run.Metrics.ToolCalls = int(toolCalls.Int64)
		run.Metrics.ToolSuccesses = int(toolSuccesses.Int64)
		run.Metrics.ToolFailures = int(toolFailures.Int64)
		run.Metrics.FilesModified = int(filesModified.Int64)
		run.Metrics.LinesChanged = int(linesChanged.Int64)
		if completed.Valid {
			run.CompletedAt = &completed.Time
		}
		if err := unmarshalJSON(filesChanged.String, &run.Files); err != nil {
			return nil, fmt.Errorf("decode files: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// GetRun fetches a single run by ID.
func (s *Store) GetRun(runID string) (*Run, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("run id is required")
	}

	row := s.db.QueryRow(`
		SELECT experiment_id, variant_id, session_id, branch, status, output, files_changed, error,
		       duration_ms, prompt_tokens, completion_tokens, total_cost,
		       tool_calls, tool_successes, tool_failures, files_modified, lines_changed,
		       started_at, completed_at
		FROM experiment_runs WHERE id = ?
	`, runID)

	var run Run
	var sessionID sql.NullString
	var statusStr string
	var output sql.NullString
	var filesChanged sql.NullString
	var errStr sql.NullString
	var duration sql.NullInt64
	var promptTokens sql.NullInt64
	var completionTokens sql.NullInt64
	var totalCost sql.NullFloat64
	var toolCalls sql.NullInt64
	var toolSuccesses sql.NullInt64
	var toolFailures sql.NullInt64
	var filesModified sql.NullInt64
	var linesChanged sql.NullInt64
	var completed sql.NullTime

	if err := row.Scan(
		&run.ExperimentID,
		&run.VariantID,
		&sessionID,
		&run.Branch,
		&statusStr,
		&output,
		&filesChanged,
		&errStr,
		&duration,
		&promptTokens,
		&completionTokens,
		&totalCost,
		&toolCalls,
		&toolSuccesses,
		&toolFailures,
		&filesModified,
		&linesChanged,
		&run.StartedAt,
		&completed,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	run.ID = runID
	run.SessionID = sessionID.String
	run.Status = RunStatus(statusStr)
	run.Output = output.String
	if errStr.Valid {
		value := errStr.String
		run.Error = &value
	}
	run.Metrics.DurationMs = duration.Int64
	run.Metrics.PromptTokens = int(promptTokens.Int64)
	run.Metrics.CompletionTokens = int(completionTokens.Int64)
	run.Metrics.TotalCost = totalCost.Float64
	run.Metrics.ToolCalls = int(toolCalls.Int64)
	run.Metrics.ToolSuccesses = int(toolSuccesses.Int64)
	run.Metrics.ToolFailures = int(toolFailures.Int64)
	run.Metrics.FilesModified = int(filesModified.Int64)
	run.Metrics.LinesChanged = int(linesChanged.Int64)
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	if err := unmarshalJSON(filesChanged.String, &run.Files); err != nil {
		return nil, fmt.Errorf("decode files: %w", err)
	}

	return &run, nil
}

// ReplaceEvaluations overwrites evaluations for a run.
func (s *Store) ReplaceEvaluations(runID string, evals []CriterionEvaluation) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if strings.TrimSpace(runID) == "" {
		return errors.New("run id is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM experiment_evaluations WHERE run_id = ?`, runID); err != nil {
		return err
	}

	if len(evals) > 0 {
		stmt, prepErr := tx.Prepare(`
			INSERT INTO experiment_evaluations (
				run_id, criterion_id, passed, score, details, evaluated_at
			) VALUES (?, ?, ?, ?, ?, ?)
		`)
		if prepErr != nil {
			return prepErr
		}
		defer stmt.Close()

		for i := range evals {
			eval := evals[i]
			evalTime := eval.EvaluatedAt
			if evalTime.IsZero() {
				evalTime = time.Now()
			}
			passed := 0
			if eval.Passed {
				passed = 1
			}
			if _, execErr := stmt.Exec(
				runID,
				eval.CriterionID,
				passed,
				eval.Score,
				nullIfEmpty(eval.Details),
				evalTime,
			); execErr != nil {
				return execErr
			}
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return nil
}

// ListEvaluationsByExperiment returns evaluations keyed by run ID.
func (s *Store) ListEvaluationsByExperiment(experimentID string) (map[string][]CriterionEvaluation, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if strings.TrimSpace(experimentID) == "" {
		return nil, errors.New("experiment id is required")
	}

	rows, err := s.db.Query(`
		SELECT e.id, e.run_id, e.criterion_id, e.passed, e.score, e.details, e.evaluated_at
		FROM experiment_evaluations e
		JOIN experiment_runs r ON r.id = e.run_id
		WHERE r.experiment_id = ?
		ORDER BY e.evaluated_at
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]CriterionEvaluation)
	for rows.Next() {
		var eval CriterionEvaluation
		var runID string
		var passed int
		var details sql.NullString

		if err := rows.Scan(
			&eval.ID,
			&runID,
			&eval.CriterionID,
			&passed,
			&eval.Score,
			&details,
			&eval.EvaluatedAt,
		); err != nil {
			return nil, err
		}
		eval.RunID = runID
		eval.Passed = passed == 1
		eval.Details = details.String
		result[runID] = append(result[runID], eval)
	}

	return result, rows.Err()
}

func (s *Store) listVariants(experimentID string) ([]Variant, error) {
	rows, err := s.db.Query(`
		SELECT id, name, model_id, provider_id, system_prompt, temperature, max_tokens,
		       tools_allowed, custom_config
		FROM experiment_variants
		WHERE experiment_id = ?
		ORDER BY created_at
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variants []Variant
	for rows.Next() {
		var v Variant
		var provider sql.NullString
		var systemPrompt sql.NullString
		var temperature sql.NullFloat64
		var maxTokens sql.NullInt64
		var toolsAllowed sql.NullString
		var customConfig sql.NullString

		if err := rows.Scan(
			&v.ID,
			&v.Name,
			&v.ModelID,
			&provider,
			&systemPrompt,
			&temperature,
			&maxTokens,
			&toolsAllowed,
			&customConfig,
		); err != nil {
			return nil, err
		}
		v.ProviderID = provider.String
		if systemPrompt.Valid {
			value := systemPrompt.String
			v.SystemPrompt = &value
		}
		if temperature.Valid {
			value := temperature.Float64
			v.Temperature = &value
		}
		if maxTokens.Valid {
			value := int(maxTokens.Int64)
			v.MaxTokens = &value
		}
		if err := unmarshalJSON(toolsAllowed.String, &v.ToolsAllowed); err != nil {
			return nil, fmt.Errorf("decode tools allowed: %w", err)
		}
		if err := unmarshalJSON(customConfig.String, &v.CustomConfig); err != nil {
			return nil, fmt.Errorf("decode custom config: %w", err)
		}
		variants = append(variants, v)
	}
	return variants, rows.Err()
}

func (s *Store) listCriteria(experimentID string) ([]SuccessCriterion, error) {
	rows, err := s.db.Query(`
		SELECT id, name, criterion_type, target, weight
		FROM experiment_criteria
		WHERE experiment_id = ?
		ORDER BY id
	`, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var criteria []SuccessCriterion
	for rows.Next() {
		var c SuccessCriterion
		var typ string
		if err := rows.Scan(&c.ID, &c.Name, &typ, &c.Target, &c.Weight); err != nil {
			return nil, err
		}
		c.Type = CriterionType(typ)
		criteria = append(criteria, c)
	}
	return criteria, rows.Err()
}

func marshalJSON(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if string(data) == "null" {
		return "", nil
	}
	return string(data), nil
}

func unmarshalJSON(raw string, target any) error {
	if target == nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullStringPtr(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

func nullIntPtr(value *int) any {
	if value == nil || *value == 0 {
		return nil
	}
	return *value
}

func nullFloatPtr(value *float64) any {
	if value == nil || *value == 0 {
		return nil
	}
	return *value
}

func nullIfZeroInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullIfZeroInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullIfZeroFloat(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return *value
}

func durationFromMillis(raw sql.NullInt64) time.Duration {
	if !raw.Valid || raw.Int64 <= 0 {
		return 0
	}
	return time.Duration(raw.Int64) * time.Millisecond
}

func timeoutMillis(timeout time.Duration) int64 {
	if timeout <= 0 {
		return 0
	}
	return timeout.Milliseconds()
}

func variantName(variant *Variant) string {
	if variant == nil {
		return ""
	}
	name := strings.TrimSpace(variant.Name)
	if name != "" {
		return name
	}
	if model := strings.TrimSpace(variant.ModelID); model != "" {
		return model
	}
	return variant.ID
}
