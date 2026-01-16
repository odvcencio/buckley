// pkg/ralph/memory_store.go
package ralph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const memorySchema = `
CREATE TABLE IF NOT EXISTS ralph_turns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    iteration INTEGER NOT NULL,
    ts TIMESTAMP NOT NULL,
    prompt TEXT NOT NULL,
    response TEXT NOT NULL,
    backend TEXT,
    model TEXT,
    tokens_in INTEGER,
    tokens_out INTEGER,
    cost REAL,
    error TEXT
);

CREATE INDEX IF NOT EXISTS idx_ralph_turns_session_iter ON ralph_turns(session_id, iteration);
CREATE INDEX IF NOT EXISTS idx_ralph_turns_session_ts ON ralph_turns(session_id, ts);

CREATE VIRTUAL TABLE IF NOT EXISTS ralph_turns_fts USING fts5(
    prompt,
    response,
    content='ralph_turns',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS ralph_turns_ai AFTER INSERT ON ralph_turns BEGIN
    INSERT INTO ralph_turns_fts(rowid, prompt, response)
    VALUES (new.id, COALESCE(new.prompt, ''), COALESCE(new.response, ''));
END;

CREATE TRIGGER IF NOT EXISTS ralph_turns_ad AFTER DELETE ON ralph_turns BEGIN
    INSERT INTO ralph_turns_fts(ralph_turns_fts, rowid, prompt, response)
    VALUES ('delete', old.id, COALESCE(old.prompt, ''), COALESCE(old.response, ''));
END;

CREATE TRIGGER IF NOT EXISTS ralph_turns_au AFTER UPDATE ON ralph_turns BEGIN
    INSERT INTO ralph_turns_fts(ralph_turns_fts, rowid, prompt, response)
    VALUES ('delete', old.id, COALESCE(old.prompt, ''), COALESCE(old.response, ''));
    INSERT INTO ralph_turns_fts(rowid, prompt, response)
    VALUES (new.id, COALESCE(new.prompt, ''), COALESCE(new.response, ''));
END;

CREATE TABLE IF NOT EXISTS ralph_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    iteration INTEGER,
    ts TIMESTAMP NOT NULL,
    event_type TEXT NOT NULL,
    tool TEXT,
    file_path TEXT,
    has_error BOOLEAN,
    data TEXT
);

CREATE INDEX IF NOT EXISTS idx_ralph_events_session_type ON ralph_events(session_id, event_type);
CREATE INDEX IF NOT EXISTS idx_ralph_events_session_iter ON ralph_events(session_id, iteration);
CREATE INDEX IF NOT EXISTS idx_ralph_events_tool ON ralph_events(tool);
CREATE INDEX IF NOT EXISTS idx_ralph_events_file_path ON ralph_events(file_path);

CREATE TABLE IF NOT EXISTS ralph_summaries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    start_iteration INTEGER NOT NULL,
    end_iteration INTEGER NOT NULL,
    summary TEXT NOT NULL,
    key_decisions TEXT,
    files_modified TEXT,
    error_patterns TEXT,
    generated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ralph_summaries_session_end ON ralph_summaries(session_id, end_iteration);

CREATE VIRTUAL TABLE IF NOT EXISTS ralph_summaries_fts USING fts5(
    summary,
    content='ralph_summaries',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS ralph_summaries_ai AFTER INSERT ON ralph_summaries BEGIN
    INSERT INTO ralph_summaries_fts(rowid, summary)
    VALUES (new.id, COALESCE(new.summary, ''));
END;

CREATE TRIGGER IF NOT EXISTS ralph_summaries_ad AFTER DELETE ON ralph_summaries BEGIN
    INSERT INTO ralph_summaries_fts(ralph_summaries_fts, rowid, summary)
    VALUES ('delete', old.id, COALESCE(old.summary, ''));
END;

CREATE TRIGGER IF NOT EXISTS ralph_summaries_au AFTER UPDATE ON ralph_summaries BEGIN
    INSERT INTO ralph_summaries_fts(ralph_summaries_fts, rowid, summary)
    VALUES ('delete', old.id, COALESCE(old.summary, ''));
    INSERT INTO ralph_summaries_fts(rowid, summary)
    VALUES (new.id, COALESCE(new.summary, ''));
END;
`

// TurnRecord captures a raw prompt/response pair for a session iteration.
type TurnRecord struct {
	ID        int64
	SessionID string
	Iteration int
	Timestamp time.Time
	Prompt    string
	Response  string
	Backend   string
	Model     string
	TokensIn  int
	TokensOut int
	Cost      float64
	Error     string
}

// StructuredEvent represents an indexed event from the JSONL log.
type StructuredEvent struct {
	ID        int64
	SessionID string
	Iteration int
	Timestamp time.Time
	EventType string
	Tool      string
	FilePath  string
	HasError  bool
	Data      map[string]any
}

// EventQuery filters structured events.
type EventQuery struct {
	SessionID  string
	EventTypes []string
	Tools      []string
	FilePaths  []string
	Since      int
	Until      int
	HasError   *bool
	Query      string
	Limit      int
}

// SessionSummary captures a condensed summary for a range of iterations.
type SessionSummary struct {
	SessionID      string
	StartIteration int
	EndIteration   int
	Summary        string
	KeyDecisions   []string
	FilesModified  []string
	ErrorPatterns  []string
	GeneratedAt    time.Time
}

// MemoryStore manages persistent Ralph session memory.
type MemoryStore struct {
	db *sql.DB
}

// NewMemoryStore creates a new SQLite-backed memory store.
func NewMemoryStore(path string) (*MemoryStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("memory store path required")
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create memory store dir: %w", err)
		}
	}
	if err := ensurePrivateSQLiteFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open memory store: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if _, err := db.Exec(memorySchema); err != nil {
		return nil, fmt.Errorf("init memory schema: %w", err)
	}

	return &MemoryStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *MemoryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// HandleLogEvent ingests a log event into the events table.
func (s *MemoryStore) HandleLogEvent(event LogEvent) {
	if s == nil {
		return
	}

	structured := logEventToStructured(event)
	if structured == nil {
		return
	}

	ctx := context.Background()
	_ = s.SaveEvent(ctx, structured)
}

// SaveTurn stores a raw turn record.
func (s *MemoryStore) SaveTurn(ctx context.Context, turn *TurnRecord) error {
	if s == nil || s.db == nil || turn == nil {
		return fmt.Errorf("memory store not initialized")
	}
	if strings.TrimSpace(turn.SessionID) == "" {
		return fmt.Errorf("session id required")
	}
	if turn.Timestamp.IsZero() {
		turn.Timestamp = time.Now()
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO ralph_turns (session_id, iteration, ts, prompt, response, backend, model, tokens_in, tokens_out, cost, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		turn.SessionID,
		turn.Iteration,
		turn.Timestamp,
		turn.Prompt,
		turn.Response,
		turn.Backend,
		turn.Model,
		turn.TokensIn,
		turn.TokensOut,
		turn.Cost,
		turn.Error,
	)
	if err != nil {
		return fmt.Errorf("insert turn: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		turn.ID = id
	}
	return nil
}

// TrimRawTurns enforces a maximum number of raw turns per session.
func (s *MemoryStore) TrimRawTurns(ctx context.Context, sessionID string, maxTurns int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store not initialized")
	}
	if maxTurns <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM ralph_turns
		WHERE id IN (
			SELECT id FROM ralph_turns
			WHERE session_id = ?
			ORDER BY iteration DESC, id DESC
			LIMIT -1 OFFSET ?
		)
	`, sessionID, maxTurns)
	if err != nil {
		return fmt.Errorf("trim raw turns: %w", err)
	}
	return nil
}

// GetTurnsByIteration retrieves all turn records for a specific iteration.
func (s *MemoryStore) GetTurnsByIteration(ctx context.Context, sessionID string, iteration int) ([]TurnRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, iteration, ts, prompt, response, backend, model, tokens_in, tokens_out, cost, error
		FROM ralph_turns
		WHERE session_id = ? AND iteration = ?
		ORDER BY id ASC
	`, sessionID, iteration)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer rows.Close()

	var turns []TurnRecord
	for rows.Next() {
		var tr TurnRecord
		if err := rows.Scan(
			&tr.ID,
			&tr.SessionID,
			&tr.Iteration,
			&tr.Timestamp,
			&tr.Prompt,
			&tr.Response,
			&tr.Backend,
			&tr.Model,
			&tr.TokensIn,
			&tr.TokensOut,
			&tr.Cost,
			&tr.Error,
		); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, tr)
	}
	return turns, rows.Err()
}

// GetTurnsInRange retrieves turns for an iteration range.
func (s *MemoryStore) GetTurnsInRange(ctx context.Context, sessionID string, startIteration int, endIteration int) ([]TurnRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, iteration, ts, prompt, response, backend, model, tokens_in, tokens_out, cost, error
		FROM ralph_turns
		WHERE session_id = ? AND iteration >= ? AND iteration <= ?
		ORDER BY iteration ASC, id ASC
	`, sessionID, startIteration, endIteration)
	if err != nil {
		return nil, fmt.Errorf("query turn range: %w", err)
	}
	defer rows.Close()

	var turns []TurnRecord
	for rows.Next() {
		var tr TurnRecord
		if err := rows.Scan(
			&tr.ID,
			&tr.SessionID,
			&tr.Iteration,
			&tr.Timestamp,
			&tr.Prompt,
			&tr.Response,
			&tr.Backend,
			&tr.Model,
			&tr.TokensIn,
			&tr.TokensOut,
			&tr.Cost,
			&tr.Error,
		); err != nil {
			return nil, fmt.Errorf("scan turn range: %w", err)
		}
		turns = append(turns, tr)
	}
	return turns, rows.Err()
}

// SearchTurns performs a full-text search across prompt/response text.
func (s *MemoryStore) SearchTurns(ctx context.Context, sessionID, query string, limit int) ([]TurnRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.session_id, t.iteration, t.ts, t.prompt, t.response, t.backend, t.model, t.tokens_in, t.tokens_out, t.cost, t.error
		FROM ralph_turns_fts f
		JOIN ralph_turns t ON t.id = f.rowid
		WHERE f MATCH ? AND t.session_id = ?
		ORDER BY t.iteration DESC
		LIMIT ?
	`, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("search turns: %w", err)
	}
	defer rows.Close()

	var turns []TurnRecord
	for rows.Next() {
		var tr TurnRecord
		if err := rows.Scan(
			&tr.ID,
			&tr.SessionID,
			&tr.Iteration,
			&tr.Timestamp,
			&tr.Prompt,
			&tr.Response,
			&tr.Backend,
			&tr.Model,
			&tr.TokensIn,
			&tr.TokensOut,
			&tr.Cost,
			&tr.Error,
		); err != nil {
			return nil, fmt.Errorf("scan turn search: %w", err)
		}
		turns = append(turns, tr)
	}
	return turns, rows.Err()
}

// SaveEvent stores a structured event record.
func (s *MemoryStore) SaveEvent(ctx context.Context, event *StructuredEvent) error {
	if s == nil || s.db == nil || event == nil {
		return fmt.Errorf("memory store not initialized")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	dataJSON, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("marshal event data: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO ralph_events (session_id, iteration, ts, event_type, tool, file_path, has_error, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.SessionID,
		event.Iteration,
		event.Timestamp,
		event.EventType,
		event.Tool,
		event.FilePath,
		event.HasError,
		string(dataJSON),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		event.ID = id
	}
	return nil
}

// SearchEvents queries structured events using filters.
func (s *MemoryStore) SearchEvents(ctx context.Context, query EventQuery) ([]StructuredEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}

	clauses := []string{"session_id = ?"}
	args := []any{query.SessionID}

	if len(query.EventTypes) > 0 {
		placeholders := make([]string, 0, len(query.EventTypes))
		for _, eventType := range query.EventTypes {
			placeholders = append(placeholders, "?")
			args = append(args, eventType)
		}
		clauses = append(clauses, "event_type IN ("+strings.Join(placeholders, ",")+")")
	}

	if len(query.Tools) > 0 {
		placeholders := make([]string, 0, len(query.Tools))
		for _, tool := range query.Tools {
			placeholders = append(placeholders, "?")
			args = append(args, tool)
		}
		clauses = append(clauses, "tool IN ("+strings.Join(placeholders, ",")+")")
	}

	if len(query.FilePaths) > 0 {
		pathClauses := make([]string, 0, len(query.FilePaths))
		for _, path := range query.FilePaths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			path = strings.ReplaceAll(path, "*", "%")
			pathClauses = append(pathClauses, "file_path LIKE ?")
			args = append(args, path)
		}
		if len(pathClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(pathClauses, " OR ")+")")
		}
	}

	if query.Since > 0 {
		clauses = append(clauses, "iteration >= ?")
		args = append(args, query.Since)
	}
	if query.Until > 0 {
		clauses = append(clauses, "iteration <= ?")
		args = append(args, query.Until)
	}
	if query.HasError != nil {
		clauses = append(clauses, "has_error = ?")
		args = append(args, *query.HasError)
	}
	if strings.TrimSpace(query.Query) != "" {
		clauses = append(clauses, "data LIKE ?")
		args = append(args, "%"+query.Query+"%")
	}

	args = append(args, limit)

	stmt := `
		SELECT id, session_id, iteration, ts, event_type, tool, file_path, has_error, data
		FROM ralph_events
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY ts DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	defer rows.Close()

	var events []StructuredEvent
	for rows.Next() {
		var evt StructuredEvent
		var dataRaw string
		if err := rows.Scan(
			&evt.ID,
			&evt.SessionID,
			&evt.Iteration,
			&evt.Timestamp,
			&evt.EventType,
			&evt.Tool,
			&evt.FilePath,
			&evt.HasError,
			&dataRaw,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if dataRaw != "" {
			_ = json.Unmarshal([]byte(dataRaw), &evt.Data)
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

// SaveSummary stores a session summary.
func (s *MemoryStore) SaveSummary(ctx context.Context, summary *SessionSummary) error {
	if s == nil || s.db == nil || summary == nil {
		return fmt.Errorf("memory store not initialized")
	}
	if summary.GeneratedAt.IsZero() {
		summary.GeneratedAt = time.Now()
	}

	keyDecisionsJSON, err := json.Marshal(summary.KeyDecisions)
	if err != nil {
		return fmt.Errorf("marshal key decisions: %w", err)
	}
	filesJSON, err := json.Marshal(summary.FilesModified)
	if err != nil {
		return fmt.Errorf("marshal files modified: %w", err)
	}
	errorsJSON, err := json.Marshal(summary.ErrorPatterns)
	if err != nil {
		return fmt.Errorf("marshal error patterns: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO ralph_summaries (session_id, start_iteration, end_iteration, summary, key_decisions, files_modified, error_patterns, generated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		summary.SessionID,
		summary.StartIteration,
		summary.EndIteration,
		summary.Summary,
		string(keyDecisionsJSON),
		string(filesJSON),
		string(errorsJSON),
		summary.GeneratedAt,
	)
	if err != nil {
		return fmt.Errorf("insert summary: %w", err)
	}
	return nil
}

// ListSummaries lists summaries since a given iteration.
func (s *MemoryStore) ListSummaries(ctx context.Context, sessionID string, sinceIteration int, limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, start_iteration, end_iteration, summary, key_decisions, files_modified, error_patterns, generated_at
		FROM ralph_summaries
		WHERE session_id = ? AND end_iteration >= ?
		ORDER BY end_iteration DESC
		LIMIT ?
	`, sessionID, sinceIteration, limit)
	if err != nil {
		return nil, fmt.Errorf("list summaries: %w", err)
	}
	defer rows.Close()

	var summaries []SessionSummary
	for rows.Next() {
		var summary SessionSummary
		var decisionsRaw, filesRaw, errorsRaw string
		if err := rows.Scan(
			&summary.SessionID,
			&summary.StartIteration,
			&summary.EndIteration,
			&summary.Summary,
			&decisionsRaw,
			&filesRaw,
			&errorsRaw,
			&summary.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		if decisionsRaw != "" {
			_ = json.Unmarshal([]byte(decisionsRaw), &summary.KeyDecisions)
		}
		if filesRaw != "" {
			_ = json.Unmarshal([]byte(filesRaw), &summary.FilesModified)
		}
		if errorsRaw != "" {
			_ = json.Unmarshal([]byte(errorsRaw), &summary.ErrorPatterns)
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// SearchSummaries performs full-text search over summary text.
func (s *MemoryStore) SearchSummaries(ctx context.Context, sessionID, query string, limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store not initialized")
	}
	if limit <= 0 {
		limit = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.session_id, s.start_iteration, s.end_iteration, s.summary, s.key_decisions, s.files_modified, s.error_patterns, s.generated_at
		FROM ralph_summaries_fts f
		JOIN ralph_summaries s ON s.id = f.rowid
		WHERE f MATCH ? AND s.session_id = ?
		ORDER BY s.end_iteration DESC
		LIMIT ?
	`, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("search summaries: %w", err)
	}
	defer rows.Close()

	var summaries []SessionSummary
	for rows.Next() {
		var summary SessionSummary
		var decisionsRaw, filesRaw, errorsRaw string
		if err := rows.Scan(
			&summary.SessionID,
			&summary.StartIteration,
			&summary.EndIteration,
			&summary.Summary,
			&decisionsRaw,
			&filesRaw,
			&errorsRaw,
			&summary.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf("scan summary search: %w", err)
		}
		if decisionsRaw != "" {
			_ = json.Unmarshal([]byte(decisionsRaw), &summary.KeyDecisions)
		}
		if filesRaw != "" {
			_ = json.Unmarshal([]byte(filesRaw), &summary.FilesModified)
		}
		if errorsRaw != "" {
			_ = json.Unmarshal([]byte(errorsRaw), &summary.ErrorPatterns)
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// PruneRetention removes data older than the retention window.
func (s *MemoryStore) PruneRetention(ctx context.Context, retentionDays int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store not initialized")
	}
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ralph_turns WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("prune turns: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ralph_events WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ralph_summaries WHERE generated_at < ?`, cutoff); err != nil {
		return fmt.Errorf("prune summaries: %w", err)
	}
	return nil
}

func ensurePrivateSQLiteFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("db path cannot be empty")
	}
	if path == ":memory:" || strings.Contains(path, ":memory:") {
		return nil
	}

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat db path: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("create db file: %w", err)
	}
	return f.Close()
}

func logEventToStructured(event LogEvent) *StructuredEvent {
	if strings.TrimSpace(event.SessionID) == "" {
		return nil
	}

	data := event.Data
	tool := ""
	filePath := ""
	hasError := false

	switch event.Event {
	case "tool_call":
		tool = stringFromAny(data["tool"])
	case "tool_result":
		tool = stringFromAny(data["tool"])
		if success, ok := data["success"].(bool); ok && !success {
			hasError = true
		}
	case "file_change":
		filePath = stringFromAny(data["path"])
	case "backend_switch", "model_switch":
		// handled below
	case "error":
		hasError = true
	default:
		return nil
	}

	return &StructuredEvent{
		SessionID: event.SessionID,
		Iteration: event.Iteration,
		Timestamp: event.Timestamp,
		EventType: event.Event,
		Tool:      tool,
		FilePath:  filePath,
		HasError:  hasError,
		Data:      data,
	}
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}
