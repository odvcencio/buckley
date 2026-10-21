package evidence

import (
	"bytes"
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

// ErrIntegrity reports that stored evidence no longer matches its content record.
var ErrIntegrity = errors.New("evidence: stored content integrity check failed")

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
	if err := storage.EnableSQLiteIncrementalVacuum(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: enable incremental vacuum: %w", err)
	}

	if err := storage.EnableSQLiteWAL(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: enable WAL mode: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: run migrations: %w", err)
	}
	if err := storage.RefreshStorageBudget(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("evidence: refresh storage budget: %w", err)
	}

	blobs, err := newSQLiteBlobStore(db, cfg.blobRoot)
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
	if err := storage.RefreshStorageBudget(context.Background(), db); err != nil {
		return nil, fmt.Errorf("evidence: refresh storage budget: %w", err)
	}
	blobs, err := newSQLiteBlobStore(db, blobRoot)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db, blobs: blobs}, nil
}

func newSQLiteBlobStore(db *sql.DB, root string) (*BlobStore, error) {
	blobs, err := NewBlobStore(root)
	if err != nil {
		return nil, err
	}
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		return nil, err
	}
	if path == "" {
		path = fmt.Sprintf("memory:%p", db)
	} else if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	blobs.namespace = "db-" + ContentSHA256Hex([]byte(path))
	return blobs, nil
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
// Returned InlineBody bytes may share obj.InlineBody; changing them never
// changes the persisted content.
func (s *SQLiteStore) Put(ctx context.Context, obj Object) (Object, error) {
	var stored Object
	var pendingBlob string
	err := storage.WithSQLiteWriteTransaction(ctx, s.db, func(conn *sql.Conn) error {
		var err error
		stored, err = s.put(ctx, conn, obj, &pendingBlob)
		if err != nil {
			return err
		}
		// Pin associations in the same transaction as deduplication. Reusing
		// old content must not expose it to a sweep before the caller can pin it.
		reason := ""
		if id, ok := obj.Metadata[MetaRunID].(string); ok && id != "" {
			reason = "run:" + id
		} else if id, ok := obj.Metadata[MetaSessionID].(string); ok && id != "" {
			reason = "session:" + id
		}
		if reason != "" {
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO evidence_pins
				(evidence_id, reason, created_at) VALUES (?, ?, ?)`,
				stored.ID, reason, sqliteTimestamp(time.Now().UTC())); err != nil {
				return fmt.Errorf("evidence: pin association: %w", err)
			}
		}
		return nil
	})
	if err != nil && pendingBlob != "" {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupErr := storage.WithSQLiteWriteTransaction(cleanup, s.db, func(conn *sql.Conn) error {
			var referenced bool
			if err := conn.QueryRowContext(cleanup, `SELECT EXISTS(SELECT 1 FROM evidence_objects WHERE blob_path = ?)`, pendingBlob).Scan(&referenced); err != nil {
				return err
			}
			if referenced {
				return nil
			}
			if err := os.Remove(pendingBlob); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		})
		err = errors.Join(err, cleanupErr)
	}
	if storage.IsStorageBudgetError(err) && !errors.Is(err, storage.ErrStorageBudget) {
		err = fmt.Errorf("%w: %w", storage.ErrStorageBudget, err)
	}
	return stored, err
}

func (s *SQLiteStore) put(ctx context.Context, conn *sql.Conn, obj Object, pendingBlob *string) (Object, error) {
	if obj.Kind == "" {
		return Object{}, fmt.Errorf("evidence: kind is required")
	}

	content := obj.InlineBody
	sha := ContentSHA256Hex(content)

	if existing, err := s.reuseByKindHash(ctx, conn, obj.Kind, sha, content); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Object{}, err
	}
	metadataJSON, err := marshalMetadata(obj.Metadata)
	if err != nil {
		return Object{}, fmt.Errorf("evidence: marshal metadata: %w", err)
	}
	// Check while holding the writer lock and before writing a blob. Accounting
	// triggers cover the object and pin atomically; rejected writes leave no file.
	charge := int64(4096 + 2*(len(metadataJSON)+len(obj.Kind)+len(obj.MediaType)+len(obj.Encoding)))

	id := ComputeContentID(obj.Kind, obj.MediaType, content)

	sensitivity := obj.Sensitivity
	if sensitivity == "" {
		sensitivity = classifySensitivity(obj.Kind, content)
	}

	storageKind := StorageInline
	inlineBody := content
	blobPath := ""
	storedBytes := int64(len(content))
	if len(content) > InlineThreshold {
		storageKind = StorageBlob
		path, bytes, err := s.blobs.write(sha, content, func(bytes int64) error {
			return storage.CheckStorageBudget(ctx, conn, charge+4*bytes)
		})
		if err != nil {
			return Object{}, fmt.Errorf("evidence: write blob: %w", err)
		}
		blobPath = path
		*pendingBlob = path
		storedBytes = bytes
		inlineBody = nil
	} else if err := storage.CheckStorageBudget(ctx, conn, charge+6*storedBytes); err != nil {
		return Object{}, err
	}

	createdAt := obj.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err = conn.ExecContext(ctx, `
		INSERT INTO evidence_objects (
			evidence_id, kind, media_type, encoding, content_sha256, byte_count,
			estimated_tokens, sensitivity, storage_kind, inline_body, blob_path,
			metadata_json, created_at, stored_byte_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id, string(obj.Kind), obj.MediaType, obj.Encoding, sha, int64(len(content)),
		obj.EstimatedTokens, string(sensitivity), string(storageKind), inlineBody, nullableString(blobPath),
		metadataJSON, sqliteTimestamp(createdAt), storedBytes,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			// A concurrent writer won the race for this (kind, content)
			// pair between our dedup check and this insert; defer to it.
			return s.reuseByKindHash(ctx, conn, obj.Kind, sha, content)
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

func (s *SQLiteStore) reuseByKindHash(ctx context.Context, conn *sql.Conn, kind Kind, sha256Hex string, content []byte) (Object, error) {
	obj, err := s.scanObject(conn.QueryRowContext(ctx, objectSelectColumns+" WHERE kind = ? AND content_sha256 = ?", string(kind), sha256Hex))
	if err != nil {
		return Object{}, err
	}
	if obj.ByteCount != int64(len(content)) {
		return Object{}, fmt.Errorf("%w: byte count for %s", ErrIntegrity, obj.ID)
	}
	switch obj.Storage {
	case StorageBlob:
		if err := s.blobs.verify(ctx, obj.BlobPath, content); err != nil {
			return Object{}, fmt.Errorf("evidence: verify existing blob for %s: %w", obj.ID, err)
		}
	case StorageInline:
		if !bytes.Equal(obj.InlineBody, content) {
			return Object{}, fmt.Errorf("%w: inline body for %s", ErrIntegrity, obj.ID)
		}
	default:
		return Object{}, fmt.Errorf("%w: storage kind for %s", ErrIntegrity, obj.ID)
	}
	obj.InlineBody = content
	return obj, nil
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
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO evidence_pins (evidence_id, reason, created_at)
		SELECT evidence_id, ?, ? FROM evidence_objects WHERE evidence_id = ?
		ON CONFLICT(evidence_id, reason) DO NOTHING
	`, reason, sqliteTimestamp(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("evidence: pin %s: %w", id, err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM evidence_objects WHERE evidence_id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
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

// ReleaseByReason removes every pin created for one reason, returning
// how many were released. Run pruning uses it to release a whole run's
// step-evidence pins in one call.
func (s *SQLiteStore) ReleaseByReason(ctx context.Context, reason string) (int64, error) {
	if reason == "" {
		return 0, fmt.Errorf("evidence: release reason is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM evidence_pins WHERE reason = ?`, reason)
	if err != nil {
		return 0, fmt.Errorf("evidence: release pins for %s: %w", reason, err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return released, nil
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

// migrations is the ordered list of schema steps applied under one serialized
// SQLite migration owner.
var migrations = []storage.SQLiteMigration{
	{Version: 1, Name: "evidence_objects", Apply: createEvidenceObjectsTable},
	{Version: 2, Name: "evidence_pins", Apply: createEvidencePinsTable},
	{Version: 3, Name: "evidence_storage_bytes", Apply: migrateEvidenceStorageBytes},
	{Version: 4, Name: "evidence_blob_paths", Apply: createEvidenceBlobPathIndex},
}

func createEvidenceBlobPathIndex(db storage.MigrationDB) error {
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_evidence_blob_path
		ON evidence_objects(blob_path) WHERE blob_path IS NOT NULL`)
	return err
}

func migrateEvidenceStorageBytes(db storage.MigrationDB) error {
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('evidence_objects') WHERE name = 'stored_byte_count')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE evidence_objects ADD COLUMN stored_byte_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var cursor int64
	for {
		rows, err := db.Query(`SELECT rowid, storage_kind, COALESCE(blob_path, ''), byte_count
			FROM evidence_objects WHERE rowid > ? AND stored_byte_count = 0 ORDER BY rowid LIMIT 256`, cursor)
		if err != nil {
			return err
		}
		var batch []struct {
			id         int64
			kind, path string
			bytes      int64
		}
		for rows.Next() {
			var item struct {
				id         int64
				kind, path string
				bytes      int64
			}
			if err := rows.Scan(&item.id, &item.kind, &item.path, &item.bytes); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			cursor = item.id
			if item.kind == string(StorageBlob) {
				if info, err := os.Stat(item.path); err == nil {
					item.bytes = info.Size()
				}
			}
			if _, err := db.Exec(`UPDATE evidence_objects SET stored_byte_count = ? WHERE rowid = ?`, item.bytes, item.id); err != nil {
				return err
			}
		}
	}
}

func createEvidenceObjectsTable(db storage.MigrationDB) error {
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

func createEvidencePinsTable(db storage.MigrationDB) error {
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

// runMigrations applies pending schema migrations via the shared serialized
// SQLite runner, tracked in evidence_schema_migrations (named
// distinctly from pkg/storage's schema_migrations table so the two packages
// can safely share one SQLite file once wiring lands).
func runMigrations(db *sql.DB) error {
	return storage.MigrateSQLite(db, "evidence_schema_migrations", migrations)
}
