// Package checkpoint provides session state saving and restoration.
// It allows users to save their progress and resume later.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	envBuckleyCheckpointsDir = "BUCKLEY_CHECKPOINTS_DIR"
	envBuckleyDataDir        = "BUCKLEY_DATA_DIR"
)

// Checkpoint represents a saved session state
type Checkpoint struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	SessionID   string            `json:"session_id"`
	Branch      string            `json:"branch,omitempty"`
	Messages    []Message         `json:"messages"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	TokenCount  int               `json:"token_count"`
	Summary     string            `json:"summary,omitempty"`
}

// Message represents a conversation message
type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

// Store manages checkpoint persistence
type Store struct {
	baseDir string
}

// NewStore creates a new checkpoint store
func NewStore(baseDir string) *Store {
	if strings.TrimSpace(baseDir) == "" {
		if dir := strings.TrimSpace(os.Getenv(envBuckleyCheckpointsDir)); dir != "" {
			baseDir = expandHomePath(dir)
		} else if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
			baseDir = filepath.Join(expandHomePath(dir), "checkpoints")
		} else if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			baseDir = filepath.Join(home, ".buckley", "checkpoints")
		}
	}
	return &Store{baseDir: baseDir}
}

func expandHomePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

// Save saves a checkpoint to disk
func (s *Store) Save(cp *Checkpoint) error {
	if cp == nil {
		return fmt.Errorf("checkpoint is nil")
	}

	if cp.ID == "" {
		cp.ID = generateID()
	}

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	path := filepath.Join(s.baseDir, cp.ID+".json")

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}

	return nil
}

// Load loads a checkpoint by ID
func (s *Store) Load(id string) (*Checkpoint, error) {
	path := filepath.Join(s.baseDir, id+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint: %w", err)
	}

	return &cp, nil
}

// List returns all checkpoints, sorted by creation time (newest first)
func (s *Store) List() ([]*Checkpoint, error) {
	entries, err := os.ReadDir(s.baseDir)
	if os.IsNotExist(err) {
		return []*Checkpoint{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var checkpoints []*Checkpoint
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		cp, err := s.Load(id)
		if err != nil {
			continue
		}
		checkpoints = append(checkpoints, cp)
	}

	// Sort by creation time, newest first
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.After(checkpoints[j].CreatedAt)
	})

	return checkpoints, nil
}

// Delete removes a checkpoint
func (s *Store) Delete(id string) error {
	path := filepath.Join(s.baseDir, id+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}
	return nil
}

// ListBySession returns checkpoints for a specific session
func (s *Store) ListBySession(sessionID string) ([]*Checkpoint, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	var filtered []*Checkpoint
	for _, cp := range all {
		if cp.SessionID == sessionID {
			filtered = append(filtered, cp)
		}
	}

	return filtered, nil
}

// ListByBranch returns checkpoints for a specific git branch
func (s *Store) ListByBranch(branch string) ([]*Checkpoint, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	var filtered []*Checkpoint
	for _, cp := range all {
		if cp.Branch == branch {
			filtered = append(filtered, cp)
		}
	}

	return filtered, nil
}

// GetLatest returns the most recent checkpoint
func (s *Store) GetLatest() (*Checkpoint, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no checkpoints found")
	}

	return all[0], nil
}

// GetLatestForSession returns the most recent checkpoint for a session
func (s *Store) GetLatestForSession(sessionID string) (*Checkpoint, error) {
	bySession, err := s.ListBySession(sessionID)
	if err != nil {
		return nil, err
	}

	if len(bySession) == 0 {
		return nil, fmt.Errorf("no checkpoints found for session %s", sessionID)
	}

	return bySession[0], nil
}

// Prune removes old checkpoints, keeping only the N most recent
func (s *Store) Prune(keepCount int) (int, error) {
	all, err := s.List()
	if err != nil {
		return 0, err
	}

	if len(all) <= keepCount {
		return 0, nil
	}

	deleted := 0
	for i := keepCount; i < len(all); i++ {
		if err := s.Delete(all[i].ID); err != nil {
			continue
		}
		deleted++
	}

	return deleted, nil
}

// AutoCheckpoint creates an automatic checkpoint if conditions are met
func (s *Store) AutoCheckpoint(sessionID, branch string, messages []Message, tokenCount int) (*Checkpoint, bool, error) {
	// Only auto-save if we have significant content
	if len(messages) < 5 || tokenCount < 1000 {
		return nil, false, nil
	}

	// Check if we need a new checkpoint
	latest, _ := s.GetLatestForSession(sessionID)
	if latest != nil {
		// Don't create if last checkpoint was less than 10 minutes ago
		if time.Since(latest.CreatedAt) < 10*time.Minute {
			return nil, false, nil
		}

		// Don't create if we haven't added many new messages
		if len(messages)-len(latest.Messages) < 5 {
			return nil, false, nil
		}
	}

	cp := &Checkpoint{
		Name:       fmt.Sprintf("Auto-save %s", time.Now().Format("2006-01-02 15:04")),
		CreatedAt:  time.Now(),
		SessionID:  sessionID,
		Branch:     branch,
		Messages:   messages,
		TokenCount: tokenCount,
		Metadata: map[string]string{
			"auto": "true",
		},
	}

	if err := s.Save(cp); err != nil {
		return nil, false, err
	}

	return cp, true, nil
}

func generateID() string {
	return fmt.Sprintf("cp_%d", time.Now().UnixNano())
}

// Manager provides high-level checkpoint operations
type Manager struct {
	store     *Store
	sessionID string
	branch    string
}

// NewManager creates a new checkpoint manager
func NewManager(store *Store, sessionID, branch string) *Manager {
	return &Manager{
		store:     store,
		sessionID: sessionID,
		branch:    branch,
	}
}

// CreateCheckpoint creates a new checkpoint with the given name
func (m *Manager) CreateCheckpoint(name, description string, messages []Message, tokenCount int) (*Checkpoint, error) {
	cp := &Checkpoint{
		Name:        name,
		Description: description,
		CreatedAt:   time.Now(),
		SessionID:   m.sessionID,
		Branch:      m.branch,
		Messages:    messages,
		TokenCount:  tokenCount,
	}

	if err := m.store.Save(cp); err != nil {
		return nil, err
	}

	return cp, nil
}

// RestoreCheckpoint loads a checkpoint and returns its messages
func (m *Manager) RestoreCheckpoint(id string) (*Checkpoint, error) {
	return m.store.Load(id)
}

// ListCheckpoints returns all checkpoints for current session
func (m *Manager) ListCheckpoints() ([]*Checkpoint, error) {
	return m.store.ListBySession(m.sessionID)
}

// DeleteCheckpoint removes a checkpoint
func (m *Manager) DeleteCheckpoint(id string) error {
	return m.store.Delete(id)
}

// Format returns a formatted string representation of a checkpoint
func (cp *Checkpoint) Format() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("📍 %s\n", cp.Name))
	b.WriteString(fmt.Sprintf("   ID: %s\n", cp.ID))
	b.WriteString(fmt.Sprintf("   Created: %s\n", cp.CreatedAt.Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("   Messages: %d\n", len(cp.Messages)))
	b.WriteString(fmt.Sprintf("   Tokens: %d\n", cp.TokenCount))

	if cp.Branch != "" {
		b.WriteString(fmt.Sprintf("   Branch: %s\n", cp.Branch))
	}

	if cp.Description != "" {
		b.WriteString(fmt.Sprintf("   Description: %s\n", cp.Description))
	}

	if cp.Summary != "" {
		b.WriteString(fmt.Sprintf("   Summary: %s\n", cp.Summary))
	}

	return b.String()
}

// FormatCompact returns a compact one-line representation
func (cp *Checkpoint) FormatCompact() string {
	age := time.Since(cp.CreatedAt)
	ageStr := formatAge(age)

	return fmt.Sprintf("[%s] %s (%d msgs, %s ago)",
		cp.ID[:12], cp.Name, len(cp.Messages), ageStr)
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
