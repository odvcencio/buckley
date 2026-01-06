package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResearchBrief captures pre-implementation research notes.
type ResearchBrief struct {
	Feature       string
	UserGoal      string
	Questions     []ResearchQuestion
	Risks         []string
	RelevantFiles []RelevantFile
	Decisions     []string
	Summary       string

	FilePath string
	Created  time.Time
	Updated  time.Time
	Status   string
}

type ResearchQuestion struct {
	Question string
	Answer   string
}

type RelevantFile struct {
	Path    string
	Reason  string
	Summary string
}

// ResearchGenerator persists research briefs as Markdown.
type ResearchGenerator struct {
	outputDir string
}

// NewResearchGenerator creates an artifact generator for research briefs.
func NewResearchGenerator(outputDir string) *ResearchGenerator {
	return &ResearchGenerator{outputDir: outputDir}
}

// Generate saves the research brief to disk.
func (g *ResearchGenerator) Generate(brief *ResearchBrief) (string, error) {
	if err := os.MkdirAll(g.outputDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create research dir: %w", err)
	}

	now := time.Now()
	filename := fmt.Sprintf("%s-%s-research.md", now.Format("2006-01-02"), brief.Feature)
	path := filepath.Join(g.outputDir, filename)

	content := g.renderMarkdown(brief, now)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write research brief: %w", err)
	}

	brief.FilePath = path
	brief.Created = now
	brief.Updated = now
	brief.Status = "completed"
	return path, nil
}

func (g *ResearchGenerator) renderMarkdown(brief *ResearchBrief, now time.Time) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Research Brief: %s\n\n", formatFeatureName(brief.Feature)))
	b.WriteString(fmt.Sprintf("**Date:** %s\n", now.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("**Status:** %s\n\n", defaultStatus(brief.Status)))

	if brief.UserGoal != "" {
		b.WriteString("## 1. User Goal\n\n")
		b.WriteString(brief.UserGoal + "\n\n")
	}

	if brief.Summary != "" {
		b.WriteString("## 2. Summary\n\n")
		b.WriteString(brief.Summary + "\n\n")
	}

	if len(brief.RelevantFiles) > 0 {
		b.WriteString("## 3. Relevant Files\n\n")
		for _, file := range brief.RelevantFiles {
			b.WriteString(fmt.Sprintf("- `%s` — %s\n", file.Path, file.Reason))
			if file.Summary != "" {
				b.WriteString("  - " + file.Summary + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(brief.Questions) > 0 {
		b.WriteString("## 4. Open Questions\n\n")
		for _, q := range brief.Questions {
			b.WriteString(fmt.Sprintf("- **Q:** %s\n  - **A:** %s\n", q.Question, q.Answer))
		}
		b.WriteString("\n")
	}

	if len(brief.Risks) > 0 {
		b.WriteString("## 5. Risks & Unknowns\n\n")
		for _, risk := range brief.Risks {
			b.WriteString(fmt.Sprintf("- %s\n", risk))
		}
		b.WriteString("\n")
	}

	if len(brief.Decisions) > 0 {
		b.WriteString("## 6. Preliminary Decisions\n\n")
		for _, decision := range brief.Decisions {
			b.WriteString(fmt.Sprintf("- %s\n", decision))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func defaultStatus(status string) string {
	if status == "" {
		return "in_progress"
	}
	return status
}
