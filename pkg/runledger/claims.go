package runledger

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// AgentClaim is one durable workspace reservation. Resources are
// adapter-normalized workspace-relative identifiers; the ledger preserves
// them verbatim so the coordinator, not SQLite, owns path semantics.
type AgentClaim struct {
	ClaimID       string     `json:"claim_id"`
	RunID         string     `json:"run_id"`
	Resource      string     `json:"resource"`
	AcquiredAt    time.Time  `json:"acquired_at"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
	ReleaseReason string     `json:"release_reason,omitempty"`
}

// ClaimQuery selects durable workspace reservations.
type ClaimQuery struct {
	RunID           string `json:"run_id,omitempty"`
	IncludeReleased bool   `json:"include_released,omitempty"`
}

// ClaimJournal is an optional durable extension to Store. It intentionally
// remains separate from Store so existing callers and lightweight test doubles
// are not forced to implement claim persistence.
type ClaimJournal interface {
	AcquireClaims(ctx context.Context, runID string, resources []string) ([]AgentClaim, error)
	ReleaseClaims(ctx context.Context, runID string, resources []string, reason string) error
	ListClaims(ctx context.Context, query ClaimQuery) ([]AgentClaim, error)
}

var _ ClaimJournal = (*SQLiteStore)(nil)

// AcquireClaims reserves a set of workspace resources atomically. The lock
// row is deliberately touched before reading active claims so the operation
// serializes across independent SQLite connections, including processes that
// open the same WAL database. Prefix overlap is treated as a conflict: a
// claim on "pkg" excludes a later claim on "pkg/tool" and vice versa.
func (s *SQLiteStore) AcquireClaims(ctx context.Context, runID string, resources []string) ([]AgentClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runledger: claim journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("runledger: claim run_id is required")
	}
	resources = cleanClaimResources(resources)
	if len(resources) == 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("runledger: begin claim transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_claim_locks SET touched_at = ? WHERE lock_key = 'workspace'`, sqliteTimestamp(time.Now().UTC())); err != nil {
		return nil, fmt.Errorf("runledger: lock claims: %w", err)
	}

	active, err := listClaimsWith(ctx, tx, ClaimQuery{})
	if err != nil {
		return nil, err
	}
	byResource := make(map[string]AgentClaim, len(active))
	for _, claim := range active {
		byResource[claim.Resource] = claim
	}
	for _, resource := range resources {
		for _, claim := range active {
			if !claimResourcesOverlap(resource, claim.Resource) {
				continue
			}
			if claim.RunID != runID {
				return nil, fmt.Errorf("runledger: workspace claim conflict: %q is held by run %s", resource, claim.RunID)
			}
		}
	}

	result := make([]AgentClaim, 0, len(resources))
	now := time.Now().UTC()
	for _, resource := range resources {
		if existing, ok := byResource[resource]; ok && existing.RunID == runID {
			result = append(result, existing)
			continue
		}
		claim := AgentClaim{
			ClaimID:    "claim_" + ulid.Make().String(),
			RunID:      runID,
			Resource:   resource,
			AcquiredAt: now,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_claims (claim_id, run_id, resource, acquired_at)
			VALUES (?, ?, ?, ?)
		`, claim.ClaimID, claim.RunID, claim.Resource, sqliteTimestamp(claim.AcquiredAt)); err != nil {
			return nil, fmt.Errorf("runledger: insert claim: %w", err)
		}
		result = append(result, claim)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("runledger: commit claims: %w", err)
	}
	return result, nil
}

// ReleaseClaims releases exact resources owned by runID. An empty resource
// list releases every active claim held by the run, which is useful at a
// terminal lifecycle boundary.
func (s *SQLiteStore) ReleaseClaims(ctx context.Context, runID string, resources []string, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: claim journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("runledger: claim run_id is required")
	}
	resources = cleanClaimResources(resources)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runledger: begin release transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_claim_locks SET touched_at = ? WHERE lock_key = 'workspace'`, sqliteTimestamp(time.Now().UTC())); err != nil {
		return fmt.Errorf("runledger: lock claims: %w", err)
	}

	query := `UPDATE agent_claims SET released_at = ?, release_reason = ? WHERE run_id = ? AND released_at IS NULL`
	args := []any{sqliteTimestamp(time.Now().UTC()), strings.TrimSpace(reason), runID}
	if len(resources) > 0 {
		placeholders := make([]string, len(resources))
		for i, resource := range resources {
			placeholders[i] = "?"
			args = append(args, resource)
		}
		query += " AND resource IN (" + strings.Join(placeholders, ",") + ")"
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("runledger: release claims: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runledger: commit release claims: %w", err)
	}
	return nil
}

// ListClaims returns claims in acquisition order.
func (s *SQLiteStore) ListClaims(ctx context.Context, query ClaimQuery) ([]AgentClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runledger: claim journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listClaimsWith(ctx, s.db, query)
}

type claimQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listClaimsWith(ctx context.Context, queryer claimQueryer, query ClaimQuery) ([]AgentClaim, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if runID := strings.TrimSpace(query.RunID); runID != "" {
		clauses = append(clauses, "run_id = ?")
		args = append(args, runID)
	}
	if !query.IncludeReleased {
		clauses = append(clauses, "released_at IS NULL")
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT claim_id, run_id, resource, acquired_at, released_at, release_reason
		FROM agent_claims
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY acquired_at ASC, claim_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: list claims: %w", err)
	}
	defer rows.Close()

	claims := []AgentClaim{}
	for rows.Next() {
		var (
			claim       AgentClaim
			acquiredRaw string
			releasedRaw sql.NullString
			reason      sql.NullString
		)
		if err := rows.Scan(&claim.ClaimID, &claim.RunID, &claim.Resource, &acquiredRaw, &releasedRaw, &reason); err != nil {
			return nil, fmt.Errorf("runledger: scan claim: %w", err)
		}
		claim.AcquiredAt = parseSQLiteTimestamp(acquiredRaw)
		if releasedRaw.Valid && strings.TrimSpace(releasedRaw.String) != "" {
			releasedAt := parseSQLiteTimestamp(releasedRaw.String)
			claim.ReleasedAt = &releasedAt
		}
		claim.ReleaseReason = reason.String
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate claims: %w", err)
	}
	return claims, nil
}

func cleanClaimResources(resources []string) []string {
	seen := make(map[string]struct{}, len(resources))
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			continue
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		out = append(out, resource)
	}
	sort.Strings(out)
	return out
}

func claimResourcesOverlap(left, right string) bool {
	left = strings.Trim(strings.TrimSpace(left), "/")
	right = strings.Trim(strings.TrimSpace(right), "/")
	if left == "." || right == "." {
		return true
	}
	if left == "" || right == "" {
		return left == right
	}
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
