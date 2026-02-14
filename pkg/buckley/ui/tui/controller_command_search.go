package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/embeddings"
)

func (c *Controller) handleSearchCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Search unavailable: storage not configured.", "system")
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

	mode := ""
	limit := 5
	scopeAll := false
	var queryParts []string
	for i := 0; i < len(args); i++ {
		switch strings.ToLower(strings.TrimSpace(args[i])) {
		case "--semantic", "-s":
			mode = "semantic"
		case "--fulltext", "--fts", "-f":
			mode = "fulltext"
		case "--all":
			scopeAll = true
		case "--limit", "-l":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /search [--semantic|--fulltext] [--all] [--limit N] <query>", "system")
				return
			}
			value, err := strconv.Atoi(args[i+1])
			if err != nil || value <= 0 {
				c.app.AddMessage("Limit must be a positive integer.", "system")
				return
			}
			limit = value
			i++
		default:
			queryParts = append(queryParts, args[i])
		}
	}

	query := strings.TrimSpace(strings.Join(queryParts, " "))
	if query == "" {
		c.app.AddMessage("Usage: /search [--semantic|--fulltext] [--all] [--limit N] <query>", "system")
		return
	}

	var provider embeddings.EmbeddingProvider
	if mode == "" || mode == "semantic" {
		provider = c.newEmbeddingProvider()
		if mode == "" {
			if provider != nil {
				mode = "semantic"
			} else {
				mode = "fulltext"
			}
		}
	}

	if scopeAll {
		if mode == "semantic" {
			c.app.AddMessage("Semantic search is session-scoped; use /search --fulltext --all <query>.", "system")
			return
		}
		sessionID = ""
	}

	searcher := conversation.NewConversationSearcher(c.store, provider)
	opts := conversation.SearchOptions{SessionID: sessionID, Limit: limit}
	ctx := c.baseContext()

	var (
		results []conversation.SearchResult
		err     error
	)
	switch mode {
	case "semantic":
		if provider == nil {
			c.app.AddMessage("Semantic search unavailable: configure OPENROUTER_API_KEY or OPENAI_API_KEY.", "system")
			return
		}
		results, err = searcher.Search(ctx, query, opts)
	default:
		results, err = searcher.SearchFullText(ctx, query, opts)
		mode = "fulltext"
	}
	if err != nil {
		c.app.AddMessage("Search failed: "+err.Error(), "system")
		return
	}
	if len(results) == 0 {
		c.app.AddMessage("No matches found.", "system")
		return
	}

	scopeLabel := "current session"
	if sessionID == "" {
		scopeLabel = "all sessions"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Search results (%s, %s):\n", mode, scopeLabel))
	for i, res := range results {
		line := fmt.Sprintf("%d. [%.2f] %s", i+1, res.Score, res.Role)
		if strings.TrimSpace(res.SessionID) != "" {
			line += " (" + res.SessionID + ")"
		}
		if !res.Timestamp.IsZero() {
			line += " " + res.Timestamp.Format(time.RFC3339)
		}
		b.WriteString(line + "\n")
		snippet := strings.TrimSpace(res.Snippet)
		if snippet == "" {
			snippet = strings.TrimSpace(res.Content)
		}
		if len(snippet) > 240 {
			snippet = snippet[:240] + "..."
		}
		if snippet != "" {
			b.WriteString(snippet + "\n\n")
		}
	}
	c.app.AddMessage(strings.TrimSpace(b.String()), "system")
}
