package rlm

import (
	"context"
	"time"
)

// EntryType identifies a scratchpad entry kind.
type EntryType string

const (
	EntryTypeFile     EntryType = "file"
	EntryTypeCommand  EntryType = "command"
	EntryTypeAnalysis EntryType = "analysis"
	EntryTypeDecision EntryType = "decision"
	EntryTypeArtifact EntryType = "artifact"
	EntryTypeStrategy EntryType = "strategy" // Strategic decisions for context
)

// Entry stores raw and summarized scratchpad content.
type Entry struct {
	Key        string
	Type       EntryType
	Raw        []byte
	Summary    string
	Metadata   map[string]any
	CreatedBy  string
	CreatedAt  time.Time
	LastAccess time.Time
}

// EntrySummary is the coordinator-safe view of an entry.
type EntrySummary struct {
	Key       string
	Type      EntryType
	Summary   string
	Metadata  map[string]any
	CreatedBy string
	CreatedAt time.Time
}

// WriteRequest describes data to append to the scratchpad.
type WriteRequest struct {
	Key       string
	Type      EntryType
	Raw       []byte
	Summary   string
	Metadata  map[string]any
	CreatedBy string
	CreatedAt time.Time
}

// ScratchpadWriter persists scratchpad entries.
type ScratchpadWriter interface {
	Write(ctx context.Context, req WriteRequest) (string, error)
}
