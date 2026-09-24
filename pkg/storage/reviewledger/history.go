package reviewledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"m31labs.dev/buckley/pkg/orchestrator"
)

// Old code-mode runs have evidence and execution state but no saved merge
// verdict or reliable repository identity. Preserve the facts without guessing.
func (l *Ledger) backfillReviewRuns(ctx context.Context, filename string) (int, error) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='agent_runs'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT run_id, coalesce(model_id,''), started_at, ended_at,
 json_object('run_id',run_id,'session_id',session_id,'task_id',task_id,'agent_id',agent_id,'model_id',model_id,'provider_id',provider_id,'backend',backend,'status',status,'started_at',started_at,'ended_at',ended_at,'outcome_json',outcome_json)
 FROM agent_runs WHERE backend='review-code-mode' AND ended_at IS NOT NULL ORDER BY started_at`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, modelID, raw string
		var start time.Time
		var end sql.NullTime
		if err := rows.Scan(&id, &modelID, &start, &end, &raw); err != nil {
			return count, err
		}
		events, err := db.QueryContext(ctx, `SELECT json_object('event_id',event_id,'sequence',sequence,'event_type',event_type,'timestamp',timestamp,'payload_json',payload_json,'evidence_ids_json',evidence_ids_json,'receipt_ids_json',receipt_ids_json) FROM run_events WHERE run_id=? ORDER BY sequence`, id)
		if err != nil {
			return count, err
		}
		history := struct {
			Run    json.RawMessage   `json:"run"`
			Events []json.RawMessage `json:"events"`
		}{Run: json.RawMessage(raw), Events: []json.RawMessage{}}
		for events.Next() {
			var event string
			if err := events.Scan(&event); err != nil {
				events.Close()
				return count, err
			}
			history.Events = append(history.Events, json.RawMessage(event))
		}
		err = events.Err()
		events.Close()
		if err != nil {
			return count, err
		}
		data, err := json.Marshal(history)
		if err != nil {
			return count, err
		}
		hash := Hash(data)
		if err := l.UploadBlob(ctx, hash, data); err != nil {
			return count, err
		}
		record := orchestrator.ReviewRecord{SchemaVersion: 1, ReviewID: "legacy-" + id, Repository: "local/legacy-review", Ref: "unknown", Model: modelID, StartedAt: start.UTC(), EndedAt: end.Time.UTC(), Verdict: "UNKNOWN", Findings: json.RawMessage("[]"), Verification: []orchestrator.ReviewVerification{}, Evidence: []string{hash}, Error: "Historical code-mode record: repository revisions and final verdict were not stored."}
		if !end.Valid {
			record.EndedAt = record.StartedAt
			record.Error += " End time unknown; start time is used as the lower bound."
		}
		evidenceRows, err := db.QueryContext(ctx, `SELECT DISTINCT content_sha256 FROM evidence_objects WHERE json_extract(metadata_json,'$.run_id')=? OR evidence_id IN (SELECT value FROM run_events,json_each(run_events.evidence_ids_json) WHERE run_events.run_id=?) ORDER BY content_sha256`, id, id)
		if err != nil {
			return count, err
		}
		for evidenceRows.Next() {
			var hash string
			if err := evidenceRows.Scan(&hash); err != nil {
				evidenceRows.Close()
				return count, err
			}
			record.Evidence = append(record.Evidence, hash)
		}
		err = evidenceRows.Err()
		evidenceRows.Close()
		if err != nil {
			return count, err
		}
		key, err := ManifestKey(record)
		if err != nil {
			return count, err
		}
		body, err := json.Marshal(record)
		if err != nil {
			return count, err
		}
		err = l.objects.Create(ctx, l.key(key), body, "application/json")
		if errors.Is(err, ErrExists) {
			existing, readErr := l.objects.Read(ctx, l.key(key))
			if readErr != nil {
				return count, readErr
			}
			if string(existing) != string(body) {
				return count, fmt.Errorf("immutable historical record conflict: %s", id)
			}
			err = nil
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}
