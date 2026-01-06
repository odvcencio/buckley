package review

import (
	"regexp"
	"strings"
)

// Severity levels for findings.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityMajor    Severity = "MAJOR"
	SeverityMinor    Severity = "MINOR"
)

// Grade represents the overall review grade.
type Grade string

const (
	GradeA Grade = "A"
	GradeB Grade = "B"
	GradeC Grade = "C"
	GradeD Grade = "D"
	GradeF Grade = "F"
)

// Finding represents a single issue found during review.
type Finding struct {
	ID           string   // e.g., "FINDING-001"
	Severity     Severity // CRITICAL, MAJOR, MINOR
	Title        string   // Brief description
	File         string   // File path
	Line         int      // Line number (0 if not specified)
	Evidence     string   // Proof of the issue
	Impact       string   // Business/technical impact
	Fix          string   // Description of fix
	SuggestedFix string   // Code block with suggested fix
}

// ParsedReview contains the structured review data.
type ParsedReview struct {
	Grade        Grade
	Summary      string
	BuildStatus  string
	TestStatus   string
	Findings     []Finding
	Remarks      []string
	Approved     bool
	Blockers     []string // Finding IDs
	Suggestions  []string // Finding IDs
	RawReview    string
}

// ParseReview extracts structured data from review markdown.
func ParseReview(review string) *ParsedReview {
	parsed := &ParsedReview{
		RawReview: review,
	}

	// Extract grade
	parsed.Grade = extractGrade(review)

	// Extract summary
	parsed.Summary = extractSection(review, "Summary")

	// Extract build/test status
	statusSection := extractSection(review, "Build & Test Status")
	if statusSection == "" {
		statusSection = extractSection(review, "CI Status")
	}
	parsed.BuildStatus, parsed.TestStatus = parseStatusSection(statusSection)

	// Extract findings
	parsed.Findings = extractFindings(review)

	// Extract remarks
	parsed.Remarks = extractRemarks(review)

	// Extract verdict
	parsed.Approved, parsed.Blockers, parsed.Suggestions = extractVerdict(review)

	return parsed
}

// extractGrade finds the grade from "## Grade: X"
func extractGrade(review string) Grade {
	re := regexp.MustCompile(`(?m)^## Grade:\s*\[?([A-F])\]?`)
	matches := re.FindStringSubmatch(review)
	if len(matches) >= 2 {
		return Grade(matches[1])
	}
	return ""
}

// extractSection extracts content under a ## heading.
func extractSection(review, heading string) string {
	// Find the heading
	headingRe := regexp.MustCompile(`(?m)^##\s+` + regexp.QuoteMeta(heading) + `\s*$`)
	loc := headingRe.FindStringIndex(review)
	if loc == nil {
		return ""
	}

	// Find content until next ## heading
	content := review[loc[1]:]
	nextHeading := regexp.MustCompile(`(?m)^##\s+`)
	nextLoc := nextHeading.FindStringIndex(content)
	if nextLoc != nil {
		content = content[:nextLoc[0]]
	}

	return strings.TrimSpace(content)
}

// parseStatusSection extracts build and test status.
func parseStatusSection(section string) (build, test string) {
	lines := strings.Split(section, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "- build:") || strings.HasPrefix(lower, "build:") {
			build = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		} else if strings.HasPrefix(lower, "- tests:") || strings.HasPrefix(lower, "tests:") {
			test = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return
}

// extractFindings parses all FINDING-XXX blocks.
func extractFindings(review string) []Finding {
	var findings []Finding

	// Match finding headers: ### FINDING-001: [CRITICAL] Title
	findingRe := regexp.MustCompile(`(?m)^###\s+(FINDING-\d+):\s*\[?(CRITICAL|MAJOR|MINOR)\]?\s+(.+)$`)
	matches := findingRe.FindAllStringSubmatchIndex(review, -1)

	for i, match := range matches {
		if len(match) < 8 {
			continue
		}

		finding := Finding{
			ID:       review[match[2]:match[3]],
			Severity: Severity(review[match[4]:match[5]]),
			Title:    strings.TrimSpace(review[match[6]:match[7]]),
		}

		// Get content until next finding or section
		start := match[1]
		end := len(review)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else {
			// Check for next ## section
			nextSection := regexp.MustCompile(`(?m)^##\s+`)
			if loc := nextSection.FindStringIndex(review[start:]); loc != nil {
				end = start + loc[0]
			}
		}

		content := review[start:end]

		// Extract file:line
		finding.File, finding.Line = extractFileLine(content)

		// Extract evidence
		finding.Evidence = extractField(content, "Evidence")

		// Extract impact
		finding.Impact = extractField(content, "Impact")
		if finding.Impact == "" {
			finding.Impact = extractField(content, "Business Impact")
		}

		// Extract fix description
		finding.Fix = extractField(content, "Fix")

		// Extract suggested code
		finding.SuggestedFix = extractCodeBlock(content, "suggested")

		findings = append(findings, finding)
	}

	return findings
}

// extractFileLine parses "**File**: path/to/file.go:123"
func extractFileLine(content string) (file string, line int) {
	re := regexp.MustCompile(`\*\*File\*\*:\s*([^\s:]+)(?::(\d+))?`)
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		file = matches[1]
		if len(matches) >= 3 && matches[2] != "" {
			// Parse line number
			var n int
			for _, c := range matches[2] {
				n = n*10 + int(c-'0')
			}
			line = n
		}
	}
	return
}

// extractField extracts a markdown field like "**Field**: value"
func extractField(content, field string) string {
	// Match field until next field, code block, or section header
	re := regexp.MustCompile("(?m)\\*\\*" + regexp.QuoteMeta(field) + "\\*\\*:\\s*(.+?)(?:\\n\\*\\*|\\n" + "```" + "|\\n##|\\n###|$)")
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// extractCodeBlock extracts a fenced code block with optional language hint.
func extractCodeBlock(content, lang string) string {
	// Match ```lang or ``` followed by content until ```
	re := regexp.MustCompile("(?s)```" + regexp.QuoteMeta(lang) + `?\s*\n(.*?)\n\s*` + "```")
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// extractRemarks parses the Remarks section as a list.
func extractRemarks(review string) []string {
	section := extractSection(review, "Remarks")
	if section == "" {
		return nil
	}

	var remarks []string
	lines := strings.Split(section, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			remarks = append(remarks, strings.TrimPrefix(line, "- "))
		} else if strings.HasPrefix(line, "* ") {
			remarks = append(remarks, strings.TrimPrefix(line, "* "))
		}
	}
	return remarks
}

// extractVerdict parses the Verdict section.
func extractVerdict(review string) (approved bool, blockers, suggestions []string) {
	section := extractSection(review, "Verdict")
	if section == "" {
		return
	}

	// Check approval
	lower := strings.ToLower(section)
	approved = strings.Contains(lower, "approved**: yes") ||
		strings.Contains(lower, "recommendation**: approve")

	// Extract blockers
	blockers = extractFindingIDs(section, "Blockers")

	// Extract suggestions
	suggestions = extractFindingIDs(section, "Suggestions")
	if len(suggestions) == 0 {
		suggestions = extractFindingIDs(section, "Optional")
	}

	return
}

// extractFindingIDs extracts FINDING-XXX IDs from a field.
func extractFindingIDs(section, field string) []string {
	re := regexp.MustCompile(`(?i)\*\*` + regexp.QuoteMeta(field) + `\*\*:\s*(.+)`)
	matches := re.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil
	}

	idRe := regexp.MustCompile(`FINDING-\d+`)
	return idRe.FindAllString(matches[1], -1)
}

// CriticalFindings returns only critical severity findings.
func (p *ParsedReview) CriticalFindings() []Finding {
	var critical []Finding
	for _, f := range p.Findings {
		if f.Severity == SeverityCritical {
			critical = append(critical, f)
		}
	}
	return critical
}

// MajorFindings returns only major severity findings.
func (p *ParsedReview) MajorFindings() []Finding {
	var major []Finding
	for _, f := range p.Findings {
		if f.Severity == SeverityMajor {
			major = append(major, f)
		}
	}
	return major
}

// MinorFindings returns only minor severity findings.
func (p *ParsedReview) MinorFindings() []Finding {
	var minor []Finding
	for _, f := range p.Findings {
		if f.Severity == SeverityMinor {
			minor = append(minor, f)
		}
	}
	return minor
}

// BlockingFindings returns findings that block approval (Critical + Major).
func (p *ParsedReview) BlockingFindings() []Finding {
	var blocking []Finding
	for _, f := range p.Findings {
		if f.Severity == SeverityCritical || f.Severity == SeverityMajor {
			blocking = append(blocking, f)
		}
	}
	return blocking
}

// HasBlockers returns true if there are blocking findings.
func (p *ParsedReview) HasBlockers() bool {
	for _, f := range p.Findings {
		if f.Severity == SeverityCritical || f.Severity == SeverityMajor {
			return true
		}
	}
	return false
}

// FindingByID returns a finding by its ID.
func (p *ParsedReview) FindingByID(id string) *Finding {
	for i := range p.Findings {
		if p.Findings[i].ID == id {
			return &p.Findings[i]
		}
	}
	return nil
}
