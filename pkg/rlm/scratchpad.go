package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/oklog/ulid/v2"
)

const defaultScratchpadSummaryLimit = 200

// Scratchpad stores sub-agent outputs with optional persistence.
type Scratchpad struct {
	mu         sync.RWMutex
	entries    map[string]*Entry
	store      *storage.Store
	summarizer func([]byte) string
	config     ScratchpadConfig
	rawBytes   int64
	onWrite    func(context.Context, []EntrySummary)
}

// NewScratchpad constructs a scratchpad backed by an optional store.
func NewScratchpad(store *storage.Store, summarizer func([]byte) string, cfg ScratchpadConfig) *Scratchpad {
	cfg = normalizeScratchpadConfig(cfg)
	return &Scratchpad{
		entries:    make(map[string]*Entry),
		store:      store,
		summarizer: summarizer,
		config:     cfg,
	}
}

// SetOnWrite registers a callback after successful writes.
func (s *Scratchpad) SetOnWrite(handler func(context.Context, []EntrySummary)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onWrite = handler
	s.mu.Unlock()
}

// Write stores raw data and returns the entry key.
func (s *Scratchpad) Write(ctx context.Context, req WriteRequest) (string, error) {
	if s == nil {
		return "", fmt.Errorf("scratchpad is nil")
	}
	now := time.Now().UTC()
	key := strings.TrimSpace(req.Key)
	if key == "" {
		key = ulid.Make().String()
	}
	entryType := req.Type
	if entryType == "" {
		entryType = EntryTypeAnalysis
	}
	createdAt := req.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = s.summarize(req.Raw)
	}
	entry := &Entry{
		Key:        key,
		Type:       entryType,
		Raw:        req.Raw,
		Summary:    summary,
		Metadata:   cloneMetadata(req.Metadata),
		CreatedBy:  strings.TrimSpace(req.CreatedBy),
		CreatedAt:  createdAt,
		LastAccess: now,
	}

	var persistEntry *storage.ScratchpadEntry
	var wroteSummary *EntrySummary
	s.mu.Lock()
	s.purgeExpiredLocked(now)
	if s.isExpired(entry, now) {
		s.mu.Unlock()
		return key, nil
	}
	if existing := s.entries[key]; existing != nil {
		s.rawBytes -= entrySize(existing)
	}
	s.entries[key] = entry
	s.rawBytes += entrySize(entry)
	s.enforceLimitsLocked(now)
	if s.entries[key] != nil {
		wroteSummary = toEntrySummary(entry)
	}
	if s.shouldPersist(entry, now) && s.store != nil {
		metadataJSON := ""
		if entry.Metadata != nil {
			if data, err := json.Marshal(entry.Metadata); err == nil {
				metadataJSON = string(data)
			}
		}
		persistEntry = &storage.ScratchpadEntry{
			Key:       entry.Key,
			EntryType: string(entry.Type),
			Raw:       entry.Raw,
			Summary:   entry.Summary,
			Metadata:  metadataJSON,
			CreatedBy: entry.CreatedBy,
			CreatedAt: entry.CreatedAt,
		}
	}
	s.mu.Unlock()

	if persistEntry != nil {
		if _, err := s.store.UpsertScratchpadEntry(ctx, *persistEntry); err != nil {
			return key, err
		}
	}
	if wroteSummary != nil {
		s.emitOnWrite(ctx, []EntrySummary{*wroteSummary})
	}

	return key, nil
}

// WriteBatch stores multiple entries efficiently.
func (s *Scratchpad) WriteBatch(ctx context.Context, requests []WriteRequest) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("scratchpad is nil")
	}
	if len(requests) == 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	keys := make([]string, len(requests))
	var persistEntries []storage.ScratchpadEntry
	writtenByKey := make(map[string]*Entry, len(requests))

	s.mu.Lock()
	s.purgeExpiredLocked(now)

	for i, req := range requests {
		key := strings.TrimSpace(req.Key)
		if key == "" {
			key = ulid.Make().String()
		}
		keys[i] = key

		entryType := req.Type
		if entryType == "" {
			entryType = EntryTypeAnalysis
		}
		createdAt := req.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		summary := strings.TrimSpace(req.Summary)
		if summary == "" {
			summary = s.summarize(req.Raw)
		}

		entry := &Entry{
			Key:        key,
			Type:       entryType,
			Raw:        req.Raw,
			Summary:    summary,
			Metadata:   cloneMetadata(req.Metadata),
			CreatedBy:  strings.TrimSpace(req.CreatedBy),
			CreatedAt:  createdAt,
			LastAccess: now,
		}

		if s.isExpired(entry, now) {
			continue
		}
		if existing := s.entries[key]; existing != nil {
			s.rawBytes -= entrySize(existing)
		}
		s.entries[key] = entry
		s.rawBytes += entrySize(entry)
		writtenByKey[key] = entry

		if s.shouldPersist(entry, now) && s.store != nil {
			metadataJSON := ""
			if entry.Metadata != nil {
				if data, err := json.Marshal(entry.Metadata); err == nil {
					metadataJSON = string(data)
				}
			}
			persistEntries = append(persistEntries, storage.ScratchpadEntry{
				Key:       entry.Key,
				EntryType: string(entry.Type),
				Raw:       entry.Raw,
				Summary:   entry.Summary,
				Metadata:  metadataJSON,
				CreatedBy: entry.CreatedBy,
				CreatedAt: entry.CreatedAt,
			})
		}
	}

	s.enforceLimitsLocked(now)
	var wroteSummaries []EntrySummary
	for _, entry := range writtenByKey {
		if s.entries[entry.Key] != nil {
			if summary := toEntrySummary(entry); summary != nil {
				wroteSummaries = append(wroteSummaries, *summary)
			}
		}
	}
	s.mu.Unlock()

	// Persist all entries that need persistence
	for _, entry := range persistEntries {
		if _, err := s.store.UpsertScratchpadEntry(ctx, entry); err != nil {
			// Log but don't fail - memory write succeeded
			continue
		}
	}
	if len(wroteSummaries) > 0 {
		s.emitOnWrite(ctx, wroteSummaries)
	}

	return keys, nil
}

// Inspect returns a summary-only view for coordinators.
func (s *Scratchpad) Inspect(ctx context.Context, key string) (*EntrySummary, error) {
	entry, err := s.getEntry(ctx, key)
	if err != nil || entry == nil {
		return nil, err
	}
	return toEntrySummary(entry), nil
}

// InspectRaw returns the full entry for sub-agent review.
func (s *Scratchpad) InspectRaw(ctx context.Context, key string) (*Entry, error) {
	return s.getEntry(ctx, key)
}

// ListSummaries returns summaries ordered by creation time (newest first).
func (s *Scratchpad) ListSummaries(ctx context.Context, limit int) ([]EntrySummary, error) {
	if s == nil {
		return nil, fmt.Errorf("scratchpad is nil")
	}
	if err := s.loadFromStore(ctx, limit); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.purgeExpiredLocked(now)
	entries := make([]*Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	out := make([]EntrySummary, 0, len(entries))
	s.mu.Lock()
	for _, entry := range entries {
		entry.LastAccess = now
		out = append(out, *toEntrySummary(entry))
	}
	s.mu.Unlock()
	return out, nil
}

func (s *Scratchpad) summarize(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if s.summarizer != nil {
		if summary := strings.TrimSpace(s.summarizer(raw)); summary != "" {
			return summary
		}
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) <= defaultScratchpadSummaryLimit {
		return trimmed
	}
	return trimmed[:defaultScratchpadSummaryLimit] + "..."
}

func (s *Scratchpad) getEntry(ctx context.Context, key string) (*Entry, error) {
	if s == nil {
		return nil, fmt.Errorf("scratchpad is nil")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("scratchpad key required")
	}

	now := time.Now().UTC()
	s.mu.Lock()
	entry := s.entries[key]
	if entry != nil {
		if s.isExpired(entry, now) {
			s.removeEntryLocked(key)
			s.mu.Unlock()
			return nil, nil
		}
		entry.LastAccess = now
		s.mu.Unlock()
		return entry, nil
	}
	s.mu.Unlock()
	if s.store == nil {
		return nil, nil
	}

	stored, err := s.store.GetScratchpadEntry(ctx, key)
	if err != nil || stored == nil {
		return nil, err
	}
	entry = entryFromStorage(stored)
	entry.LastAccess = now
	if s.isExpired(entry, now) {
		return nil, nil
	}
	s.mu.Lock()
	if existing := s.entries[key]; existing != nil {
		if s.isExpired(existing, now) {
			s.removeEntryLocked(key)
		} else {
			existing.LastAccess = now
			entry = existing
		}
		s.mu.Unlock()
		return entry, nil
	}
	s.entries[key] = entry
	s.rawBytes += entrySize(entry)
	s.enforceLimitsLocked(now)
	s.mu.Unlock()
	return entry, nil
}

func (s *Scratchpad) loadFromStore(ctx context.Context, limit int) error {
	if s == nil || s.store == nil {
		return nil
	}
	if limit < 0 {
		limit = 0
	}
	entries, err := s.store.ListScratchpadEntries(ctx, limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	now := time.Now().UTC()
	s.mu.Lock()
	for _, stored := range entries {
		if _, ok := s.entries[stored.Key]; ok {
			continue
		}
		entry := entryFromStorage(&stored)
		if s.isExpired(entry, now) {
			continue
		}
		s.entries[stored.Key] = entry
		s.rawBytes += entrySize(entry)
	}
	s.enforceLimitsLocked(now)
	s.mu.Unlock()
	return nil
}

func entryFromStorage(stored *storage.ScratchpadEntry) *Entry {
	if stored == nil {
		return nil
	}
	metadata := map[string]any{}
	if strings.TrimSpace(stored.Metadata) != "" {
		_ = json.Unmarshal([]byte(stored.Metadata), &metadata)
		if len(metadata) == 0 {
			metadata = nil
		}
	}
	return &Entry{
		Key:        stored.Key,
		Type:       EntryType(stored.EntryType),
		Raw:        stored.Raw,
		Summary:    stored.Summary,
		Metadata:   metadata,
		CreatedBy:  stored.CreatedBy,
		CreatedAt:  stored.CreatedAt,
		LastAccess: stored.CreatedAt,
	}
}

func normalizeScratchpadConfig(cfg ScratchpadConfig) ScratchpadConfig {
	if cfg == (ScratchpadConfig{}) {
		return DefaultConfig().Scratchpad
	}
	defaults := DefaultConfig().Scratchpad
	if cfg.MaxEntriesMemory == 0 {
		cfg.MaxEntriesMemory = defaults.MaxEntriesMemory
	}
	if cfg.MaxRawBytesMemory == 0 {
		cfg.MaxRawBytesMemory = defaults.MaxRawBytesMemory
	}
	if strings.TrimSpace(cfg.EvictionPolicy) == "" {
		cfg.EvictionPolicy = defaults.EvictionPolicy
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = defaults.DefaultTTL
	}
	return cfg
}

func (s *Scratchpad) shouldPersist(entry *Entry, now time.Time) bool {
	if s == nil || entry == nil {
		return false
	}
	if s.isExpired(entry, now) {
		return false
	}
	switch entry.Type {
	case EntryTypeArtifact:
		return s.config.PersistArtifacts
	case EntryTypeDecision, EntryTypeStrategy:
		return s.config.PersistDecisions
	default:
		return false
	}
}

func (s *Scratchpad) isExpired(entry *Entry, now time.Time) bool {
	if s == nil || entry == nil {
		return false
	}
	ttl := s.config.DefaultTTL
	if ttl <= 0 {
		return false
	}
	if entry.CreatedAt.IsZero() {
		return false
	}
	return now.After(entry.CreatedAt.Add(ttl))
}

func (s *Scratchpad) purgeExpiredLocked(now time.Time) {
	if s == nil {
		return
	}
	if s.config.DefaultTTL <= 0 {
		return
	}
	for key, entry := range s.entries {
		if s.isExpired(entry, now) {
			s.removeEntryLocked(key)
		}
	}
}

func (s *Scratchpad) enforceLimitsLocked(now time.Time) {
	if s == nil {
		return
	}
	maxEntries := s.config.MaxEntriesMemory
	maxBytes := s.config.MaxRawBytesMemory
	if maxEntries <= 0 && maxBytes <= 0 {
		return
	}
	if len(s.entries) == 0 {
		return
	}

	if (maxEntries <= 0 || len(s.entries) <= maxEntries) && (maxBytes <= 0 || s.rawBytes <= maxBytes) {
		return
	}

	candidates := make([]*Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		candidates = append(candidates, entry)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return s.evictionTime(candidates[i], now).Before(s.evictionTime(candidates[j], now))
	})
	for _, entry := range candidates {
		if (maxEntries <= 0 || len(s.entries) <= maxEntries) && (maxBytes <= 0 || s.rawBytes <= maxBytes) {
			break
		}
		s.removeEntryLocked(entry.Key)
	}
}

func (s *Scratchpad) evictionTime(entry *Entry, now time.Time) time.Time {
	if entry == nil {
		return now
	}
	policy := strings.ToLower(strings.TrimSpace(s.config.EvictionPolicy))
	if policy == "lru" {
		if !entry.LastAccess.IsZero() {
			return entry.LastAccess
		}
	}
	if !entry.CreatedAt.IsZero() {
		return entry.CreatedAt
	}
	return now
}

func (s *Scratchpad) removeEntryLocked(key string) {
	entry := s.entries[key]
	if entry == nil {
		return
	}
	delete(s.entries, key)
	s.rawBytes -= entrySize(entry)
	if s.rawBytes < 0 {
		s.rawBytes = 0
	}
}

func entrySize(entry *Entry) int64 {
	if entry == nil {
		return 0
	}
	return int64(len(entry.Raw))
}

func toEntrySummary(entry *Entry) *EntrySummary {
	if entry == nil {
		return nil
	}
	return &EntrySummary{
		Key:       entry.Key,
		Type:      entry.Type,
		Summary:   entry.Summary,
		Metadata:  cloneMetadata(entry.Metadata),
		CreatedBy: entry.CreatedBy,
		CreatedAt: entry.CreatedAt,
	}
}

func (s *Scratchpad) emitOnWrite(ctx context.Context, summaries []EntrySummary) {
	if s == nil || len(summaries) == 0 {
		return
	}
	s.mu.RLock()
	handler := s.onWrite
	s.mu.RUnlock()
	if handler == nil {
		return
	}
	handler(ctx, summaries)
}

func cloneMetadata(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
