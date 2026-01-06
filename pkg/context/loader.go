package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/conversation"
)

// ProjectContext holds parsed AGENTS.md content
type ProjectContext struct {
	Summary    string
	Rules      []string
	Guidelines []string
	SubAgents  map[string]*SubAgentSpec
	TechStack  map[string]string
	Loaded     bool
	RawContent string // Full content for fallback when parsing yields empty
}

// SubAgentSpec defines a sub-agent from AGENTS.md
type SubAgentSpec struct {
	Name         string
	Description  string
	Model        string
	Tools        []string
	MaxCost      float64
	Instructions string
}

// Loader handles AGENTS.md loading and parsing
type Loader struct {
	rootPath string
}

// NewLoader creates a new context loader
func NewLoader(rootPath string) *Loader {
	return &Loader{rootPath: rootPath}
}

// Load attempts to load AGENTS.md from project root
func (l *Loader) Load() (*ProjectContext, error) {
	agentsPath := filepath.Join(l.rootPath, "AGENTS.md")

	// Check if file exists
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		return &ProjectContext{Loaded: false}, nil
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, err
	}

	// Parse markdown content
	ctx := &ProjectContext{
		Loaded:     true,
		SubAgents:  make(map[string]*SubAgentSpec),
		TechStack:  make(map[string]string),
		RawContent: string(content), // Store raw content for fallback
	}

	return l.parseContent(string(content), ctx)
}

// parseContent extracts structured information from markdown
func (l *Loader) parseContent(content string, ctx *ProjectContext) (*ProjectContext, error) {
	parser := newAgentsParser(ctx)
	for _, line := range strings.Split(content, "\n") {
		parser.processLine(strings.TrimSpace(line))
	}
	ctx.Summary = strings.TrimSpace(ctx.Summary)
	return ctx, nil
}

// parseSubAgentField parses a sub-agent field like "- **Model:** gpt-4"
// extractValue extracts the value after a markdown bold label
func extractValue(line, label string) string {
	parts := strings.SplitN(line, label, 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// InjectIntoConversation adds project context to the conversation as a system message
func (l *Loader) InjectIntoConversation(conv *conversation.Conversation, ctx *ProjectContext) {
	if !ctx.Loaded {
		return
	}

	message := buildContextSystemMessage(ctx)
	if strings.TrimSpace(message) == "" {
		return
	}

	// Add as system message at start of conversation
	conv.AddSystemMessage(message)
}

// buildContextSystemMessage formats parsed AGENTS.md content into the system message injected into conversations.
func buildContextSystemMessage(ctx *ProjectContext) string {
	if ctx == nil || !ctx.Loaded {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Project Context\n\n")

	hasStructuredContent := ctx.Summary != "" || len(ctx.Rules) > 0 || len(ctx.Guidelines) > 0
	if hasStructuredContent {
		if ctx.Summary != "" {
			b.WriteString(fmt.Sprintf("**Summary:** %s\n\n", ctx.Summary))
		}
		if len(ctx.Rules) > 0 {
			b.WriteString("**Development Rules:**\n")
			for _, rule := range ctx.Rules {
				b.WriteString(fmt.Sprintf("- %s\n", rule))
			}
			b.WriteString("\n")
		}
		if len(ctx.Guidelines) > 0 {
			b.WriteString("**Agent Guidelines:**\n")
			for _, guideline := range ctx.Guidelines {
				b.WriteString(fmt.Sprintf("- %s\n", guideline))
			}
		}
	} else if strings.TrimSpace(ctx.RawContent) != "" {
		b.WriteString("## From AGENTS.md\n\n")
		b.WriteString(ctx.RawContent)
	}

	return b.String()
}
