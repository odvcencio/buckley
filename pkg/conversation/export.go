package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// ExportFormat specifies the conversation export format.
type ExportFormat string

const (
	ExportMarkdown ExportFormat = "markdown"
	ExportJSON     ExportFormat = "json"
	ExportHTML     ExportFormat = "html"
)

// ExportOptions configures export behavior.
type ExportOptions struct {
	Format           ExportFormat
	IncludeSystem    bool
	IncludeToolCalls bool
	IncludeMetadata  bool
}

// Exporter exports conversation history from storage.
type Exporter struct {
	store *storage.Store
}

// NewExporter creates a new exporter.
func NewExporter(store *storage.Store) *Exporter {
	return &Exporter{store: store}
}

// Export returns the serialized conversation for a session.
func (e *Exporter) Export(sessionID string, opts ExportOptions) ([]byte, error) {
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("store required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	if opts.Format == "" {
		opts.Format = ExportMarkdown
	}

	messages, err := e.store.GetAllMessages(sessionID)
	if err != nil {
		return nil, err
	}
	session, _ := e.store.GetSession(sessionID)

	filtered := filterExportMessages(messages, opts)
	switch opts.Format {
	case ExportJSON:
		return exportJSON(sessionID, session, filtered, opts)
	case ExportHTML:
		return exportHTML(sessionID, session, filtered, opts)
	default:
		return exportMarkdown(sessionID, session, filtered, opts)
	}
}

type exportMessage struct {
	Role        string         `json:"role"`
	Content     string         `json:"content"`
	ContentJSON string         `json:"content_json,omitempty"`
	ContentType string         `json:"content_type,omitempty"`
	Reasoning   string         `json:"reasoning,omitempty"`
	Timestamp   time.Time      `json:"timestamp,omitempty"`
	Tokens      int            `json:"tokens,omitempty"`
	IsSummary   bool           `json:"is_summary,omitempty"`
	IsTruncated bool           `json:"is_truncated,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type exportPayload struct {
	SessionID  string          `json:"session_id"`
	ExportedAt time.Time       `json:"exported_at"`
	Session    *storage.Session `json:"session,omitempty"`
	Messages   []exportMessage `json:"messages"`
}

func filterExportMessages(messages []storage.Message, opts ExportOptions) []storage.Message {
	filtered := make([]storage.Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			if !opts.IncludeSystem {
				continue
			}
		case "tool":
			if !opts.IncludeToolCalls {
				continue
			}
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

func exportJSON(sessionID string, session *storage.Session, messages []storage.Message, opts ExportOptions) ([]byte, error) {
	includeContentJSON := opts.IncludeMetadata || opts.IncludeToolCalls
	payload := exportPayload{
		SessionID:  sessionID,
		ExportedAt: time.Now(),
		Messages:   make([]exportMessage, 0, len(messages)),
	}
	if opts.IncludeMetadata {
		payload.Session = session
	}

	for _, msg := range messages {
		item := exportMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		if includeContentJSON {
			item.ContentJSON = msg.ContentJSON
			item.ContentType = msg.ContentType
		}
		if opts.IncludeMetadata {
			item.Reasoning = msg.Reasoning
			item.Timestamp = msg.Timestamp
			item.Tokens = msg.Tokens
			item.IsSummary = msg.IsSummary
			item.IsTruncated = msg.IsTruncated
			item.Metadata = map[string]any{
				"summary": msg.IsSummary,
				"truncated": msg.IsTruncated,
			}
		}
		payload.Messages = append(payload.Messages, item)
	}

	return json.MarshalIndent(payload, "", "  ")
}

func exportMarkdown(sessionID string, session *storage.Session, messages []storage.Message, opts ExportOptions) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Buckley Conversation Export\n\n")
	b.WriteString("Session: " + sessionID + "\n")
	b.WriteString("Exported: " + time.Now().Format(time.RFC3339) + "\n")
	if opts.IncludeMetadata && session != nil {
		b.WriteString("Project: " + strings.TrimSpace(session.ProjectPath) + "\n")
	}
	b.WriteString("\n---\n\n")

	for _, msg := range messages {
		b.WriteString("### " + strings.ToUpper(msg.Role))
		if opts.IncludeMetadata {
			b.WriteString(" (" + msg.Timestamp.Format(time.RFC3339) + ")")
		}
		b.WriteString("\n\n")
		content := msg.Content
		if strings.TrimSpace(content) == "" && strings.TrimSpace(msg.ContentJSON) != "" {
			content = msg.ContentJSON
		}
		b.WriteString(content)
		b.WriteString("\n\n")
		if opts.IncludeMetadata {
			b.WriteString(fmt.Sprintf("_tokens: %d, summary: %v, truncated: %v_\n\n", msg.Tokens, msg.IsSummary, msg.IsTruncated))
		}
	}

	return []byte(b.String()), nil
}

func exportHTML(sessionID string, session *storage.Session, messages []storage.Message, opts ExportOptions) ([]byte, error) {
	tpl := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Buckley Conversation Export</title>
<style>
body { font-family: "Georgia", serif; background: #f7f3ea; color: #1f1a14; margin: 32px; }
header { margin-bottom: 24px; }
section { margin-bottom: 18px; padding: 12px 16px; background: #fff8e8; border-radius: 8px; }
.role { font-weight: bold; text-transform: uppercase; font-size: 12px; letter-spacing: 0.08em; color: #6b4f2b; }
.meta { font-size: 12px; color: #7d6a50; margin-top: 6px; }
pre { white-space: pre-wrap; font-family: "Menlo", monospace; }
</style>
</head>
<body>
<header>
<h1>Buckley Conversation Export</h1>
<p>Session: {{ .SessionID }}</p>
<p>Exported: {{ .ExportedAt }}</p>
{{ if .Project }}<p>Project: {{ .Project }}</p>{{ end }}
</header>
{{ range .Messages }}
<section>
<div class="role">{{ .Role }}</div>
<pre>{{ .Content }}</pre>
{{ if .Meta }}<div class="meta">{{ .Meta }}</div>{{ end }}
</section>
{{ end }}
</body>
</html>`

	type htmlMessage struct {
		Role    string
		Content string
		Meta    string
	}

	data := struct {
		SessionID  string
		ExportedAt string
		Project    string
		Messages   []htmlMessage
	}{
		SessionID:  sessionID,
		ExportedAt: time.Now().Format(time.RFC3339),
	}
	if opts.IncludeMetadata && session != nil {
		data.Project = strings.TrimSpace(session.ProjectPath)
	}
	for _, msg := range messages {
		content := msg.Content
		if strings.TrimSpace(content) == "" && strings.TrimSpace(msg.ContentJSON) != "" {
			content = msg.ContentJSON
		}
		meta := ""
		if opts.IncludeMetadata {
			meta = fmt.Sprintf("%s | tokens=%d | summary=%v | truncated=%v", msg.Timestamp.Format(time.RFC3339), msg.Tokens, msg.IsSummary, msg.IsTruncated)
		}
		data.Messages = append(data.Messages, htmlMessage{Role: msg.Role, Content: content, Meta: meta})
	}

	parsed, err := template.New("export").Parse(tpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
