package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/modelprofile"
)

// BehaviorProfileStore is the SQLite adapter for immutable, versioned model
// behavior facts. It stores only aggregate profile JSON; prompts, source,
// tool arguments, and secrets never enter this table.
type BehaviorProfileStore struct {
	store *Store
}

var _ modelprofile.Store = (*BehaviorProfileStore)(nil)

func NewBehaviorProfileStore(store *Store) *BehaviorProfileStore {
	return &BehaviorProfileStore{store: store}
}

func (s *BehaviorProfileStore) Put(ctx context.Context, profile modelprofile.Profile) error {
	if err := profileStoreContextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.store == nil || s.store.db == nil {
		return ErrStoreClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	profile = profile.Normalize()
	if err := profile.Validate(); err != nil {
		return err
	}
	digest, err := profile.Digest()
	if err != nil {
		return err
	}
	body, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("marshal model behavior profile: %w", err)
	}

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model behavior profile write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT profile_digest FROM model_behavior_profiles WHERE model_id = ? AND profile_version = ?`, profile.ModelID, profile.Version).Scan(&existing)
	switch {
	case err == nil:
		if existing != digest {
			return fmt.Errorf("model behavior profile %s version %s already exists with different facts", profile.ModelID, profile.Version)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit existing model behavior profile: %w", err)
		}
		return nil
	case err != sql.ErrNoRows:
		return fmt.Errorf("read model behavior profile version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO model_behavior_profiles (model_id, profile_version, profile_digest, profile_json, measured_at)
		VALUES (?, ?, ?, ?, ?)`, profile.ModelID, profile.Version, digest, string(body), nullableProfileTimestamp(profile)); err != nil {
		return fmt.Errorf("insert model behavior profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model behavior profile: %w", err)
	}
	return nil
}

func (s *BehaviorProfileStore) Get(ctx context.Context, modelID, version string) (modelprofile.Profile, bool, error) {
	if err := profileStoreContextErr(ctx); err != nil {
		return modelprofile.Profile{}, false, err
	}
	if s == nil || s.store == nil || s.store.db == nil {
		return modelprofile.Profile{}, false, ErrStoreClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var body string
	err := s.store.db.QueryRowContext(ctx, `SELECT profile_json FROM model_behavior_profiles WHERE model_id = ? AND profile_version = ?`, strings.TrimSpace(modelID), strings.TrimSpace(version)).Scan(&body)
	if err == sql.ErrNoRows {
		return modelprofile.Profile{}, false, nil
	}
	if err != nil {
		return modelprofile.Profile{}, false, fmt.Errorf("get model behavior profile: %w", err)
	}
	profile, err := decodeBehaviorProfile(body)
	if err != nil {
		return modelprofile.Profile{}, false, err
	}
	return profile, true, nil
}

func (s *BehaviorProfileStore) Latest(ctx context.Context, modelID string) (modelprofile.Profile, bool, error) {
	profiles, err := s.List(ctx, modelID)
	if err != nil || len(profiles) == 0 {
		return modelprofile.Profile{}, false, err
	}
	return profiles[len(profiles)-1], true, nil
}

func (s *BehaviorProfileStore) List(ctx context.Context, modelID string) ([]modelprofile.Profile, error) {
	if err := profileStoreContextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, ErrStoreClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT profile_json FROM model_behavior_profiles
		WHERE model_id = ?
		ORDER BY measured_at ASC, profile_version ASC`, strings.TrimSpace(modelID))
	if err != nil {
		return nil, fmt.Errorf("list model behavior profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]modelprofile.Profile, 0)
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan model behavior profile: %w", err)
		}
		profile, err := decodeBehaviorProfile(body)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model behavior profiles: %w", err)
	}
	return profiles, nil
}

func decodeBehaviorProfile(body string) (modelprofile.Profile, error) {
	var profile modelprofile.Profile
	if err := json.Unmarshal([]byte(body), &profile); err != nil {
		return modelprofile.Profile{}, fmt.Errorf("decode stored model behavior profile: %w", err)
	}
	profile = profile.Normalize()
	if err := profile.Validate(); err != nil {
		return modelprofile.Profile{}, fmt.Errorf("validate stored model behavior profile: %w", err)
	}
	return profile, nil
}

func nullableProfileTimestamp(profile modelprofile.Profile) any {
	if profile.MeasuredAt.IsZero() {
		return nil
	}
	return sqliteTimestamp(profile.MeasuredAt)
}

func profileStoreContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
