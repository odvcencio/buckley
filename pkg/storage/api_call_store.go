package storage

import "time"

// APICall represents an API cost tracking record.
type APICall struct {
	ID               int64     `json:"id"`
	SessionID        string    `json:"sessionId"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	Cost             float64   `json:"cost"`
	Timestamp        time.Time `json:"timestamp"`
}

// SaveAPICall records an API call and updates the owning session's total cost.
func (s *Store) SaveAPICall(call *APICall) error {
	timestamp := call.Timestamp.UTC().Format("2006-01-02 15:04:05")
	query := `
		INSERT INTO api_calls (session_id, model, prompt_tokens, completion_tokens, cost, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
		call.SessionID,
		call.Model,
		call.PromptTokens,
		call.CompletionTokens,
		call.Cost,
		timestamp,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	call.ID = id

	updateQuery := `
		UPDATE sessions
		SET total_cost = total_cost + ?
		WHERE session_id = ?
	`
	_, err = s.db.Exec(updateQuery, call.Cost, call.SessionID)
	return err
}

// GetDailyCost returns the total API cost accrued today.
func (s *Store) GetDailyCost() (float64, error) {
	query := `
		SELECT COALESCE(SUM(cost), 0)
		FROM api_calls
		WHERE strftime('%Y-%m-%d', timestamp) = strftime('%Y-%m-%d', 'now')
	`
	var cost float64
	err := s.db.QueryRow(query).Scan(&cost)
	return cost, err
}

// GetMonthlyCost returns the total API cost for the current month.
func (s *Store) GetMonthlyCost() (float64, error) {
	query := `
		SELECT COALESCE(SUM(cost), 0)
		FROM api_calls
		WHERE strftime('%Y-%m', timestamp) = strftime('%Y-%m', 'now')
	`
	var cost float64
	err := s.db.QueryRow(query).Scan(&cost)
	return cost, err
}
