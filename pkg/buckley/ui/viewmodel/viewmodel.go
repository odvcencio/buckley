package viewmodel

import "time"

// State represents a render-ready snapshot for all sessions.
type State struct {
	Sessions    []SessionState `json:"sessions"`
	GeneratedAt time.Time      `json:"generatedAt"`
}

// Patch represents a targeted update for a single session.
type Patch struct {
	Session *SessionState `json:"session,omitempty"`
}

// SessionState captures everything a renderer needs for a single session.
type SessionState struct {
	ID         string         `json:"id"`
	Title      string         `json:"title,omitempty"`
	Status     SessionStatus  `json:"status"`
	Workflow   WorkflowStatus `json:"workflow"`
	Transcript TranscriptPage `json:"transcript"`
	Todos      []Todo         `json:"todos,omitempty"`
	Plan       *PlanSnapshot  `json:"plan,omitempty"`
	Metrics    Metrics        `json:"metrics"`
	Activity   []string       `json:"activity,omitempty"`

	// Runtime state (live telemetry-derived)
	IsStreaming     bool        `json:"isStreaming,omitempty"`
	ActiveToolCalls []ToolCall  `json:"activeToolCalls,omitempty"`
	RecentFiles     []FileTouch `json:"recentFiles,omitempty"`
	ActiveTouches   []CodeTouch `json:"activeTouches,omitempty"`
}

// SessionStatus summarizes high-level state and whether we are waiting on the user.
type SessionStatus struct {
	State        string    `json:"state"`
	Paused       bool      `json:"paused,omitempty"`
	AwaitingUser bool      `json:"awaitingUser,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Question     string    `json:"question,omitempty"`
	LastUpdated  time.Time `json:"lastUpdated,omitempty"`
}

// WorkflowStatus mirrors orchestrator metadata for renderers.
type WorkflowStatus struct {
	Phase         string     `json:"phase,omitempty"`
	ActiveAgent   string     `json:"activeAgent,omitempty"`
	Paused        bool       `json:"paused,omitempty"`
	AwaitingUser  bool       `json:"awaitingUser,omitempty"`
	PauseReason   string     `json:"pauseReason,omitempty"`
	PauseQuestion string     `json:"pauseQuestion,omitempty"`
	PauseAt       *time.Time `json:"pauseAt,omitempty"`
}

// TranscriptPage provides a paginated view of messages.
type TranscriptPage struct {
	Messages   []Message `json:"messages"`
	HasMore    bool      `json:"hasMore"`
	NextOffset int       `json:"nextOffset"`
}

// Message is a render-friendly message.
type Message struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	ContentType string    `json:"contentType,omitempty"`
	Reasoning   string    `json:"reasoning,omitempty"`
	Tokens      int       `json:"tokens,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	IsSummary   bool      `json:"isSummary,omitempty"`
}

// Todo captures task progress.
type Todo struct {
	ID          int64     `json:"id"`
	Content     string    `json:"content"`
	ActiveForm  string    `json:"activeForm,omitempty"`
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// PlanSnapshot is a lightweight view of a plan.
type PlanSnapshot struct {
	ID          string      `json:"id"`
	FeatureName string      `json:"featureName"`
	Description string      `json:"description,omitempty"`
	Tasks       []PlanTask  `json:"tasks,omitempty"`
	Progress    TaskSummary `json:"progress"`
}

// PlanTask captures task state.
type PlanTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Type   string `json:"type,omitempty"`
}

// TaskSummary aggregates plan progress.
type TaskSummary struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
	Total     int `json:"total"`
}

// Metrics contains cost and token stats.
type Metrics struct {
	TotalTokens int     `json:"totalTokens"`
	TotalCost   float64 `json:"totalCost"`
}

// ToolCall represents a currently running or recently completed tool.
type ToolCall struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running, completed, failed
	Command   string    `json:"command,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// FileTouch represents a recently accessed file.
type FileTouch struct {
	Path      string    `json:"path"`
	Operation string    `json:"operation"` // read, write, create
	TouchedAt time.Time `json:"touchedAt"`
}

// CodeTouch represents an active touch on a file range.
type CodeTouch struct {
	ID        string      `json:"id"`
	ToolName  string      `json:"toolName,omitempty"`
	Operation string      `json:"operation"`
	FilePath  string      `json:"filePath"`
	Ranges    []LineRange `json:"ranges,omitempty"`
	StartedAt time.Time   `json:"startedAt"`
	ExpiresAt time.Time   `json:"expiresAt,omitempty"`
}

// LineRange defines a 1-based inclusive line span.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
