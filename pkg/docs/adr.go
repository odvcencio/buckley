package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ADRStatus represents the status of an Architecture Decision Record
type ADRStatus string

const (
	ADRStatusProposed   ADRStatus = "Proposed"
	ADRStatusAccepted   ADRStatus = "Accepted"
	ADRStatusDeprecated ADRStatus = "Deprecated"
	ADRStatusSuperseded ADRStatus = "Superseded"
)

// ADR represents an Architecture Decision Record
type ADR struct {
	Number       int
	Title        string
	Status       ADRStatus
	Date         time.Time
	Context      string
	Decision     string
	Consequences string
	Supersedes   *int // ADR number this supersedes (if any)
	SupersededBy *int // ADR number that supersedes this (if any)
}

// ADRManager handles Architecture Decision Record creation and management
type ADRManager struct {
	decisionsDir string // Path to architecture/decisions
	readmePath   string // Path to decisions/README.md
}

// NewADRManager creates a new ADR manager
func NewADRManager(docsRoot string) *ADRManager {
	decisionsDir := filepath.Join(docsRoot, "architecture", "decisions")
	return &ADRManager{
		decisionsDir: decisionsDir,
		readmePath:   filepath.Join(decisionsDir, "README.md"),
	}
}

// Create generates a new ADR and updates the index
func (m *ADRManager) Create(adr *ADR) (string, error) {
	// Ensure decisions directory exists
	if err := os.MkdirAll(m.decisionsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create decisions directory: %w", err)
	}

	// Get next ADR number if not specified
	if adr.Number == 0 {
		nextNum, err := m.getNextNumber()
		if err != nil {
			return "", err
		}
		adr.Number = nextNum
	}

	// Set date if not specified
	if adr.Date.IsZero() {
		adr.Date = time.Now()
	}

	// Set status if not specified
	if adr.Status == "" {
		adr.Status = ADRStatusProposed
	}

	// Generate filename: 0001-repository-pattern.md
	slug := slugify(adr.Title)
	filename := fmt.Sprintf("%04d-%s.md", adr.Number, slug)
	filePath := filepath.Join(m.decisionsDir, filename)

	// Generate markdown content
	content := m.generateMarkdown(adr)

	// Write ADR file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write ADR: %w", err)
	}

	// Update index in README
	if err := m.updateIndex(adr, filename); err != nil {
		return "", fmt.Errorf("failed to update index: %w", err)
	}

	return filePath, nil
}

// List returns all ADRs sorted by number
func (m *ADRManager) List() ([]ADR, error) {
	files, err := filepath.Glob(filepath.Join(m.decisionsDir, "[0-9][0-9][0-9][0-9]-*.md"))
	if err != nil {
		return nil, err
	}

	var adrs []ADR
	for _, file := range files {
		adr, err := m.parseADR(file)
		if err != nil {
			continue // Skip malformed ADRs
		}
		adrs = append(adrs, adr)
	}

	// Sort by number
	sort.Slice(adrs, func(i, j int) bool {
		return adrs[i].Number < adrs[j].Number
	})

	return adrs, nil
}

// getNextNumber determines the next ADR number
func (m *ADRManager) getNextNumber() (int, error) {
	adrs, err := m.List()
	if err != nil {
		return 0, err
	}

	if len(adrs) == 0 {
		return 1, nil
	}

	// Return highest number + 1
	return adrs[len(adrs)-1].Number + 1, nil
}

// generateMarkdown converts an ADR to markdown format
func (m *ADRManager) generateMarkdown(adr *ADR) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# %d. %s\n\n", adr.Number, adr.Title))
	b.WriteString(fmt.Sprintf("**Status:** %s\n\n", adr.Status))
	b.WriteString(fmt.Sprintf("**Date:** %s\n\n", adr.Date.Format("2006-01-02")))

	if adr.Supersedes != nil {
		b.WriteString(fmt.Sprintf("**Supersedes:** [ADR %d](%04d-*.md)\n\n", *adr.Supersedes, *adr.Supersedes))
	}

	if adr.SupersededBy != nil {
		b.WriteString(fmt.Sprintf("**Superseded By:** [ADR %d](%04d-*.md)\n\n", *adr.SupersededBy, *adr.SupersededBy))
	}

	b.WriteString("## Context\n\n")
	b.WriteString(adr.Context)
	b.WriteString("\n\n")

	b.WriteString("## Decision\n\n")
	b.WriteString(adr.Decision)
	b.WriteString("\n\n")

	b.WriteString("## Consequences\n\n")
	b.WriteString(adr.Consequences)
	b.WriteString("\n")

	return b.String()
}

// updateIndex adds the new ADR to the index in README.md
func (m *ADRManager) updateIndex(adr *ADR, filename string) error {
	// Read current README
	content, err := os.ReadFile(m.readmePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")

	// Find the index section and add entry
	var newLines []string
	indexFound := false

	for _, line := range lines {
		newLines = append(newLines, line)

		// Add after "## Index" line
		if strings.HasPrefix(line, "## Index") {
			indexFound = true
			// Skip the blank line after heading
			continue
		}

		// If we're in the index section and hit the separator, insert before it
		if indexFound && strings.HasPrefix(line, "---") {
			// Insert new ADR entry before separator
			entry := fmt.Sprintf("- [ADR %d: %s](%s) - %s",
				adr.Number, adr.Title, filename, adr.Status)
			newLines = append(newLines[:len(newLines)-1], entry, line)
			indexFound = false
		}
	}

	// Write updated README
	return os.WriteFile(m.readmePath, []byte(strings.Join(newLines, "\n")), 0644)
}

// parseADR reads an ADR file and extracts metadata
func (m *ADRManager) parseADR(filePath string) (ADR, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return ADR{}, err
	}

	lines := strings.Split(string(content), "\n")

	adr := ADR{}

	// Parse filename for number: 0001-repository-pattern.md
	filename := filepath.Base(filePath)
	numStr := filename[:4]
	adr.Number, _ = strconv.Atoi(numStr)

	// Parse content
	for i, line := range lines {
		// Title (first line starting with #)
		if strings.HasPrefix(line, "# ") && adr.Title == "" {
			titleParts := strings.SplitN(line[2:], ". ", 2)
			if len(titleParts) > 1 {
				adr.Title = titleParts[1]
			}
		}

		// Status
		if strings.HasPrefix(line, "**Status:**") {
			status := strings.TrimSpace(strings.TrimPrefix(line, "**Status:**"))
			adr.Status = ADRStatus(status)
		}

		// Date
		if strings.HasPrefix(line, "**Date:**") {
			dateStr := strings.TrimSpace(strings.TrimPrefix(line, "**Date:**"))
			adr.Date, _ = time.Parse("2006-01-02", dateStr)
		}

		// Context section
		if strings.HasPrefix(line, "## Context") && i+2 < len(lines) {
			adr.Context = extractSection(lines, i+2, "## Decision")
		}

		// Decision section
		if strings.HasPrefix(line, "## Decision") && i+2 < len(lines) {
			adr.Decision = extractSection(lines, i+2, "## Consequences")
		}

		// Consequences section
		if strings.HasPrefix(line, "## Consequences") && i+2 < len(lines) {
			adr.Consequences = extractSection(lines, i+2, "##")
		}
	}

	return adr, nil
}

// extractSection extracts text between two markdown headers
func extractSection(lines []string, start int, endMarker string) string {
	var section []string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, endMarker) && endMarker != "##" {
			break
		}
		if endMarker == "##" && strings.HasPrefix(line, "##") {
			break
		}
		section = append(section, line)
	}
	return strings.TrimSpace(strings.Join(section, "\n"))
}

// slugify converts a title to a URL-friendly slug
func slugify(title string) string {
	// Convert to lowercase
	slug := strings.ToLower(title)

	// Replace spaces and special chars with hyphens
	reg := regexp.MustCompile("[^a-z0-9]+")
	slug = reg.ReplaceAllString(slug, "-")

	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	return slug
}
