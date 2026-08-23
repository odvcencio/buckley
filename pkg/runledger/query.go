package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const runSelectColumns = `SELECT run_id, session_id, parent_run_id, task_id, agent_id, model_id,
	provider_id, backend, status, started_at, ended_at, budget_json, outcome_json FROM agent_runs`

// runStartedAtOrderKey converts every supported timestamp representation to
// an epoch-second plus fractional-second pair. Existing databases may contain
// RFC3339Nano values with variable fractional widths, where plain TEXT order
// incorrectly places .12 before .1 in ascending order.
const (
	runStartedAtEpochKey    = `CAST(strftime('%s', started_at) AS INTEGER)`
	runStartedAtFractionKey = `CASE WHEN substr(started_at, 20, 1) = '.'
		THEN CAST('0.' || substr(started_at, 21) AS REAL)
		ELSE 0 END`
)

// GetRun implements Store.
func (s *SQLiteStore) GetRun(ctx context.Context, runID string) (AgentRun, error) {
	row := s.db.QueryRowContext(ctx, runSelectColumns+" WHERE run_id = ?", runID)
	return scanRun(row)
}

// ListRuns implements Store.
func (s *SQLiteStore) ListRuns(ctx context.Context, q RunQuery) ([]AgentRun, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if q.SessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, q.SessionID)
	}
	if q.TaskID != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, q.TaskID)
	}
	if q.ParentRunID != "" {
		clauses = append(clauses, "parent_run_id = ?")
		args = append(args, q.ParentRunID)
	}
	order := runStartedAtEpochKey + " ASC, " + runStartedAtFractionKey + " ASC, run_id ASC"
	if q.Order == RunOrderNewestFirst {
		order = runStartedAtEpochKey + " DESC, " + runStartedAtFractionKey + " DESC, run_id DESC"
	}
	query := runSelectColumns + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + order
	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: list runs: %w", err)
	}
	defer rows.Close()

	var runs []AgentRun
	for rows.Next() {
		run, err := scanRunRows(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (AgentRun, error) {
	var (
		run          AgentRun
		parentRunID  sql.NullString
		taskID       sql.NullString
		agentID      sql.NullString
		modelID      sql.NullString
		providerID   sql.NullString
		backend      sql.NullString
		startedAtRaw string
		endedAtRaw   sql.NullString
		budgetJSON   sql.NullString
		outcomeJSON  sql.NullString
	)
	err := row.Scan(&run.RunID, &run.SessionID, &parentRunID, &taskID, &agentID, &modelID, &providerID,
		&backend, &run.Status, &startedAtRaw, &endedAtRaw, &budgetJSON, &outcomeJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRun{}, ErrNotFound
	}
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: scan run: %w", err)
	}
	run.ParentRunID = parentRunID.String
	run.TaskID = taskID.String
	run.AgentID = agentID.String
	run.ModelID = modelID.String
	run.ProviderID = providerID.String
	run.Backend = backend.String
	run.StartedAt = parseSQLiteTimestamp(startedAtRaw)
	if endedAtRaw.Valid && endedAtRaw.String != "" {
		t := parseSQLiteTimestamp(endedAtRaw.String)
		run.EndedAt = &t
	}
	run.Budget, err = unmarshalJSONMap(budgetJSON.String)
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: unmarshal budget: %w", err)
	}
	run.Outcome, err = unmarshalJSONMap(outcomeJSON.String)
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: unmarshal outcome: %w", err)
	}
	return run, nil
}

func scanRunRows(rows *sql.Rows) (AgentRun, error) {
	return scanRun(rows)
}

// ListEvents implements Store. Results are ordered by (run_id, sequence),
// matching the replay read order required by section 14.5.
func (s *SQLiteStore) ListEvents(ctx context.Context, q EventQuery) ([]Event, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if q.RunID != "" {
		clauses = append(clauses, "re.run_id = ?")
		args = append(args, q.RunID)
	}
	if q.TaskID != "" {
		clauses = append(clauses, "re.task_id = ?")
		args = append(args, q.TaskID)
	}
	if len(q.Types) > 0 {
		placeholders := make([]string, len(q.Types))
		for i, t := range q.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		clauses = append(clauses, fmt.Sprintf("re.event_type IN (%s)", strings.Join(placeholders, ",")))
	}
	if !q.Since.IsZero() {
		clauses = append(clauses, "re.timestamp >= ?")
		args = append(args, sqliteTimestamp(q.Since))
	}

	// session_id and parent_run_id are not duplicated onto run_events; they
	// are derived here via a join to agent_runs (see the doc comment on
	// Event in event.go and on SQLiteStore in sqlite.go).
	query := `
		SELECT re.event_id, re.run_id, re.sequence, re.event_type, re.timestamp, re.task_id, re.agent_id,
		       re.model_id, re.provider_id, re.backend, re.snapshot_id, re.payload_json,
		       re.evidence_ids_json, re.receipt_ids_json, re.redaction_version,
		       ar.session_id, ar.parent_run_id
		FROM run_events re
		JOIN agent_runs ar ON ar.run_id = re.run_id
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY re.run_id ASC, re.sequence ASC
	`
	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func scanEventRow(rows *sql.Rows) (Event, error) {
	var (
		event           Event
		taskID          sql.NullString
		agentID         sql.NullString
		modelID         sql.NullString
		providerID      sql.NullString
		backend         sql.NullString
		snapshotID      sql.NullString
		payloadJSON     sql.NullString
		evidenceIDsJSON sql.NullString
		receiptIDsJSON  sql.NullString
		timestampRaw    string
		parentRunID     sql.NullString
	)
	err := rows.Scan(&event.ID, &event.RunID, &event.Sequence, &event.Type, &timestampRaw, &taskID, &agentID,
		&modelID, &providerID, &backend, &snapshotID, &payloadJSON, &evidenceIDsJSON, &receiptIDsJSON,
		&event.Redaction, &event.SessionID, &parentRunID)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: scan event: %w", err)
	}
	event.SchemaVersion = SchemaVersion
	event.Timestamp = parseSQLiteTimestamp(timestampRaw)
	event.TaskID = taskID.String
	event.AgentID = agentID.String
	event.ModelID = modelID.String
	event.ProviderID = providerID.String
	event.Backend = backend.String
	event.SnapshotID = snapshotID.String
	event.ParentRunID = parentRunID.String

	event.Payload, err = unmarshalJSONMap(payloadJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: unmarshal payload: %w", err)
	}
	event.EvidenceIDs, err = unmarshalJSONStrings(evidenceIDsJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: unmarshal evidence_ids: %w", err)
	}
	event.ReceiptIDs, err = unmarshalJSONStrings(receiptIDsJSON.String)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: unmarshal receipt_ids: %w", err)
	}
	return event, nil
}
