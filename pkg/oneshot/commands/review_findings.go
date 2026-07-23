package commands

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
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

// CoverageEntry is one machine-parseable changed-file entry from the review's
// Coverage ledger.
type CoverageEntry struct {
	Path     string
	Evidence string
}

// VerificationState is the normalized, machine-parseable result of a build,
// test, or CI gate. Review prose is not accepted as a substitute for one of
// these explicit states.
type VerificationState string

const (
	VerificationPass        VerificationState = "PASS"
	VerificationFail        VerificationState = "FAIL"
	VerificationPending     VerificationState = "PENDING"
	VerificationNotRun      VerificationState = "NOT_RUN"
	VerificationUnavailable VerificationState = "UNAVAILABLE"
	VerificationUnknown     VerificationState = "UNKNOWN"
)

// FeedbackDisposition records whether prior review feedback was supplied and
// explicitly dispositioned by the reviewer.
type FeedbackDisposition string

const (
	FeedbackNoneSupplied  FeedbackDisposition = "NONE_SUPPLIED"
	FeedbackDispositioned FeedbackDisposition = "DISPOSITIONED"
)

// FeedbackStatus is the reviewer's machine-readable disposition of one
// specific supplied feedback item.
type FeedbackStatus string

const (
	FeedbackAddressed  FeedbackStatus = "ADDRESSED"
	FeedbackDisputed   FeedbackStatus = "DISPUTED"
	FeedbackUnresolved FeedbackStatus = "UNRESOLVED"
)

// FeedbackEntry is one exact-ID entry in the Coverage feedback ledger.
type FeedbackEntry struct {
	ID       string
	Status   FeedbackStatus
	Evidence string
}

// FalsificationConclusion is the outcome of the reviewer's strongest
// adversarial hypothesis.
type FalsificationConclusion string

const (
	FalsificationProved     FalsificationConclusion = "PROVED"
	FalsificationDisproved  FalsificationConclusion = "DISPROVED"
	FalsificationUnresolved FalsificationConclusion = "UNRESOLVED"
)

// ParsedReview contains the structured review data.
type ParsedReview struct {
	Grade                      Grade
	Summary                    string
	BuildStatus                string
	TestStatus                 string
	BuildVerification          VerificationState
	TestVerification           VerificationState
	Coverage                   string
	CoverageEntries            []CoverageEntry
	FeedbackDisposition        FeedbackDisposition
	FeedbackDispositionDetails string
	FeedbackEntries            []FeedbackEntry
	InvariantAudit             string
	Falsification              string
	FalsificationConclusion    FalsificationConclusion
	Verdict                    string
	Findings                   []Finding
	Remarks                    []string
	Approved                   bool
	Blockers                   []string // Finding IDs
	Suggestions                []string // Finding IDs
	RawReview                  string
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
	parsed.BuildVerification = parseVerificationState(parsed.BuildStatus)
	parsed.TestVerification = parseVerificationState(parsed.TestStatus)
	parsed.Coverage = extractSection(review, "Coverage")
	parsed.CoverageEntries, parsed.FeedbackDisposition, parsed.FeedbackDispositionDetails, parsed.FeedbackEntries = parseCoverageLedger(parsed.Coverage)
	parsed.InvariantAudit = extractSection(review, "Invariant Audit")
	parsed.Falsification = extractSection(review, "Falsification")
	parsed.FalsificationConclusion = parseFalsificationConclusion(parsed.Falsification)
	parsed.Verdict = extractSection(review, "Verdict")

	parsed.Findings = extractFindings(review)
	parsed.Remarks = extractRemarks(review)
	parsed.Approved, parsed.Blockers, parsed.Suggestions = extractVerdict(review)

	return parsed
}

// ReviewValidationOptions describes evidence that a structured review must
// account for before the CLI can present it as a valid result.
type ReviewValidationOptions struct {
	ChangedFiles                []string
	ContextIncomplete           bool
	CIStatus                    string
	CIProvenance                string
	RequiresFeedbackDisposition bool
	RequiredFeedbackIDs         []string
	RequirePassingRemoteCI      bool
}

// ValidateParsedReview rejects incomplete or internally inconsistent review
// artifacts. It validates evidence coverage, not whether the model's technical
// judgment is correct.
func ValidateParsedReview(parsed *ParsedReview, opts ReviewValidationOptions) error {
	if parsed == nil {
		return fmt.Errorf("parsed review is missing")
	}
	var missing []string
	if parsed.Grade == "" {
		missing = append(missing, "grade")
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		missing = append(missing, "summary")
	}
	if strings.TrimSpace(parsed.BuildStatus) == "" || strings.TrimSpace(parsed.TestStatus) == "" {
		missing = append(missing, "build/test or CI status")
	}
	if strings.TrimSpace(parsed.Coverage) == "" {
		missing = append(missing, "Coverage section")
	}
	if strings.TrimSpace(parsed.InvariantAudit) == "" {
		missing = append(missing, "Invariant Audit section")
	}
	if strings.TrimSpace(parsed.Falsification) == "" {
		missing = append(missing, "Falsification section")
	} else if parsed.FalsificationConclusion == "" {
		missing = append(missing, "Falsification conclusion (PROVED, DISPROVED, or UNRESOLVED)")
	}
	if parsed.FeedbackDisposition == "" {
		missing = append(missing, "explicit feedback disposition")
	}
	if strings.TrimSpace(parsed.Verdict) == "" {
		missing = append(missing, "Verdict section")
	}
	if len(missing) > 0 {
		return fmt.Errorf("review is missing required evidence: %s", strings.Join(missing, ", "))
	}
	if parsed.BuildVerification == "" {
		return fmt.Errorf("build status must start with one exact verification state: %s", verificationStateList())
	}
	if parsed.TestVerification == "" {
		return fmt.Errorf("tests status must start with one exact verification state: %s", verificationStateList())
	}
	verdictApproved, err := parseVerdictApproval(parsed.Verdict)
	if err != nil {
		return fmt.Errorf("invalid Verdict decision: %w", err)
	}
	if verdictApproved != parsed.Approved {
		return fmt.Errorf("verdict decision is inconsistent with the parsed approval state")
	}

	if err := validateCoverageLedger(parsed.CoverageEntries, opts.ChangedFiles); err != nil {
		return err
	}
	if opts.RequiresFeedbackDisposition && len(opts.RequiredFeedbackIDs) == 0 {
		return fmt.Errorf("review context supplied feedback but no required feedback IDs were provided for exact disposition")
	}
	if len(opts.RequiredFeedbackIDs) > 0 {
		if parsed.FeedbackDisposition != FeedbackDispositioned {
			return fmt.Errorf("coverage must mark supplied review feedback as %s", FeedbackDispositioned)
		}
	} else if parsed.FeedbackDisposition != FeedbackNoneSupplied {
		return fmt.Errorf("coverage must mark feedback as %s when no feedback IDs were supplied", FeedbackNoneSupplied)
	}
	if err := validateFeedbackLedger(parsed.FeedbackEntries, opts.RequiredFeedbackIDs); err != nil {
		return err
	}

	if parsed.Approved {
		if parsed.FalsificationConclusion != FalsificationDisproved {
			return fmt.Errorf("an approval requires a DISPROVED falsification conclusion, got %s", parsed.FalsificationConclusion)
		}
		if opts.ContextIncomplete {
			return fmt.Errorf("an approval cannot be issued from incomplete or truncated review context")
		}
		if parsed.BuildVerification != VerificationPass {
			return fmt.Errorf("an approval requires Build status PASS, got %s", parsed.BuildVerification)
		}
		if parsed.TestVerification != VerificationPass {
			return fmt.Errorf("an approval requires Tests status PASS, got %s", parsed.TestVerification)
		}
		if opts.RequirePassingRemoteCI {
			ciState := parseRemoteCIState(opts.CIStatus)
			if ciState != VerificationPass {
				return fmt.Errorf("an approval requires authoritative remote CI PASS, got %s from %q", ciState, opts.CIStatus)
			}
			switch opts.CIProvenance {
			case prCISourceHead:
			case prCISourceBase:
				if !reviewChangedFilesDocumentationOnly(opts.ChangedFiles) {
					return fmt.Errorf("immutable-base CI can authorize only a documentation-only approval")
				}
			default:
				return fmt.Errorf("an approval requires explicit remote CI provenance, got %q", opts.CIProvenance)
			}
		}
		for _, feedback := range parsed.FeedbackEntries {
			if feedback.Status == FeedbackUnresolved {
				return fmt.Errorf("an approval is inconsistent with unresolved feedback %s", feedback.ID)
			}
		}
		if parsed.Grade != GradeA && parsed.Grade != GradeB {
			return fmt.Errorf("approval is inconsistent with grade %s", parsed.Grade)
		}
		if len(parsed.Blockers) > 0 {
			return fmt.Errorf("approval is inconsistent with blockers: %s", strings.Join(parsed.Blockers, ", "))
		}
		for _, finding := range parsed.Findings {
			if finding.Severity == SeverityCritical || finding.Severity == SeverityMajor {
				return fmt.Errorf("approval is inconsistent with blocking finding %s", finding.ID)
			}
		}
	} else if parsed.Grade == GradeA {
		return fmt.Errorf("grade A is inconsistent with a non-approval verdict")
	}

	return nil
}

func verificationStateList() string {
	return "PASS, FAIL, PENDING, NOT_RUN, UNAVAILABLE, or UNKNOWN"
}

func parseVerificationState(value string) VerificationState {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "`") {
		closing := strings.Index(value[1:], "`")
		if closing < 0 {
			return ""
		}
		closing++
		value = value[1:closing] + value[closing+1:]
	}

	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	state := VerificationState(strings.ToUpper(strings.TrimSpace(fields[0])))
	switch state {
	case VerificationPass, VerificationFail, VerificationPending, VerificationNotRun, VerificationUnavailable, VerificationUnknown:
		return state
	default:
		return ""
	}
}

func parseRemoteCIState(value string) VerificationState {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "unknown" {
		return VerificationUnknown
	}
	if value == "no checks" {
		return VerificationNotRun
	}

	re := regexp.MustCompile(`^(passing|failing|pending)\s+\((\d+)/(\d+)\)$`)
	matches := re.FindStringSubmatch(value)
	if len(matches) != 4 {
		return VerificationUnknown
	}
	count, countErr := strconv.Atoi(matches[2])
	total, totalErr := strconv.Atoi(matches[3])
	if countErr != nil || totalErr != nil || total <= 0 || count > total {
		return VerificationUnknown
	}
	switch matches[1] {
	case "passing":
		if count != total {
			return VerificationUnknown
		}
		return VerificationPass
	case "failing":
		return VerificationFail
	case "pending":
		return VerificationPending
	default:
		return VerificationUnknown
	}
}

func parseFalsificationConclusion(section string) FalsificationConclusion {
	re := regexp.MustCompile("(?mi)^\\s*(?:-\\s+)?\\*\\*Conclusion\\*\\*:\\s*`?(PROVED|DISPROVED|UNRESOLVED)`?(?:\\s*[.—:(-].*)?\\s*$")
	matches := re.FindStringSubmatch(section)
	if len(matches) < 2 {
		return ""
	}
	return FalsificationConclusion(strings.ToUpper(matches[1]))
}

func parseCoverageLedger(section string) ([]CoverageEntry, FeedbackDisposition, string, []FeedbackEntry) {
	var entries []CoverageEntry
	var disposition FeedbackDisposition
	var dispositionDetails string
	var feedbackEntries []FeedbackEntry

	for _, rawLine := range strings.Split(section, "\n") {
		line := strings.TrimSpace(rawLine)
		normalized := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		lower := strings.ToLower(normalized)
		const filePrefix = "**file**:"
		const feedbackPrefix = "**feedback disposition**:"
		const feedbackEntryPrefix = "**feedback**:"

		switch {
		case strings.HasPrefix(lower, filePrefix):
			rest := strings.TrimSpace(normalized[len(filePrefix):])
			if !strings.HasPrefix(rest, "`") {
				continue
			}
			closing := strings.Index(rest[1:], "`")
			if closing < 0 {
				continue
			}
			closing++
			rawPath := rest[1:closing]
			evidence := trimCoverageSeparator(rest[closing+1:])
			entries = append(entries, CoverageEntry{Path: rawPath, Evidence: evidence})

		case strings.HasPrefix(lower, feedbackPrefix):
			rest := strings.TrimSpace(normalized[len(feedbackPrefix):])
			status, details := parseFeedbackDisposition(rest)
			if status != "" {
				disposition = status
				dispositionDetails = details
			}

		case strings.HasPrefix(lower, feedbackEntryPrefix):
			rest := strings.TrimSpace(normalized[len(feedbackEntryPrefix):])
			if entry, ok := parseFeedbackEntry(rest); ok {
				feedbackEntries = append(feedbackEntries, entry)
			}
		}
	}

	return entries, disposition, dispositionDetails, feedbackEntries
}

func parseFeedbackEntry(value string) (FeedbackEntry, bool) {
	id, rest, ok := parseBacktickToken(value)
	if !ok {
		return FeedbackEntry{}, false
	}
	rest = trimCoverageSeparator(rest)
	statusToken, rest, ok := parseBacktickToken(rest)
	if !ok {
		return FeedbackEntry{}, false
	}
	status := FeedbackStatus(strings.ToUpper(strings.TrimSpace(statusToken)))
	if status != FeedbackAddressed && status != FeedbackDisputed && status != FeedbackUnresolved {
		return FeedbackEntry{ID: strings.TrimSpace(id)}, true
	}
	return FeedbackEntry{
		ID:       strings.TrimSpace(id),
		Status:   status,
		Evidence: trimCoverageSeparator(rest),
	}, true
}

func parseBacktickToken(value string) (token, rest string, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "`") {
		return "", "", false
	}
	closing := strings.Index(value[1:], "`")
	if closing < 0 {
		return "", "", false
	}
	closing++
	return value[1:closing], value[closing+1:], true
}

func parseFeedbackDisposition(value string) (FeedbackDisposition, string) {
	value = strings.TrimSpace(value)
	var token string
	var rest string
	if strings.HasPrefix(value, "`") {
		closing := strings.Index(value[1:], "`")
		if closing < 0 {
			return "", ""
		}
		closing++
		token = value[1:closing]
		rest = value[closing+1:]
	} else {
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return "", ""
		}
		token = fields[0]
		rest = strings.TrimPrefix(value, fields[0])
	}

	status := FeedbackDisposition(strings.ToUpper(strings.TrimSpace(token)))
	if status != FeedbackNoneSupplied && status != FeedbackDispositioned {
		return "", ""
	}
	return status, trimCoverageSeparator(rest)
}

func trimCoverageSeparator(value string) string {
	value = strings.TrimSpace(value)
	for _, separator := range []string{"—", "-", "|", ":"} {
		if strings.HasPrefix(value, separator) {
			return strings.TrimSpace(strings.TrimPrefix(value, separator))
		}
	}
	return value
}

func validateCoverageLedger(entries []CoverageEntry, changedFiles []string) error {
	expected := make(map[string]struct{}, len(changedFiles))
	for _, changedFile := range changedFiles {
		if normalized := normalizeCoveragePath(changedFile); normalized != "" {
			expected[normalized] = struct{}{}
		}
	}

	actual := make(map[string]struct{}, len(entries))
	var unexpected []string
	var duplicates []string
	var missingEvidence []string
	for _, entry := range entries {
		normalized := normalizeCoveragePath(entry.Path)
		if normalized == "" {
			continue
		}
		if _, exists := actual[normalized]; exists {
			duplicates = append(duplicates, normalized)
			continue
		}
		actual[normalized] = struct{}{}
		if _, exists := expected[normalized]; !exists {
			unexpected = append(unexpected, normalized)
		}
		if strings.TrimSpace(entry.Evidence) == "" {
			missingEvidence = append(missingEvidence, normalized)
		}
	}

	var missing []string
	for expectedPath := range expected {
		if _, exists := actual[expectedPath]; !exists {
			missing = append(missing, expectedPath)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	sort.Strings(duplicates)
	sort.Strings(missingEvidence)

	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "missing "+strings.Join(missing, ", "))
	}
	if len(unexpected) > 0 {
		problems = append(problems, "unexpected "+strings.Join(unexpected, ", "))
	}
	if len(duplicates) > 0 {
		problems = append(problems, "duplicate "+strings.Join(duplicates, ", "))
	}
	if len(missingEvidence) > 0 {
		problems = append(problems, "missing evidence for "+strings.Join(missingEvidence, ", "))
	}
	if len(problems) > 0 {
		return fmt.Errorf("coverage ledger does not exactly match changed files: %s", strings.Join(problems, "; "))
	}
	return nil
}

func validateFeedbackLedger(entries []FeedbackEntry, requiredIDs []string) error {
	expected := make(map[string]struct{}, len(requiredIDs))
	var invalidRequired []string
	for _, requiredID := range requiredIDs {
		id := strings.TrimSpace(requiredID)
		if id == "" {
			invalidRequired = append(invalidRequired, "<empty>")
			continue
		}
		expected[id] = struct{}{}
	}

	actual := make(map[string]struct{}, len(entries))
	var unexpected []string
	var duplicates []string
	var invalidStatus []string
	var missingEvidence []string
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, exists := actual[id]; exists {
			duplicates = append(duplicates, id)
			continue
		}
		actual[id] = struct{}{}
		if _, exists := expected[id]; !exists {
			unexpected = append(unexpected, id)
		}
		if entry.Status != FeedbackAddressed && entry.Status != FeedbackDisputed && entry.Status != FeedbackUnresolved {
			invalidStatus = append(invalidStatus, id)
		}
		if strings.TrimSpace(entry.Evidence) == "" {
			missingEvidence = append(missingEvidence, id)
		}
	}

	var missing []string
	for expectedID := range expected {
		if _, exists := actual[expectedID]; !exists {
			missing = append(missing, expectedID)
		}
	}
	for _, values := range [][]string{invalidRequired, missing, unexpected, duplicates, invalidStatus, missingEvidence} {
		sort.Strings(values)
	}

	var problems []string
	if len(invalidRequired) > 0 {
		problems = append(problems, "invalid required IDs "+strings.Join(invalidRequired, ", "))
	}
	if len(missing) > 0 {
		problems = append(problems, "missing "+strings.Join(missing, ", "))
	}
	if len(unexpected) > 0 {
		problems = append(problems, "unexpected "+strings.Join(unexpected, ", "))
	}
	if len(duplicates) > 0 {
		problems = append(problems, "duplicate "+strings.Join(duplicates, ", "))
	}
	if len(invalidStatus) > 0 {
		problems = append(problems, "invalid status for "+strings.Join(invalidStatus, ", "))
	}
	if len(missingEvidence) > 0 {
		problems = append(problems, "missing evidence for "+strings.Join(missingEvidence, ", "))
	}
	if len(problems) > 0 {
		return fmt.Errorf("feedback ledger does not exactly match supplied feedback IDs: %s", strings.Join(problems, "; "))
	}
	return nil
}

func normalizeCoveragePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "./")
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

	approved, _ = parseVerdictApproval(section)

	blockers = extractFindingIDs(section, "Blockers")
	suggestions = extractFindingIDs(section, "Suggestions")
	if len(suggestions) == 0 {
		suggestions = extractFindingIDs(section, "Optional")
	}

	return
}

var verdictDecisionLineRE = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?\*\*(Approved|Recommendation)\*\*:\s*(.*?)\s*$`)

// parseVerdictApproval accepts exactly one machine-readable decision line. It
// deliberately rejects option-list placeholders, prose suffixes, and duplicate
// decision fields so an ambiguous template can never be interpreted as an
// approval by substring matching.
func parseVerdictApproval(section string) (bool, error) {
	matches := verdictDecisionLineRE.FindAllStringSubmatch(section, -1)
	if len(matches) != 1 {
		return false, fmt.Errorf(
			"expected exactly one **Approved** or **Recommendation** decision line, got %d",
			len(matches),
		)
	}

	field := strings.ToLower(strings.TrimSpace(matches[0][1]))
	value := strings.ToUpper(strings.Join(strings.Fields(matches[0][2]), " "))
	switch field {
	case "approved":
		switch value {
		case "YES":
			return true, nil
		case "NO":
			return false, nil
		default:
			return false, fmt.Errorf("**Approved** must be exactly YES or NO, got %q", matches[0][2])
		}
	case "recommendation":
		switch value {
		case "APPROVE":
			return true, nil
		case "REQUEST CHANGES", "NEEDS DISCUSSION":
			return false, nil
		default:
			return false, fmt.Errorf(
				"**Recommendation** must be exactly APPROVE, REQUEST CHANGES, or NEEDS DISCUSSION, got %q",
				matches[0][2],
			)
		}
	default:
		return false, fmt.Errorf("unsupported Verdict decision field %q", matches[0][1])
	}
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
