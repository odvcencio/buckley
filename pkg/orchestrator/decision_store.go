package orchestrator

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DecisionStore persists planning decisions to storage
type DecisionStore interface {
	SaveDecision(decision *Decision) error
	GetDecisions(sessionID string) ([]Decision, error)
	GetRecentDecisions(sessionID string, limit int) ([]Decision, error)
	ClearDecisions(sessionID string) error
}

// SQLiteDecisionStore implements DecisionStore using SQLite
type SQLiteDecisionStore struct {
	db *sql.DB
}

// NewSQLiteDecisionStore creates a new SQLite-backed decision store
func NewSQLiteDecisionStore(db *sql.DB) (*SQLiteDecisionStore, error) {
	store := &SQLiteDecisionStore{db: db}
	if err := store.ensureSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize decision schema: %w", err)
	}
	return store, nil
}

// ensureSchema creates the decisions table if it doesn't exist
func (s *SQLiteDecisionStore) ensureSchema() error {
	schema := `
		CREATE TABLE IF NOT EXISTS planning_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			context TEXT NOT NULL,
			options TEXT NOT NULL,
			selected INTEGER NOT NULL,
			reasoning TEXT,
			auto_decided INTEGER NOT NULL DEFAULT 0,
			risk_level TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_decisions_session ON planning_decisions(session_id);
		CREATE INDEX IF NOT EXISTS idx_decisions_timestamp ON planning_decisions(timestamp);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SaveDecision saves a decision to the database
func (s *SQLiteDecisionStore) SaveDecision(decision *Decision) error {
	optionsJSON, err := json.Marshal(decision.Options)
	if err != nil {
		return fmt.Errorf("failed to marshal options: %w", err)
	}

	query := `
		INSERT INTO planning_decisions (session_id, timestamp, context, options, selected, reasoning, auto_decided)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.Exec(query,
		decision.SessionID,
		decision.Timestamp,
		decision.Context,
		string(optionsJSON),
		decision.Selected,
		decision.Reasoning,
		decision.AutoDecided,
	)
	return err
}

// GetDecisions retrieves all decisions for a session
func (s *SQLiteDecisionStore) GetDecisions(sessionID string) ([]Decision, error) {
	query := `
		SELECT timestamp, session_id, context, options, selected, reasoning, auto_decided
		FROM planning_decisions
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`
	return s.queryDecisions(query, sessionID)
}

// GetRecentDecisions retrieves the most recent decisions for a session
func (s *SQLiteDecisionStore) GetRecentDecisions(sessionID string, limit int) ([]Decision, error) {
	query := `
		SELECT timestamp, session_id, context, options, selected, reasoning, auto_decided
		FROM planning_decisions
		WHERE session_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`
	decisions, err := s.queryDecisions(query, sessionID, limit)
	if err != nil {
		return nil, err
	}

	// Reverse to get chronological order
	for i, j := 0, len(decisions)-1; i < j; i, j = i+1, j-1 {
		decisions[i], decisions[j] = decisions[j], decisions[i]
	}
	return decisions, nil
}

// ClearDecisions removes all decisions for a session
func (s *SQLiteDecisionStore) ClearDecisions(sessionID string) error {
	_, err := s.db.Exec("DELETE FROM planning_decisions WHERE session_id = ?", sessionID)
	return err
}

// queryDecisions executes a query and returns decisions
func (s *SQLiteDecisionStore) queryDecisions(query string, args ...any) ([]Decision, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []Decision
	for rows.Next() {
		var d Decision
		var optionsJSON string
		var autoDecided int

		err := rows.Scan(
			&d.Timestamp,
			&d.SessionID,
			&d.Context,
			&optionsJSON,
			&d.Selected,
			&d.Reasoning,
			&autoDecided,
		)
		if err != nil {
			return nil, err
		}

		if err := json.Unmarshal([]byte(optionsJSON), &d.Options); err != nil {
			return nil, fmt.Errorf("failed to unmarshal options: %w", err)
		}
		d.AutoDecided = autoDecided != 0

		decisions = append(decisions, d)
	}

	return decisions, rows.Err()
}

// InMemoryDecisionStore implements DecisionStore in memory (for testing)
type InMemoryDecisionStore struct {
	decisions map[string][]Decision
}

// NewInMemoryDecisionStore creates an in-memory decision store
func NewInMemoryDecisionStore() *InMemoryDecisionStore {
	return &InMemoryDecisionStore{
		decisions: make(map[string][]Decision),
	}
}

// SaveDecision saves a decision to memory
func (s *InMemoryDecisionStore) SaveDecision(decision *Decision) error {
	s.decisions[decision.SessionID] = append(s.decisions[decision.SessionID], *decision)
	return nil
}

// GetDecisions retrieves all decisions for a session
func (s *InMemoryDecisionStore) GetDecisions(sessionID string) ([]Decision, error) {
	decisions := s.decisions[sessionID]
	result := make([]Decision, len(decisions))
	copy(result, decisions)
	return result, nil
}

// GetRecentDecisions retrieves the most recent decisions
func (s *InMemoryDecisionStore) GetRecentDecisions(sessionID string, limit int) ([]Decision, error) {
	decisions := s.decisions[sessionID]
	if len(decisions) <= limit {
		result := make([]Decision, len(decisions))
		copy(result, decisions)
		return result, nil
	}
	start := len(decisions) - limit
	result := make([]Decision, limit)
	copy(result, decisions[start:])
	return result, nil
}

// ClearDecisions removes all decisions for a session
func (s *InMemoryDecisionStore) ClearDecisions(sessionID string) error {
	delete(s.decisions, sessionID)
	return nil
}

// FormatDecisionSummary creates a human-readable summary of decisions
func FormatDecisionSummary(decisions []Decision) string {
	if len(decisions) == 0 {
		return "No planning decisions recorded"
	}

	var result string
	result = fmt.Sprintf("Planning Decisions (%d total)\n", len(decisions))
	result += "─────────────────────────────\n\n"

	autoCount := 0
	for _, d := range decisions {
		if d.AutoDecided {
			autoCount++
		}
	}

	if autoCount > 0 {
		result += fmt.Sprintf("Auto-decided: %d | Manual: %d\n\n", autoCount, len(decisions)-autoCount)
	}

	for i, d := range decisions {
		result += fmt.Sprintf("%d. %s\n", i+1, d.Timestamp.Format(time.RFC822))
		result += fmt.Sprintf("   Task: %s\n", truncateStringForDisplay(d.Context, 60))

		if len(d.Options) > 0 {
			result += "   Options:\n"
			for j, opt := range d.Options {
				marker := "   "
				if j == d.Selected {
					marker = " → "
				}
				result += fmt.Sprintf("     %s%d. %s\n", marker, j+1, opt)
			}
		}

		if d.Reasoning != "" {
			result += fmt.Sprintf("   Reasoning: %s\n", truncateStringForDisplay(d.Reasoning, 80))
		}

		if d.AutoDecided {
			result += "   [Auto-decided in long-run mode]\n"
		}
		result += "\n"
	}

	return result
}

func truncateStringForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
