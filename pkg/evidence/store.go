package evidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"m31labs.dev/buckley/pkg/storage"
)

// ErrNotFound is returned when a requested evidence object does not exist.
var ErrNotFound = errors.New("evidence: not found")

// Query selects evidence objects for listing. Zero-valued fields are not
// applied as filters. SessionID, RunID, TaskID, Path, and EntityID match
// against Object.Metadata (see query.go), because evidence_objects has no
// dedicated columns for those associations (section 14.4's schema stores
// only kind/media_type/encoding/hash/size/sensitivity/storage/body/metadata).
type Query struct {
	IDs       []string
	Kinds     []Kind
	SessionID string
	RunID     string
	TaskID    string
	Path      string
	EntityID  string
	Text      string
	Since     time.Time
	Limit     int
}

// Store is the evidence store's public port (section 13.4).
type Store interface {
	Put(ctx context.Context, object Object) (Object, error)
	Get(ctx context.Context, id string) (Object, error)
	Query(ctx context.Context, q Query) ([]ObjectSummary, error)
	Pin(ctx context.Context, id, reason string) error
	Release(ctx context.Context, id, reason string) error
}

// Metadata keys the store understands for association-based filtering (see
// Query and query.go). Callers populate these in Object.Metadata to make an
// object discoverable by session, run, task, path, or entity.
const (
	MetaSessionID = "session_id"
	MetaRunID     = "run_id"
	MetaTaskID    = "task_id"
	MetaPath      = "path"
	MetaEntityID  = "entity_id"
)

// SQLiteStore is the SQLite-backed implementation of Store. It follows the
// same connection idiom as pkg/storage.Store (WAL mode, busy_timeout,
// foreign_keys, private file permissions, versioned migrations); see ADR
// 0001.
type SQLiteStore struct {
	db    *sql.DB
	blobs *BlobStore
}

var _ Store = (*SQLiteStore)(nil)

type storeConfig struct {
	blobRoot string
}

// Option configures New.
type Option func(*storeConfig)

// WithBlobRoot overrides the blob store root directory. By default it is
// "<dir of dbPath>/evidence", matching section 13.2's default layout of
// "<buckley-data-dir>/evidence/".
func WithBlobRoot(path string) Option {
	return func(c *storeConfig) { c.blobRoot = path }
}

// New creates a SQLiteStore backed by SQLite at dbPath, initializing WAL
// mode, foreign keys, and the evidence schema.
func New(dbPath string, opts ...Option) (*SQLiteStore, error) {
	cfg := storeConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	filePath, onDisk := sqliteFilePathFromDSN(dbPath)
	if onDisk {
		if dir := filepath.Dir(filePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("evidence: create database directory: %w", err)
			}
		}
		if err := ensurePrivateSQLiteFile(filePath); err != nil {
			return nil, err
		}
		if cfg.blobRoot == "" {
			cfg.blobRoot = filepath.Join(filepath.Dir(filePath), "evidence")
		}
	}
	if cfg.blobRoot == "" {
		cfg.blobRoot = "evidence"
	}

	pragmas := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	dsn := dbPath
	if strings.Contains(dsn, "?") {
		dsn += "&" + pragmas
	} else {
		dsn += "?" + pragmas
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("evidence: open database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: enable WAL mode: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: run migrations: %w", err)
	}

	blobs, err := NewBlobStore(cfg.blobRoot)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db, blobs: blobs}, nil
}

// NewWithDB wraps an already-open, already-migrated *sql.DB. It is intended
// for composing an evidence store onto a shared database connection (for
// example, one owned by pkg/storage or pkg/runledger) once wiring lands in a
// later PR; the caller remains responsible for that connection's lifecycle
// and pragma configuration.
func NewWithDB(db *sql.DB, blobRoot string) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("evidence: db cannot be nil")
	}
	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("evidence: run migrations: %w", err)
	}
	blobs, err := NewBlobStore(blobRoot)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db, blobs: blobs}, nil
}

// Close closes the underlying database connection opened by New. It must
// not be called on a store constructed with NewWithDB, whose caller owns the
// connection lifecycle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection, primarily for tests and
// for composing other packages (such as pkg/runledger) onto the same
// database file.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// Blobs returns the store's blob backend, primarily for retention and
// orphan-cleanup callers.
func (s *SQLiteStore) Blobs() *BlobStore {
	return s.blobs
}

// Put inserts obj, deduplicating by (kind, content sha256) per the
// evidence_dedup unique index (section 14.4). If an object with the same
// kind and content already exists, Put returns that existing object
// unchanged; it does not attempt to reconcile a different declared
// MediaType, since deduplication in this schema is keyed by content, not by
// media type (see the doc comment on ComputeContentID for why the ID
// formula and the dedup index disagree on that point, and how this method
// resolves it).
func (s *SQLiteStore) Put(ctx context.Context, obj Object) (Object, error) {
	if obj.Kind == "" {
		return Object{}, fmt.Errorf("evidence: kind is required")
	}

	content := obj.InlineBody
	sha := ContentSHA256Hex(content)

	if existing, err := s.getByKindHash(ctx, obj.Kind, sha); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Object{}, err
	}

	id := ComputeContentID(obj.Kind, obj.MediaType, content)

	sensitivity := obj.Sensitivity
	if sensitivity == "" {
		sensitivity = classifySensitivity(obj.Kind, content)
	}

	storageKind := StorageInline
	inlineBody := content
	blobPath := ""
	if len(content) > InlineThreshold {
		storageKind = StorageBlob
		path, err := s.blobs.Write(sha, content)
		if err != nil {
			return Object{}, fmt.Errorf("evidence: write blob: %w", err)
		}
		blobPath = path
		inlineBody = nil
	}

	metadataJSON, err := marshalMetadata(obj.Metadata)
	if err != nil {
		return Object{}, fmt.Errorf("evidence: marshal metadata: %w", err)
	}

	createdAt := obj.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO evidence_objects (
			evidence_id, kind, media_type, encoding, content_sha256, byte_count,
			estimated_tokens, sensitivity, storage_kind, inline_body, blob_path,
			metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id, string(obj.Kind), obj.MediaType, obj.Encoding, sha, int64(len(content)),
		obj.EstimatedTokens, string(sensitivity), string(storageKind), inlineBody, nullableString(blobPath),
		metadataJSON, sqliteTimestamp(createdAt),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			// A concurrent writer won the race for this (kind, content)
			// pair between our dedup check and this insert; defer to it.
			return s.getByKindHash(ctx, obj.Kind, sha)
		}
		return Object{}, fmt.Errorf("evidence: insert object: %w", err)
	}

	return Object{
		ID:              id,
		Kind:            obj.Kind,
		MediaType:       obj.MediaType,
		Encoding:        obj.Encoding,
		ContentSHA256:   sha,
		ByteCount:       int64(len(content)),
		EstimatedTokens: obj.EstimatedTokens,
		Sensitivity:     sensitivity,
		Storage:         storageKind,
		InlineBody:      content,
		BlobPath:        blobPath,
		Metadata:        obj.Metadata,
		CreatedAt:       createdAt,
	}, nil
}

// Get retrieves the object with the given ID. If the object is stored as a
// blob, Get transparently reads and decompresses it, so callers always
// receive materialized content in InlineBody regardless of storage tier.
func (s *SQLiteStore) Get(ctx context.Context, id string) (Object, error) {
	obj, err := s.scanObject(s.db.QueryRowContext(ctx, objectSelectColumns+" WHERE evidence_id = ?", id))
	if err != nil {
		return Object{}, err
	}
	return s.materialize(obj)
}

func (s *SQLiteStore) getByKindHash(ctx context.Context, kind Kind, sha256Hex string) (Object, error) {
	obj, err := s.scanObject(s.db.QueryRowContext(ctx, objectSelectColumns+" WHERE kind = ? AND content_sha256 = ?", string(kind), sha256Hex))
	if err != nil {
		return Object{}, err
	}
	return s.materialize(obj)
}

func (s *SQLiteStore) materialize(obj Object) (Object, error) {
	if obj.Storage == StorageBlob && obj.BlobPath != "" {
		body, err := s.blobs.Read(obj.BlobPath)
		if err != nil {
			return Object{}, fmt.Errorf("evidence: read blob for %s: %w", obj.ID, err)
		}
		obj.InlineBody = body
	}
	return obj, nil
}

// Pin marks id as retained indefinitely for reason. Multiple independent
// pins may exist for the same object (for example, a user pin and a commit
// receipt pin); it is released only once every reason is released.
func (s *SQLiteStore) Pin(ctx context.Context, id, reason string) error {
	if reason == "" {
		return fmt.Errorf("evidence: pin reason is required")
	}
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO evidence_pins (evidence_id, reason, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(evidence_id, reason) DO NOTHING
	`, id, reason, sqliteTimestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("evidence: pin %s: %w", id, err)
	}
	return nil
}

// Release removes a previously created pin for id and reason. Releasing a
// pin that does not exist is not an error.
func (s *SQLiteStore) Release(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM evidence_pins WHERE evidence_id = ? AND reason = ?`, id, reason)
	if err != nil {
		return fmt.Errorf("evidence: release pin on %s: %w", id, err)
	}
	return nil
}

// isPinned reports whether id has at least one active pin.
func (s *SQLiteStore) isPinned(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_pins WHERE evidence_id = ?`, id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("evidence: check pin status: %w", err)
	}
	return count > 0, nil
}

const objectSelectColumns = `SELECT evidence_id, kind, media_type, encoding, content_sha256, byte_count,
	estimated_tokens, sensitivity, storage_kind, inline_body, blob_path, metadata_json, created_at
	FROM evidence_objects`

func (s *SQLiteStore) scanObject(row *sql.Row) (Object, error) {
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
	err := row.Scan(&obj.ID, &kind, &mediaType, &encoding, &obj.ContentSHA256, &obj.ByteCount,
		&estimatedTokens, &sensitivity, &storageKind, &inlineBody, &blobPath, &metadataJSON, &createdAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, fmt.Errorf("evidence: scan object: %w", err)
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

func marshalMetadata(metadata map[string]any) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func unmarshalMetadata(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func sqliteTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseSQLiteTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", raw); err == nil {
		return t
	}
	return time.Time{}
}

func isUniqueConstraintError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == sqlite3.SQLITE_CONSTRAINT || code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	return false
}

func isBusyError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
	}
	return false
}

func sqliteFilePathFromDSN(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil || !strings.EqualFold(strings.TrimSpace(u.Scheme), "file") {
			return "", false
		}
		path := strings.TrimSpace(u.Path)
		if path == "" {
			path = strings.TrimSpace(u.Opaque)
		}
		if path == "" || path == ":memory:" {
			return "", false
		}
		return path, true
	}
	if strings.Contains(dsn, "://") {
		return "", false
	}
	return dsn, true
}

func ensurePrivateSQLiteFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("evidence: db path cannot be empty")
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("evidence: stat db path: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("evidence: create db file: %w", err)
	}
	return f.Close()
}

// migrations is the ordered list of schema steps applied via storage.Migrate.
var migrations = []storage.Migration{
	{Version: 1, Name: "evidence_objects", Apply: createEvidenceObjectsTable},
	{Version: 2, Name: "evidence_pins", Apply: createEvidencePinsTable},
}

func createEvidenceObjectsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS evidence_objects (
			evidence_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			media_type TEXT,
			encoding TEXT,
			content_sha256 TEXT NOT NULL,
			byte_count INTEGER NOT NULL,
			estimated_tokens INTEGER,
			sensitivity TEXT NOT NULL,
			storage_kind TEXT NOT NULL,
			inline_body BLOB,
			blob_path TEXT,
			metadata_json TEXT,
			created_at TIMESTAMP NOT NULL
		);

		CREATE UNIQUE INDEX IF NOT EXISTS idx_evidence_dedup
		ON evidence_objects(kind, content_sha256);

		CREATE INDEX IF NOT EXISTS idx_evidence_created_at
		ON evidence_objects(created_at);
	`)
	if err != nil {
		return fmt.Errorf("create evidence_objects: %w", err)
	}
	return nil
}

func createEvidencePinsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS evidence_pins (
			evidence_id TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (evidence_id, reason),
			FOREIGN KEY (evidence_id) REFERENCES evidence_objects(evidence_id) ON DELETE CASCADE
		);
	`)
	if err != nil {
		return fmt.Errorf("create evidence_pins: %w", err)
	}
	return nil
}

// runMigrations applies pending schema migrations via the shared
// storage.Migrate runner, tracked in evidence_schema_migrations (named
// distinctly from pkg/storage's schema_migrations table so the two packages
// can safely share one SQLite file once wiring lands).
func runMigrations(db *sql.DB) error {
	return storage.Migrate(db, "evidence_schema_migrations", migrations)
}
