package commands

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
	Grade       Grade
	Summary     string
	BuildStatus string
	TestStatus  string
	Findings    []Finding
	Remarks     []string
	Approved    bool
	Blockers    []string // Finding IDs
	Suggestions []string // Finding IDs
	RawReview   string
}

// ParseReview extracts structured data from review markdown.
func ParseReview(review string) *ParsedReview {
	parsed := &ParsedReview{
		RawReview: review,
	}

	parsed.Grade = extractGrade(review)
	parsed.Summary = extractSection(review, "Summary")

	statusSection := extractSection(review, "Build & Test Status")
	if statusSection == "" {
		statusSection = extractSection(review, "CI Status")
	}
	parsed.BuildStatus, parsed.TestStatus = parseStatusSection(statusSection)

	parsed.Findings = extractFindings(review)
	parsed.Remarks = extractRemarks(review)
	parsed.Approved, parsed.Blockers, parsed.Suggestions = extractVerdict(review)

	return parsed
}

func extractGrade(review string) Grade {
	re := regexp.MustCompile(`(?m)^## Grade:\s*\[?([A-F])\]?`)
	matches := re.FindStringSubmatch(review)
	if len(matches) >= 2 {
		return Grade(matches[1])
	}
	return ""
}

func extractSection(review, heading string) string {
	headingRe := regexp.MustCompile(`(?m)^##\s+` + regexp.QuoteMeta(heading) + `\s*$`)
	loc := headingRe.FindStringIndex(review)
	if loc == nil {
		return ""
	}

	content := review[loc[1]:]
	nextHeading := regexp.MustCompile(`(?m)^##\s+`)
	nextLoc := nextHeading.FindStringIndex(content)
	if nextLoc != nil {
		content = content[:nextLoc[0]]
	}

	return strings.TrimSpace(content)
}

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

func extractFindings(review string) []Finding {
	var findings []Finding

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

		start := match[1]
		end := len(review)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else {
			nextSection := regexp.MustCompile(`(?m)^##\s+`)
			if loc := nextSection.FindStringIndex(review[start:]); loc != nil {
				end = start + loc[0]
			}
		}

		content := review[start:end]

		finding.File, finding.Line = extractFileLine(content)
		finding.Evidence = extractField(content, "Evidence")
		finding.Impact = extractField(content, "Impact")
		if finding.Impact == "" {
			finding.Impact = extractField(content, "Business Impact")
		}
		finding.Fix = extractField(content, "Fix")
		finding.SuggestedFix = extractCodeBlock(content, "suggested")

		findings = append(findings, finding)
	}

	return findings
}

func extractFileLine(content string) (file string, line int) {
	re := regexp.MustCompile(`\*\*File\*\*:\s*([^\s:]+)(?::(\d+))?`)
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		file = matches[1]
		if len(matches) >= 3 && matches[2] != "" {
			var n int
			for _, c := range matches[2] {
				n = n*10 + int(c-'0')
			}
			line = n
		}
	}
	return
}

func extractField(content, field string) string {
	re := regexp.MustCompile("(?m)\\*\\*" + regexp.QuoteMeta(field) + "\\*\\*:\\s*(.+?)(?:\\n\\*\\*|\\n" + "```" + "|\\n##|\\n###|$)")
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

func extractCodeBlock(content, lang string) string {
	re := regexp.MustCompile("(?s)```" + regexp.QuoteMeta(lang) + `?\s*\n(.*?)\n\s*` + "```")
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

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

func extractVerdict(review string) (approved bool, blockers, suggestions []string) {
	section := extractSection(review, "Verdict")
	if section == "" {
		return
	}

	lower := strings.ToLower(section)
	approved = strings.Contains(lower, "approved**: yes") ||
		strings.Contains(lower, "recommendation**: approve")

	blockers = extractFindingIDs(section, "Blockers")
	suggestions = extractFindingIDs(section, "Suggestions")
	if len(suggestions) == 0 {
		suggestions = extractFindingIDs(section, "Optional")
	}

	return
}

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
