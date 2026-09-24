package reviewledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"m31labs.dev/buckley/pkg/orchestrator"
)

var ErrExists = errors.New("review ledger object exists")
var ErrNotFound = errors.New("review ledger record not found")

// Objects is the immutable object storage boundary. Create must never overwrite.
type Objects interface {
	Create(context.Context, string, []byte, string) error
	Read(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]string, error)
}

type Ledger struct {
	objects Objects
	bucket  string
	prefix  string
	queue   string
}

var _ orchestrator.ReviewLedger = (*Ledger)(nil)

func New(objects Objects, bucket, prefix, queue string) *Ledger {
	return &Ledger{objects: objects, bucket: bucket, prefix: strings.Trim(prefix, "/"), queue: queue}
}

func Hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var revisionPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func BlobKey(hash string) (string, error) {
	if !hashPattern.MatchString(hash) {
		return "", fmt.Errorf("invalid blob hash")
	}
	return "blobs/sha256/" + hash[:2] + "/" + hash[2:4] + "/" + hash + ".zst", nil
}

func ManifestKey(record orchestrator.ReviewRecord) (string, error) {
	if record.SchemaVersion != 1 || !repoPattern.MatchString(record.Repository) || !idPattern.MatchString(record.ReviewID) {
		return "", fmt.Errorf("invalid review schema, repository, or ID")
	}
	if record.PRNumber < 0 || record.BaseSHA != "" && !revisionPattern.MatchString(record.BaseSHA) || record.HeadSHA != "" && !revisionPattern.MatchString(record.HeadSHA) {
		return "", fmt.Errorf("invalid review PR number or revision")
	}
	for _, part := range strings.Split(record.Repository, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("invalid repository")
		}
	}
	if record.StartedAt.IsZero() || record.EndedAt.Before(record.StartedAt) || record.Verdict == "" || !json.Valid(record.Findings) {
		return "", fmt.Errorf("invalid review times, verdict, or findings")
	}
	ref := record.Ref
	if record.PRNumber > 0 {
		ref = strconv.Itoa(record.PRNumber)
	}
	if ref == "" {
		ref = "unknown"
	}
	// Encode branch slashes so each ref occupies one path component.
	ref = strings.ReplaceAll(strings.ReplaceAll(ref, "%", "%25"), "/", "%2F")
	if ref == "." || ref == ".." || strings.ContainsAny(ref, "\\\x00\r\n") {
		return "", fmt.Errorf("invalid review ref")
	}
	head := record.HeadSHA
	if head == "" {
		head = "unknown"
	}
	if !idPattern.MatchString(head) {
		return "", fmt.Errorf("invalid head SHA")
	}
	for _, hash := range record.Evidence {
		if _, err := BlobKey(hash); err != nil {
			return "", err
		}
	}
	for _, command := range record.Verification {
		if _, err := BlobKey(command.LogHash); err != nil {
			return "", err
		}
	}
	return "reviews/" + record.Repository + "/" + ref + "/" + head + "/" + record.ReviewID + ".json", nil
}

type pending struct {
	Bucket string                    `json:"bucket"`
	Prefix string                    `json:"prefix"`
	Record orchestrator.ReviewRecord `json:"record"`
	Blobs  map[string][]byte         `json:"blobs"`
}

// Record saves a durable retry entry before making any network request.
func (l *Ledger) Record(ctx context.Context, record orchestrator.ReviewRecord, blobs map[string][]byte) error {
	if _, err := ManifestKey(record); err != nil {
		return err
	}
	needed := append([]string(nil), record.Evidence...)
	for _, command := range record.Verification {
		needed = append(needed, command.LogHash)
	}
	for _, hash := range needed {
		body, ok := blobs[hash]
		if !ok || Hash(body) != hash {
			return fmt.Errorf("missing or invalid evidence blob %s", hash)
		}
	}
	item := pending{Bucket: l.bucket, Prefix: l.prefix, Record: record, Blobs: blobs}
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode retry entry: %w", err)
	}
	if err := os.MkdirAll(l.queue, 0700); err != nil {
		return err
	}
	filename := filepath.Join(l.queue, record.ReviewID+"-"+Hash(data)+".json")
	if err := durableWrite(filename, data); err != nil {
		return err
	}
	if err := l.upload(ctx, item); err != nil {
		return fmt.Errorf("review %s queued: %w", record.ReviewID, err)
	}
	return removePending(filename)
}

func durableWrite(filename string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// A link publishes only complete data and never replaces an existing entry.
	if err := os.Link(file.Name(), filename); err != nil && !os.IsExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(filename))
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func removePending(filename string) error {
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(filename))
}

func (l *Ledger) Retry(ctx context.Context) error {
	entries, err := os.ReadDir(l.queue)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		filename := filepath.Join(l.queue, entry.Name())
		data, err := os.ReadFile(filename)
		if os.IsNotExist(err) {
			continue
		}
		var item pending
		if err == nil {
			err = json.Unmarshal(data, &item)
		}
		if err == nil && (item.Bucket != l.bucket || item.Prefix != l.prefix) {
			continue
		}
		if err == nil {
			err = l.upload(ctx, item)
		}
		if err == nil {
			err = removePending(filename)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("retry %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

func (l *Ledger) key(key string) string {
	if l.prefix == "" {
		return key
	}
	return l.prefix + "/" + key
}

func (l *Ledger) upload(ctx context.Context, item pending) error {
	key, err := ManifestKey(item.Record)
	if err != nil {
		return err
	}
	needed := append([]string(nil), item.Record.Evidence...)
	for _, command := range item.Record.Verification {
		needed = append(needed, command.LogHash)
	}
	for _, hash := range needed {
		body, ok := item.Blobs[hash]
		if !ok || Hash(body) != hash {
			return fmt.Errorf("invalid queued blob %s", hash)
		}
	}
	for hash, body := range item.Blobs {
		if err := l.UploadBlob(ctx, hash, body); err != nil {
			return err
		}
	}
	body, err := json.Marshal(item.Record)
	if err != nil {
		return err
	}
	err = l.objects.Create(ctx, l.key(key), body, "application/json")
	if errors.Is(err, ErrExists) {
		existing, readErr := l.objects.Read(ctx, l.key(key))
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("immutable manifest conflict: %s", key)
		}
		return nil
	}
	return err
}

func (l *Ledger) UploadBlob(ctx context.Context, hash string, body []byte) error {
	key, err := BlobKey(hash)
	if err != nil {
		return err
	}
	if Hash(body) != hash {
		return fmt.Errorf("blob hash mismatch: %s", hash)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	defer enc.Close()
	err = l.objects.Create(ctx, l.key(key), enc.EncodeAll(body, nil), "application/zstd")
	if errors.Is(err, ErrExists) {
		return nil
	}
	return err
}

func (l *Ledger) List(ctx context.Context, repo string, pr int) ([]orchestrator.ReviewRecord, error) {
	if !repoPattern.MatchString(repo) || pr < 0 || strings.Contains(repo, "../") || strings.HasSuffix(repo, "/..") {
		return nil, fmt.Errorf("--repo must be owner/repo and --pr must be nonnegative")
	}
	prefix := l.key("reviews/" + repo + "/")
	if pr > 0 {
		prefix += strconv.Itoa(pr) + "/"
	}
	keys, err := l.objects.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	records := make([]orchestrator.ReviewRecord, 0, len(keys))
	for _, key := range keys {
		if !strings.HasSuffix(key, ".json") {
			continue
		}
		record, err := l.read(ctx, key)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StartedAt.Before(records[j].StartedAt) })
	return records, nil
}

func (l *Ledger) Show(ctx context.Context, id string) (orchestrator.ReviewRecord, error) {
	if !idPattern.MatchString(id) {
		return orchestrator.ReviewRecord{}, fmt.Errorf("invalid review ID")
	}
	keys, err := l.objects.List(ctx, l.key("reviews/"))
	if err != nil {
		return orchestrator.ReviewRecord{}, err
	}
	var match string
	for _, key := range keys {
		if path.Base(key) == id+".json" {
			if match != "" {
				return orchestrator.ReviewRecord{}, fmt.Errorf("ambiguous review ID")
			}
			match = key
		}
	}
	if match == "" {
		return orchestrator.ReviewRecord{}, ErrNotFound
	}
	return l.read(ctx, match)
}

func (l *Ledger) read(ctx context.Context, key string) (orchestrator.ReviewRecord, error) {
	var record orchestrator.ReviewRecord
	data, err := l.objects.Read(ctx, key)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	expected, err := ManifestKey(record)
	if err != nil {
		return record, err
	}
	if l.key(expected) != key {
		return record, fmt.Errorf("manifest identity does not match object path")
	}
	return record, nil
}
