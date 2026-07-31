package evidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Query lists ObjectSummary rows matching q. IDs and Kinds are pushed down
// to SQL; Since is applied via an indexed created_at comparison; SessionID,
// RunID, TaskID, Path, EntityID, and Text are matched against each
// candidate's Metadata in Go, because the evidence_objects table (section
// 14.4) has no dedicated columns for those associations. This keeps the
// schema exactly as specified without requiring a JSON1 extension, at the
// cost of scanning more rows in Go for association-heavy queries; full-text
// search over evidence bodies is out of scope for this package (spec's PR 4
// lists "FTS/query where enabled" as an optional test).
func (s *SQLiteStore) Query(ctx context.Context, q Query) ([]ObjectSummary, error) {
	clauses := []string{"1 = 1"}
	args := []any{}

	if len(q.IDs) > 0 {
		placeholders := make([]string, len(q.IDs))
		for i, id := range q.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		clauses = append(clauses, fmt.Sprintf("evidence_id IN (%s)", strings.Join(placeholders, ",")))
	}

	if len(q.Kinds) > 0 {
		placeholders := make([]string, len(q.Kinds))
		for i, k := range q.Kinds {
			placeholders[i] = "?"
			args = append(args, string(k))
		}
		clauses = append(clauses, fmt.Sprintf("kind IN (%s)", strings.Join(placeholders, ",")))
	}

	if !q.Since.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, sqliteTimestamp(q.Since))
	}

	query := objectSelectColumns + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("evidence: query objects: %w", err)
	}
	defer rows.Close()

	var results []ObjectSummary
	for rows.Next() {
		obj, err := scanObjectRows(rows)
		if err != nil {
			return nil, err
		}
		if !matchesAssociations(obj, q) {
			continue
		}
		results = append(results, ObjectSummary{
			ID:              obj.ID,
			Kind:            obj.Kind,
			MediaType:       obj.MediaType,
			ContentSHA256:   obj.ContentSHA256,
			ByteCount:       obj.ByteCount,
			EstimatedTokens: obj.EstimatedTokens,
			Sensitivity:     obj.Sensitivity,
			Storage:         obj.Storage,
			Metadata:        obj.Metadata,
			CreatedAt:       obj.CreatedAt,
		})
		if q.Limit > 0 && len(results) >= q.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence: iterate objects: %w", err)
	}
	return results, nil
}

func matchesAssociations(obj Object, q Query) bool {
	if q.SessionID != "" && metaString(obj.Metadata, MetaSessionID) != q.SessionID {
		return false
	}
	if q.RunID != "" && metaString(obj.Metadata, MetaRunID) != q.RunID {
		return false
	}
	if q.TaskID != "" && metaString(obj.Metadata, MetaTaskID) != q.TaskID {
		return false
	}
	if q.Path != "" && metaString(obj.Metadata, MetaPath) != q.Path {
		return false
	}
	if q.EntityID != "" && metaString(obj.Metadata, MetaEntityID) != q.EntityID {
		return false
	}
	if q.Text != "" && !metadataContainsText(obj.Metadata, q.Text) {
		return false
	}
	return true
}

func metaString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	v, ok := metadata[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func metadataContainsText(metadata map[string]any, text string) bool {
	for _, v := range metadata {
		s, ok := v.(string)
		if ok && strings.Contains(strings.ToLower(s), strings.ToLower(text)) {
			return true
		}
	}
	return false
}

// scanObjectRows scans one row from a *sql.Rows result set produced by a
// query built on objectSelectColumns, mirroring scanObject's *sql.Row
// variant.
func scanObjectRows(rows *sql.Rows) (Object, error) {
	var (
		obj             Object
		kind            string
		mediaType       sql.NullString
		encoding        sql.NullString
		sensitivity     string
		storageKind     string
		inlineBody      []byte
		blobPath        sql.NullString
		metadataJSON    sql.NullString
		estimatedTokens sql.NullInt64
		createdAtRaw    string
	)
	if err := rows.Scan(&obj.ID, &kind, &mediaType, &encoding, &obj.ContentSHA256, &obj.ByteCount,
		&estimatedTokens, &sensitivity, &storageKind, &inlineBody, &blobPath, &metadataJSON, &createdAtRaw); err != nil {
		return Object{}, fmt.Errorf("evidence: scan object row: %w", err)
	}

	obj.Kind = Kind(kind)
	obj.MediaType = mediaType.String
	obj.Encoding = encoding.String
	obj.EstimatedTokens = int(estimatedTokens.Int64)
	obj.Sensitivity = Sensitivity(sensitivity)
	obj.Storage = Storage(storageKind)
	obj.InlineBody = inlineBody
	obj.BlobPath = blobPath.String
	obj.CreatedAt = parseSQLiteTimestamp(createdAtRaw)

	metadata, err := unmarshalMetadata(metadataJSON.String)
	if err != nil {
		return Object{}, fmt.Errorf("evidence: unmarshal metadata for %s: %w", obj.ID, err)
	}
	obj.Metadata = metadata

	return obj, nil
}
