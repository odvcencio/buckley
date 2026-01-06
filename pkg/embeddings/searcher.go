package embeddings

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EmbeddingProvider describes the subset of Service used by the searcher.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// Searcher provides semantic code search.
type Searcher struct {
	provider EmbeddingProvider
	db       *sql.DB
}

// IndexReport summarizes indexing work.
type IndexReport struct {
	Embedded int
	Skipped  int
	Errors   int
}

const (
	maxIndexFileBytes   = 96 * 1024
	similarityThreshold = 0.72
)

var (
	defaultExtensions = map[string]string{
		".go":    "go",
		".ts":    "typescript",
		".tsx":   "typescriptreact",
		".js":    "javascript",
		".jsx":   "javascriptreact",
		".py":    "python",
		".rb":    "ruby",
		".rs":    "rust",
		".java":  "java",
		".kt":    "kotlin",
		".swift": "swift",
		".cpp":   "cpp",
		".cc":    "cpp",
		".c":     "c",
		".h":     "c",
		".cs":    "csharp",
		".php":   "php",
		".scala": "scala",
		".md":    "markdown",
		".txt":   "text",
		".json":  "json",
		".yaml":  "yaml",
		".yml":   "yaml",
	}
	skipDirs = map[string]struct{}{
		".git":         {},
		".buckley":     {},
		"node_modules": {},
		"vendor":       {},
		"dist":         {},
		"build":        {},
		"coverage":     {},
		"bin":          {},
		"out":          {},
	}
	errStopWalk = errors.New("searcher: stop walk")
)

// NewSearcher creates a new semantic searcher.
func NewSearcher(provider EmbeddingProvider, db *sql.DB) *Searcher {
	return &Searcher{
		provider: provider,
		db:       db,
	}
}

// IndexDirectory indexes all supported files in a directory tree.
func (s *Searcher) IndexDirectory(ctx context.Context, rootPath string) (IndexReport, error) {
	var report IndexReport
	err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			report.Errors++
			return nil
		}

		if entry.IsDir() {
			if path == rootPath {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if _, skip := skipDirs[name]; skip || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !isIndexableExtension(path) {
			report.Skipped++
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			report.Errors++
			return nil
		}

		if info.Size() > maxIndexFileBytes {
			report.Skipped++
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			report.Errors++
			return nil
		}

		displayPath, err := filepath.Rel(rootPath, path)
		if err != nil {
			displayPath = path
		}

		embedded, err := s.indexContent(ctx, indexContentInput{
			StoragePath: path,
			DisplayPath: displayPath,
			Content:     string(content),
			ModTime:     info.ModTime(),
			Language:    languageForExtension(path),
			Source:      "code",
		})
		if err != nil {
			report.Errors++
			return nil
		}
		if embedded {
			report.Embedded++
		} else {
			report.Skipped++
		}
		return nil
	})

	if errors.Is(err, context.Canceled) {
		return report, err
	}
	if err != nil {
		report.Errors++
	}
	return report, err
}

// IndexMarkdownFiles indexes a fixed set of documentation files.
func (s *Searcher) IndexMarkdownFiles(ctx context.Context, rootPath string, filePaths []string) error {
	for _, filePath := range filePaths {
		info, err := os.Stat(filePath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		display := filePath
		if rel, err := filepath.Rel(rootPath, filePath); err == nil {
			display = rel
		}

		if _, err := s.indexContent(ctx, indexContentInput{
			StoragePath: filePath,
			DisplayPath: display,
			Content:     string(content),
			ModTime:     info.ModTime(),
			Language:    "markdown",
			Source:      "docs",
		}); err != nil {
			return err
		}
	}
	return nil
}

// IndexFile indexes a single file's content. Primarily provided for backwards compatibility.
func (s *Searcher) IndexFile(ctx context.Context, filePath, content string) error {
	_, err := s.indexContent(ctx, indexContentInput{
		StoragePath: filePath,
		DisplayPath: filePath,
		Content:     content,
		ModTime:     time.Now(),
		Language:    languageForExtension(filePath),
		Source:      "manual",
	})
	return err
}

// Search performs semantic search.
func (s *Searcher) Search(ctx context.Context, query string, limit int) (SearchResults, error) {
	queryEmbedding, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT file_path, content, embedding, metadata
		FROM embeddings
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query embeddings: %w", err)
	}
	defer rows.Close()

	var results SearchResults
	for rows.Next() {
		var filePath, content string
		var embeddingBytes []byte
		var metadata sql.NullString

		if err := rows.Scan(&filePath, &content, &embeddingBytes, &metadata); err != nil {
			continue
		}

		embedding, err := deserializeEmbedding(embeddingBytes)
		if err != nil {
			continue
		}

		similarity, err := CosineSimilarity(queryEmbedding, embedding)
		if err != nil {
			continue
		}

		meta := parseMetadata(metadata.String)
		if meta["file"] == "" {
			meta["file"] = normalizeDisplayPath(filePath)
		}

		results = append(results, SearchResult{
			ID:         filePath,
			Content:    content,
			Similarity: similarity,
			Metadata:   meta,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return results, nil
	}

	sort.Sort(results)

	selected := make(SearchResults, 0, len(results))
	for i, result := range results {
		if i > 0 && result.Similarity < similarityThreshold {
			if limit > 0 && len(selected) >= limit {
				break
			}
			continue
		}
		selected = append(selected, result)
		if limit > 0 && len(selected) >= limit {
			break
		}
	}

	if len(selected) == 0 {
		if limit > 0 && len(results) > limit {
			return results[:limit], nil
		}
		return results, nil
	}

	return selected, nil
}

// ClearIndex removes all indexed embeddings.
func (s *Searcher) ClearIndex(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM embeddings")
	return err
}

// GetIndexCount returns the number of indexed items.
func (s *Searcher) GetIndexCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM embeddings").Scan(&count)
	return count, err
}

// HasIndexedFile checks if a specific file is already indexed.
func (s *Searcher) HasIndexedFile(ctx context.Context, filePath string) (bool, error) {
	normalized := normalizeStoragePath(filePath)
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings WHERE file_path = ?`, normalized).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// LatestSourceModTime returns the latest source_mod_time stored in the index.
func (s *Searcher) LatestSourceModTime(ctx context.Context) (time.Time, error) {
	var numeric sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(CASE
			WHEN typeof(source_mod_time) IN ('integer','real') THEN source_mod_time
			ELSE NULL
		END)
		FROM embeddings
	`).Scan(&numeric)
	if err != nil {
		return time.Time{}, err
	}
	if numeric.Valid && numeric.Int64 > 0 {
		return time.UnixMicro(numeric.Int64).UTC(), nil
	}

	var seconds sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(strftime('%s', source_mod_time)) FROM embeddings`).Scan(&seconds); err != nil {
		return time.Time{}, err
	}
	if seconds.Valid {
		return time.Unix(int64(seconds.Float64), 0).UTC(), nil
	}
	return time.Time{}, nil
}

// HasNewerFiles reports whether the workspace contains files newer than the indexed timestamp.
func (s *Searcher) HasNewerFiles(rootPath string, since time.Time) (bool, error) {
	if since.IsZero() {
		return true, nil
	}

	err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path == rootPath {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if _, skip := skipDirs[name]; skip || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isIndexableExtension(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(since) {
			return errStopWalk
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

type indexContentInput struct {
	StoragePath string
	DisplayPath string
	Content     string
	ModTime     time.Time
	Language    string
	Source      string
}

type fileRecord struct {
	Hash    string
	ModTime time.Time
}

func (s *Searcher) indexContent(ctx context.Context, input indexContentInput) (bool, error) {
	storagePath := normalizeStoragePath(input.StoragePath)
	if storagePath == "" {
		storagePath = normalizeStoragePath(input.DisplayPath)
	}
	if storagePath == "" {
		return false, fmt.Errorf("storage path required for indexing")
	}

	contentHash := computeHash(input.Content)
	existing, err := s.lookupFileRecord(ctx, storagePath)
	if err != nil {
		return false, err
	}
	if existing != nil && existing.Hash == contentHash {
		if !input.ModTime.IsZero() && (existing.ModTime.IsZero() || input.ModTime.After(existing.ModTime)) {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE embeddings
				SET source_mod_time = ?
				WHERE file_path = ?
			`, input.ModTime.UTC().UnixMicro(), storagePath); err != nil {
				return false, err
			}
		}
		return false, nil
	}

	embedding, err := s.provider.Embed(ctx, input.Content)
	if err != nil {
		return false, fmt.Errorf("failed to generate embedding for %s: %w", storagePath, err)
	}

	embeddingBytes, err := serializeEmbedding(embedding)
	if err != nil {
		return false, fmt.Errorf("failed to serialize embedding: %w", err)
	}

	metadataJSON, err := buildMetadataJSON(input.DisplayPath, input.Language, input.Source)
	if err != nil {
		return false, err
	}

	indexedAt := time.Now()
	sourceMod := input.ModTime
	if sourceMod.IsZero() {
		sourceMod = indexedAt
	}
	sourceModValue := sourceMod.UTC().UnixMicro()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO embeddings (file_path, content_hash, content, embedding, metadata, source_mod_time, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			content_hash=excluded.content_hash,
			content=excluded.content,
			embedding=excluded.embedding,
			metadata=excluded.metadata,
			source_mod_time=excluded.source_mod_time,
			created_at=excluded.created_at
	`, storagePath, contentHash, input.Content, embeddingBytes, metadataJSON, sourceModValue, indexedAt)
	if err != nil {
		return false, fmt.Errorf("failed to store embedding: %w", err)
	}

	return true, nil
}

func (s *Searcher) lookupFileRecord(ctx context.Context, storagePath string) (*fileRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT content_hash, source_mod_time
		FROM embeddings
		WHERE file_path = ?
	`, storagePath)

	var hash sql.NullString
	var modTime sql.NullTime
	if err := row.Scan(&hash, &modTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	record := &fileRecord{Hash: hash.String}
	if modTime.Valid {
		record.ModTime = modTime.Time
	}
	return record, nil
}

func buildMetadataJSON(displayPath, language, source string) (string, error) {
	meta := map[string]string{
		"file": normalizeDisplayPath(displayPath),
	}
	if language != "" {
		meta["language"] = language
	}
	if source != "" {
		meta["source"] = source
	}
	bytes, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func parseMetadata(raw string) map[string]string {
	result := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return result
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return result
	}
	for key, value := range parsed {
		if str, ok := value.(string); ok {
			result[key] = str
		}
	}
	return result
}

func languageForExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := defaultExtensions[ext]; ok {
		return lang
	}
	return ""
}

func isIndexableExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := defaultExtensions[ext]
	return ok
}

func normalizeStoragePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func normalizeDisplayPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	cleaned = strings.TrimPrefix(cleaned, "./")
	return cleaned
}

// serializeEmbedding converts a float64 slice to bytes.
func serializeEmbedding(embedding []float64) ([]byte, error) {
	embeddingLen := len(embedding)
	if embeddingLen < 0 {
		return nil, fmt.Errorf("invalid embedding length")
	}
	if embeddingLen > math.MaxInt32 {
		return nil, fmt.Errorf("embedding too large: %d", embeddingLen)
	}
	if embeddingLen > (math.MaxInt-4)/8 {
		return nil, fmt.Errorf("embedding too large: %d", embeddingLen)
	}

	buf := make([]byte, 4+embeddingLen*8)
	binary.BigEndian.PutUint32(buf[:4], uint32(embeddingLen))
	for i, v := range embedding {
		binary.BigEndian.PutUint64(buf[4+i*8:4+(i+1)*8], math.Float64bits(v))
	}
	return buf, nil
}

// deserializeEmbedding converts bytes back to float64 slice.
func deserializeEmbedding(data []byte) ([]float64, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid embedding data")
	}
	length := binary.BigEndian.Uint32(data[:4])
	expected := 4 + int(length)*8
	if len(data) != expected {
		return nil, fmt.Errorf("invalid embedding length")
	}
	embedding := make([]float64, length)
	for i := 0; i < int(length); i++ {
		offset := 4 + i*8
		embedding[i] = math.Float64frombits(binary.BigEndian.Uint64(data[offset : offset+8]))
	}
	return embedding, nil
}

// computeHash computes a stable hash for content.
func computeHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:])[:32]
}

// SearchResult represents a search result with similarity score.
type SearchResult struct {
	ID         string
	Content    string
	Similarity float64
	Metadata   map[string]string
}

// SearchResults is a slice of search results with sorting capabilities.
type SearchResults []SearchResult

func (r SearchResults) Len() int           { return len(r) }
func (r SearchResults) Less(i, j int) bool { return r[i].Similarity > r[j].Similarity }
func (r SearchResults) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
