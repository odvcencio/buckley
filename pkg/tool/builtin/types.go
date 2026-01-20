package builtin

// ParameterSchema defines the parameters a tool accepts
type ParameterSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]PropertySchema `json:"properties"`
	Required   []string                  `json:"required"`
}

// PropertySchema defines a single parameter
type PropertySchema struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Default     any             `json:"default,omitempty"`
	Items       *PropertySchema `json:"items,omitempty"` // For array types
	Enum        []string        `json:"enum,omitempty"`  // For string types with fixed options
}

// Result represents the result of a tool execution
type Result struct {
	Success       bool           `json:"success"`
	Data          map[string]any `json:"data,omitempty"`
	Error         string         `json:"error,omitempty"`
	DisplayData   map[string]any `json:"display_data,omitempty"`   // Abridged data for conversation display
	ShouldAbridge bool           `json:"should_abridge,omitempty"` // Whether to show abridged version in chat

	// Diff preview support for file modifications
	NeedsApproval bool       `json:"needs_approval,omitempty"` // Whether this change requires user approval
	DiffPreview   *DiffInfo  `json:"diff_preview,omitempty"`   // Preview of changes for approval
	ApprovalFunc  func(bool) `json:"-"`                        // Callback for approval decision
}

// DiffInfo contains diff information for file changes
type DiffInfo struct {
	FilePath     string `json:"file_path"`
	IsNew        bool   `json:"is_new"`
	IsDelete     bool   `json:"is_delete"`
	LinesAdded   int    `json:"lines_added"`
	LinesRemoved int    `json:"lines_removed"`
	OldContent   string `json:"old_content,omitempty"` // Original content
	NewContent   string `json:"new_content,omitempty"` // New content
	UnifiedDiff  string `json:"unified_diff"`          // Unified diff format
	Preview      string `json:"preview"`               // First few lines of diff for display
}
