package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/storage"
)

// SessionExportSchemaVersion identifies the exportable session transcript
// document's JSON shape. There is no session-to-run mapping in the run
// ledger yet (pkg/runledger has no production caller as of this command's
// introduction), so this exports the conversation transcript straight from
// pkg/storage instead of runledger.ExportRun. When that mapping lands, a
// ledger-backed export should take priority for sessions that have a run.
const SessionExportSchemaVersion = "buckley.session.export.v1"

// SessionExportDocument is the redacted, exportable document for one
// session's stored transcript.
type SessionExportDocument struct {
	SchemaVersion string                 `json:"schema_version"`
	Source        string                 `json:"source"` // "storage_transcript"
	Redaction     string                 `json:"redaction"`
	Session       SessionExportSummary   `json:"session"`
	Messages      []SessionExportMessage `json:"messages"`
}

// SessionExportSummary is the redacted session metadata included in an
// export.
type SessionExportSummary struct {
	ID           string    `json:"id"`
	ProjectPath  string    `json:"project_path,omitempty"`
	GitRepo      string    `json:"git_repo,omitempty"`
	GitBranch    string    `json:"git_branch,omitempty"`
	Model        string    `json:"model,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
	MessageCount int       `json:"message_count"`
	TotalTokens  int       `json:"total_tokens"`
	TotalCost    float64   `json:"total_cost"`
	Status       string    `json:"status"`
}

// SessionExportMessage is one redacted message row included in an export.
type SessionExportMessage struct {
	Role       string    `json:"role"`
	Content    string    `json:"content,omitempty"`
	Reasoning  string    `json:"reasoning,omitempty"`
	ToolCalls  string    `json:"tool_calls,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
	IsSummary  bool      `json:"is_summary,omitempty"`
}

func runSessionCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley session <export> [flags]")
	}
	switch strings.TrimSpace(args[0]) {
	case "export":
		return runSessionExportCommand(args[1:])
	default:
		return fmt.Errorf("usage: buckley session <export> [flags]")
	}
}

func runSessionExportCommand(args []string) error {
	fs := flag.NewFlagSet("session export", flag.ContinueOnError)
	format := fs.String("format", "json", "Export format: json or markdown")
	out := fs.String("out", "", "Output file path (defaults to stdout)")
	dbPathFlag := fs.String("db", "", "Source DB path (defaults to BUCKLEY_DB_PATH/BUCKLEY_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return fmt.Errorf("usage: buckley session export <session-id> [--format json|markdown] [--out path]")
	}
	sessionID := strings.TrimSpace(positional[0])

	formatValue := strings.ToLower(strings.TrimSpace(*format))
	if formatValue != "json" && formatValue != "markdown" {
		return fmt.Errorf("unsupported --format %q: use json or markdown", *format)
	}

	dbPath := strings.TrimSpace(*dbPathFlag)
	if dbPath == "" {
		resolved, err := resolveDBPath()
		if err != nil {
			return err
		}
		dbPath = resolved
	}

	store, err := storage.New(dbPath)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	session, err := store.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	messages, err := store.GetAllMessages(sessionID)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}

	doc := buildSessionExportDocument(session, messages)

	var content string
	switch formatValue {
	case "json":
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("encode export: %w", err)
		}
		content = string(data) + "\n"
	case "markdown":
		content = renderSessionExportMarkdown(doc)
	}

	outPath := strings.TrimSpace(*out)
	if outPath == "" {
		fmt.Print(content)
		return nil
	}
	outPath, err = expandHomePath(outPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	fmt.Printf("Exported session %s to %s\n", sessionID, outPath)
	return nil
}

// buildSessionExportDocument redacts session and message content using the
// same evidence.Redact helper runledger.ExportRun uses (pkg/runledger's
// section 13.3 rule: exported run/session documents are one of the
// surfaces secret redaction MUST apply to), so no credential-shaped
// content survives export.
func buildSessionExportDocument(session *storage.Session, messages []storage.Message) SessionExportDocument {
	doc := SessionExportDocument{
		SchemaVersion: SessionExportSchemaVersion,
		Source:        "storage_transcript",
		Redaction:     evidence.RedactionVersion,
		Session: SessionExportSummary{
			ID:           session.ID,
			ProjectPath:  session.ProjectPath,
			GitRepo:      session.GitRepo,
			GitBranch:    session.GitBranch,
			Model:        redactString(session.Model),
			CreatedAt:    session.CreatedAt,
			LastActive:   session.LastActive,
			MessageCount: session.MessageCount,
			TotalTokens:  session.TotalTokens,
			TotalCost:    session.TotalCost,
			Status:       session.Status,
		},
	}

	for _, msg := range messages {
		doc.Messages = append(doc.Messages, SessionExportMessage{
			Role:       msg.Role,
			Content:    redactString(msg.Content),
			Reasoning:  redactString(msg.Reasoning),
			ToolCalls:  redactString(msg.ToolCalls),
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
			Timestamp:  msg.Timestamp,
			IsSummary:  msg.IsSummary,
		})
	}
	return doc
}

func redactString(s string) string {
	if s == "" {
		return ""
	}
	return string(evidence.Redact([]byte(s)))
}

func renderSessionExportMarkdown(doc SessionExportDocument) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session export: %s\n\n", doc.Session.ID)
	fmt.Fprintf(&b, "- Schema: %s\n", doc.SchemaVersion)
	fmt.Fprintf(&b, "- Source: %s\n", doc.Source)
	fmt.Fprintf(&b, "- Redaction: %s\n", doc.Redaction)
	fmt.Fprintf(&b, "- Status: %s\n", doc.Session.Status)
	if doc.Session.Model != "" {
		fmt.Fprintf(&b, "- Model: %s\n", doc.Session.Model)
	}
	if doc.Session.ProjectPath != "" {
		fmt.Fprintf(&b, "- Project: %s\n", doc.Session.ProjectPath)
	}
	if doc.Session.GitRepo != "" {
		fmt.Fprintf(&b, "- Git: %s (%s)\n", doc.Session.GitRepo, doc.Session.GitBranch)
	}
	fmt.Fprintf(&b, "- Created: %s\n", doc.Session.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Last active: %s\n", doc.Session.LastActive.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Messages: %d\n\n", len(doc.Messages))

	for _, msg := range doc.Messages {
		role := "Message"
		if msg.Role != "" {
			role = strings.ToUpper(msg.Role[:1]) + msg.Role[1:]
		}
		if msg.Role == "" {
			role = "Message"
		}
		header := "## " + role
		if msg.Name != "" {
			header += " (" + msg.Name + ")"
		}
		fmt.Fprintf(&b, "%s — %s\n\n", header, msg.Timestamp.Format(time.RFC3339))
		if msg.IsSummary {
			b.WriteString("_summary_\n\n")
		}
		if msg.Content != "" {
			b.WriteString(msg.Content)
			b.WriteString("\n\n")
		}
		if msg.Reasoning != "" {
			b.WriteString("<details><summary>Reasoning</summary>\n\n")
			b.WriteString(msg.Reasoning)
			b.WriteString("\n\n</details>\n\n")
		}
		if msg.ToolCalls != "" {
			b.WriteString("```json\n")
			b.WriteString(msg.ToolCalls)
			b.WriteString("\n```\n\n")
		}
	}
	return b.String()
}
