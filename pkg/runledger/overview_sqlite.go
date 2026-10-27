package runledger

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// SessionRunOverview reads the latest root and its descendants in one SQLite
// snapshot. Event bodies and historical runs never enter the Go result.
func (s *SQLiteStore) SessionRunOverview(ctx context.Context, sessionID string) (*RunOverview, error) {
	types := make([]string, 0, len(runStatusEvents))
	for kind := range runStatusEvents {
		types = append(types, kind)
	}
	sort.Strings(types)
	placeholders := make([]string, len(types))
	args := []any{sessionID}
	for i, kind := range types {
		placeholders[i] = "?"
		args = append(args, kind)
	}
	query := `WITH RECURSIVE latest_root AS (
		SELECT run_id FROM agent_runs
		WHERE session_id = ? AND COALESCE(parent_run_id, '') = ''
		ORDER BY ` + runStartedAtEpochKey + ` DESC, ` + runStartedAtFractionKey + ` DESC, run_id ASC
		LIMIT 1
	), run_tree(run_id) AS (
		SELECT run_id FROM latest_root
		UNION
		SELECT child.run_id FROM agent_runs AS child JOIN run_tree ON child.parent_run_id = run_tree.run_id
	)
	SELECT ar.run_id, ar.session_id, ar.parent_run_id, ar.task_id, ar.agent_id, ar.model_id,
		ar.provider_id, ar.backend, ar.status, ar.started_at, ar.ended_at,
		(SELECT COUNT(*) FROM run_events WHERE run_id = ar.run_id),
		(SELECT event_type FROM run_events WHERE run_id = ar.run_id AND event_type IN (` + strings.Join(placeholders, ",") + `)
		 ORDER BY sequence DESC LIMIT 1)
	FROM agent_runs AS ar WHERE ar.run_id IN (SELECT run_id FROM run_tree)
	ORDER BY ` + runStartedAtEpochKey + ` ASC, ` + runStartedAtFractionKey + ` ASC, ar.run_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: session run overview: %w", err)
	}
	defer rows.Close()

	var nodes []*RunOverview
	byID := map[string]*RunOverview{}
	var root *RunOverview
	for rows.Next() {
		node := &RunOverview{}
		var parent, task, agent, model, provider, backend, ended, lastType sql.NullString
		var started string
		if err := rows.Scan(&node.Run.RunID, &node.Run.SessionID, &parent, &task, &agent, &model,
			&provider, &backend, &node.Run.Status, &started, &ended, &node.EventCount, &lastType); err != nil {
			return nil, fmt.Errorf("runledger: scan run overview: %w", err)
		}
		node.Run.ParentRunID, node.Run.TaskID, node.Run.AgentID = parent.String, task.String, agent.String
		node.Run.ModelID, node.Run.ProviderID, node.Run.Backend = model.String, provider.String, backend.String
		node.Run.StartedAt = parseSQLiteTimestamp(started)
		if ended.Valid && ended.String != "" {
			value := parseSQLiteTimestamp(ended.String)
			node.Run.EndedAt = &value
		}
		node.Status = runStatusEvents[lastType.String]
		if node.Run.ParentRunID == "" {
			root = node
		}
		byID[node.Run.RunID] = node
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: read run overview: %w", err)
	}
	for _, node := range nodes {
		if node == root {
			continue
		}
		parent := byID[node.Run.ParentRunID]
		if parent == nil {
			return nil, fmt.Errorf("runledger: overview parent %s not found", node.Run.ParentRunID)
		}
		parent.Children = append(parent.Children, node)
	}
	return root, nil
}
