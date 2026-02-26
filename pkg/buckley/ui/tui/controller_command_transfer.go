package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
)

func (c *Controller) handleExportCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Export unavailable: storage not configured.", "system")
		return
	}
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sessionID := c.sessions[c.currentSession].ID
	c.mu.Unlock()

	opts := conversation.ExportOptions{Format: conversation.ExportMarkdown}
	outputPath := ""

	for i := 0; i < len(args); i++ {
		arg := strings.ToLower(strings.TrimSpace(args[i]))
		switch arg {
		case "--format", "-f":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /export [--format json|markdown|html] [--output path] [--include-system] [--include-tools] [--include-metadata]", "system")
				return
			}
			format, ok := parseExportFormat(args[i+1])
			if !ok {
				c.app.AddMessage("Unsupported export format: "+args[i+1], "system")
				return
			}
			opts.Format = format
			i++
		case "--output", "-o":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /export [--format json|markdown|html] [--output path] [--include-system] [--include-tools] [--include-metadata]", "system")
				return
			}
			outputPath = strings.TrimSpace(args[i+1])
			i++
		case "--include-system":
			opts.IncludeSystem = true
		case "--include-tools":
			opts.IncludeToolCalls = true
		case "--include-metadata":
			opts.IncludeMetadata = true
		default:
			if outputPath == "" {
				outputPath = strings.TrimSpace(args[i])
			}
		}
	}

	if outputPath == "" {
		ext := exportExtension(opts.Format)
		timestamp := time.Now().Format("20060102-150405")
		outputPath = fmt.Sprintf("buckley-export-%s-%s%s", sessionID, timestamp, ext)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(c.workDir, outputPath)
	}

	exporter := conversation.NewExporter(c.store)
	data, err := exporter.Export(sessionID, opts)
	if err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}

	c.app.AddMessage(fmt.Sprintf("Exported session %s to %s", sessionID, outputPath), "system")
}

func (c *Controller) handleImportCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Import unavailable: storage not configured.", "system")
		return
	}

	var (
		format    conversation.ExportFormat
		inputPath string
	)
	for i := 0; i < len(args); i++ {
		arg := strings.ToLower(strings.TrimSpace(args[i]))
		switch arg {
		case "--format", "-f":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /import [--format json|markdown] <path>", "system")
				return
			}
			parsed, ok := parseExportFormat(args[i+1])
			if !ok {
				c.app.AddMessage("Unsupported import format: "+args[i+1], "system")
				return
			}
			format = parsed
			i++
		default:
			if inputPath == "" {
				inputPath = strings.TrimSpace(args[i])
			}
		}
	}
	if inputPath == "" {
		c.app.AddMessage("Usage: /import [--format json|markdown] <path>", "system")
		return
	}
	if !filepath.IsAbs(inputPath) {
		inputPath = filepath.Join(c.workDir, inputPath)
	}

	if format == "" {
		ext := strings.ToLower(filepath.Ext(inputPath))
		switch ext {
		case ".md", ".markdown":
			format = conversation.ExportMarkdown
		case ".json":
			format = conversation.ExportJSON
		case ".html", ".htm":
			c.app.AddMessage("HTML import not supported; use JSON or Markdown.", "system")
			return
		default:
			format = conversation.ExportJSON
		}
	}
	if format == conversation.ExportHTML {
		c.app.AddMessage("HTML import not supported; use JSON or Markdown.", "system")
		return
	}

	data, err := os.ReadFile(inputPath)
	if err != nil {
		c.app.AddMessage("Import failed: "+err.Error(), "system")
		return
	}

	importer := conversation.NewImporter(c.store)
	result, err := importer.Import(data, format)
	if err != nil {
		c.app.AddMessage("Import failed: "+err.Error(), "system")
		return
	}
	if result == nil || strings.TrimSpace(result.SessionID) == "" {
		c.app.AddMessage("Import completed but no session was created.", "system")
		return
	}

	if err := c.store.UpdateSessionProjectPath(result.SessionID, c.workDir); err != nil {
		c.app.AddMessage("Imported session; failed to set project path: "+err.Error(), "system")
	}

	newSess, err := newSessionState(c.baseContext(), c.cfg, c.store, c.workDir, c.telemetry, c.modelMgr, result.SessionID, true, c.progressMgr, c.toastMgr)
	if err != nil {
		c.app.AddMessage("Imported session but failed to load: "+err.Error(), "system")
		return
	}

	c.mu.Lock()
	c.sessions = append([]*SessionState{newSess}, c.sessions...)
	c.currentSession = 0
	c.switchToSessionLocked(0)
	c.mu.Unlock()

	c.app.AddMessage(fmt.Sprintf("Imported session: %s (%d messages)", result.SessionID, result.MessageCount), "system")
	if len(result.Warnings) > 0 {
		var b strings.Builder
		b.WriteString("Import warnings:\n")
		for _, warn := range result.Warnings {
			b.WriteString("- " + warn + "\n")
		}
		c.app.AddMessage(strings.TrimSpace(b.String()), "system")
	}
	c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
}
