package commands

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/oneshot"
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
	var problems []string
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
		missing = append(
			missing,
			`Falsification conclusion (write "- **Conclusion**: PROVED", DISPROVED, or UNRESOLVED)`,
		)
	}
	if parsed.FeedbackDisposition == "" {
		missing = append(missing, "explicit feedback disposition")
	}
	if strings.TrimSpace(parsed.Verdict) == "" {
		missing = append(missing, "Verdict section")
	}
	if len(missing) > 0 {
		problems = append(problems, "review is missing required evidence: "+strings.Join(missing, ", "))
	}
	if parsed.BuildVerification == "" {
		problems = append(
			problems,
			fmt.Sprintf("build status must start with one exact verification state: %s", verificationStateList()),
		)
	}
	if parsed.TestVerification == "" {
		problems = append(
			problems,
			fmt.Sprintf("tests status must start with one exact verification state: %s", verificationStateList()),
		)
	}
	if strings.TrimSpace(parsed.Verdict) != "" {
		verdictApproved, err := parseVerdictApproval(parsed.Verdict)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid Verdict decision: %v", err))
		} else if verdictApproved != parsed.Approved {
			problems = append(problems, "verdict decision is inconsistent with the parsed approval state")
		}
	}
	if err := validateFindingDisposition(parsed); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateDemonstratedFindings(parsed.Findings); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateFalsificationDisposition(parsed); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(parsed.Verdict) != "" {
		if err := validateVerdictDisposition(parsed); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if strings.TrimSpace(parsed.Coverage) != "" {
		if err := validateCoverageLedger(parsed.CoverageEntries, opts.ChangedFiles); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if opts.RequiresFeedbackDisposition && len(opts.RequiredFeedbackIDs) == 0 {
		problems = append(
			problems,
			"review context supplied feedback but no required feedback IDs were provided for exact disposition",
		)
	}
	if parsed.FeedbackDisposition != "" {
		if len(opts.RequiredFeedbackIDs) > 0 {
			if parsed.FeedbackDisposition != FeedbackDispositioned {
				problems = append(
					problems,
					fmt.Sprintf("coverage must mark supplied review feedback as %s", FeedbackDispositioned),
				)
			}
		} else if parsed.FeedbackDisposition != FeedbackNoneSupplied {
			problems = append(
				problems,
				fmt.Sprintf("coverage must mark feedback as %s when no feedback IDs were supplied", FeedbackNoneSupplied),
			)
		}
	}
	if err := validateFeedbackLedger(parsed.FeedbackEntries, opts.RequiredFeedbackIDs); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return fmt.Errorf("review validation failed: %s", strings.Join(problems, "; "))
	}

	if parsed.Approved {
		if parsed.FalsificationConclusion != FalsificationDisproved {
			return fmt.Errorf("an approval requires a DISPROVED falsification conclusion, got %s", parsed.FalsificationConclusion)
		}
		if opts.ContextIncomplete {
			return fmt.Errorf("an approval cannot be issued from incomplete or truncated review context")
		}
		if parsed.BuildVerification != VerificationPass {
			return oneshot.RequireRLMExecutionEvidence(fmt.Errorf(
				"an approval requires Build status PASS, got %s",
				parsed.BuildVerification,
			))
		}
		if parsed.TestVerification != VerificationPass {
			return oneshot.RequireRLMExecutionEvidence(fmt.Errorf(
				"an approval requires Tests status PASS, got %s",
				parsed.TestVerification,
			))
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
		if len(parsed.Blockers) > 0 {
			return fmt.Errorf("approval is inconsistent with blockers: %s", strings.Join(parsed.Blockers, ", "))
		}
		for _, finding := range parsed.Findings {
			if finding.Severity == SeverityCritical || finding.Severity == SeverityMajor {
				return fmt.Errorf("approval is inconsistent with blocking finding %s", finding.ID)
			}
		}
		var qualityProblems []string
		if parsed.Grade != GradeA {
			qualityProblems = append(qualityProblems, fmt.Sprintf("grade is %s instead of A", parsed.Grade))
		}
		if len(parsed.Findings) > 0 {
			qualityProblems = append(qualityProblems, "Findings is not empty")
		}
		if len(parsed.Suggestions) > 0 {
			qualityProblems = append(qualityProblems, "Suggestions is not empty")
		}
		if len(qualityProblems) > 0 {
			return fmt.Errorf(
				"approval quality gate failed: %s; remove non-defect observations or return a non-approval verdict for demonstrated defects",
				strings.Join(qualityProblems, ", "),
			)
		}
	} else if parsed.Grade == GradeA {
		return fmt.Errorf("grade A is inconsistent with a non-approval verdict")
	}

	return nil
}

func validateFindingDisposition(parsed *ParsedReview) error {
	findings := make(map[string]Finding, len(parsed.Findings))
	for _, finding := range parsed.Findings {
		if _, exists := findings[finding.ID]; exists {
			return fmt.Errorf("duplicate finding ID %s", finding.ID)
		}
		findings[finding.ID] = finding
	}

	blockers := make(map[string]struct{}, len(parsed.Blockers))
	for _, id := range parsed.Blockers {
		if _, exists := findings[id]; !exists {
			return fmt.Errorf("blockers references unknown finding %s", id)
		}
		if _, exists := blockers[id]; exists {
			return fmt.Errorf("blockers repeats finding %s", id)
		}
		blockers[id] = struct{}{}
	}

	suggestions := make(map[string]struct{}, len(parsed.Suggestions))
	for _, id := range parsed.Suggestions {
		if _, exists := findings[id]; !exists {
			return fmt.Errorf("suggestions references unknown finding %s", id)
		}
		if _, exists := suggestions[id]; exists {
			return fmt.Errorf("suggestions repeats finding %s", id)
		}
		if _, blocked := blockers[id]; blocked {
			return fmt.Errorf("finding %s cannot be both a Blocker and a Suggestion", id)
		}
		suggestions[id] = struct{}{}
	}

	for _, finding := range parsed.Findings {
		_, blocked := blockers[finding.ID]
		_, suggested := suggestions[finding.ID]
		switch finding.Severity {
		case SeverityCritical, SeverityMajor:
			if !blocked {
				return fmt.Errorf("%s finding %s must be listed as a Blocker", finding.Severity, finding.ID)
			}
		case SeverityMinor:
			if !suggested {
				return fmt.Errorf("MINOR finding %s must be listed as a Suggestion", finding.ID)
			}
		}
	}
	return nil
}

func validateDemonstratedFindings(findings []Finding) error {
	styleMarkers := []string{
		"asd-ste100",
		"ste100",
		"noun cluster",
		"passive voice",
		"prose violation",
		"documentation style",
	}
	speculationMarkers := []string{
		"any rename",
		"are regenerated",
		"can rename",
		"could rename",
		"future provider",
		"future grammar",
		"future revision",
		"future maintenance",
		"if the grammar ever",
		"if the pinned",
		"if the scanner ever",
		"if the witness no longer",
		"is regenerated",
		"later shifts",
		"linkage-contract",
		"private api surface",
		"private-api surface",
		"signature change",
		"test package must track",
		"could reappear silently",
		"disprove the risk",
		"disproves the risk",
		"no demonstrated failure",
		"hypothetical",
	}

	for _, finding := range findings {
		text := strings.ToLower(strings.Join([]string{
			finding.Title,
			finding.Evidence,
			finding.Impact,
			finding.Fix,
		}, "\n"))
		for _, marker := range styleMarkers {
			if strings.Contains(text, marker) {
				return fmt.Errorf(
					"finding %s reports prose or style (%q), not a demonstrated current defect; move it to Remarks or omit it",
					finding.ID,
					marker,
				)
			}
		}
		for _, marker := range speculationMarkers {
			if strings.Contains(text, marker) {
				return fmt.Errorf(
					"finding %s relies on speculative or self-disproved evidence (%q); move it to Remarks or omit it",
					finding.ID,
					marker,
				)
			}
		}
	}
	return nil
}

func validateFalsificationDisposition(parsed *ParsedReview) error {
	if len(parsed.Findings) == 0 {
		return nil
	}
	if parsed.FalsificationConclusion != FalsificationProved {
		return fmt.Errorf(
			"a non-empty Findings section requires a PROVED falsification conclusion, got %q; move disproved or unresolved concerns to Remarks or omit them",
			parsed.FalsificationConclusion,
		)
	}
	return nil
}

func validateVerdictDisposition(parsed *ParsedReview) error {
	decision, err := parseVerdictDecision(parsed.Verdict)
	if err != nil {
		return err
	}
	if decision != "REQUEST CHANGES" || len(parsed.Blockers) > 0 {
		return nil
	}
	if parsed.FalsificationConclusion == FalsificationProved ||
		parsed.BuildVerification == VerificationFail ||
		parsed.TestVerification == VerificationFail {
		return nil
	}
	for _, feedback := range parsed.FeedbackEntries {
		if feedback.Status == FeedbackUnresolved {
			return nil
		}
	}
	return fmt.Errorf(
		"REQUEST CHANGES requires a blocker or proved current failure; pending or unavailable CI alone requires NEEDS DISCUSSION",
	)
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
	re := regexp.MustCompile("(?mi)^\\s*(?:-\\s+)?\\*\\*Conclusion\\*\\*:\\s*`?(PROVED|DISPROVED|UNRESOLVED)`?(.*)$")
	matches := re.FindAllStringSubmatch(section, -1)
	if len(matches) != 1 || len(matches[0]) < 3 {
		return ""
	}
	rawSuffix := matches[0][2]
	if rawSuffix != "" {
		first, _ := utf8.DecodeRuneInString(rawSuffix)
		if !unicode.IsSpace(first) && !strings.ContainsRune(".—:;,(-[", first) {
			return ""
		}
	}
	suffix := strings.TrimSpace(rawSuffix)
	if regexp.MustCompile(`(?i)\b(PROVED|DISPROVED|UNRESOLVED)\b`).MatchString(suffix) {
		return ""
	}
	return FalsificationConclusion(strings.ToUpper(matches[0][1]))
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
	statusValue := rest
	statusToken, statusRest, ok := parseBacktickToken(statusValue)
	if !ok {
		fields := strings.Fields(statusValue)
		if len(fields) == 0 {
			return FeedbackEntry{}, false
		}
		statusToken = fields[0]
		statusRest = strings.TrimPrefix(statusValue, fields[0])
	}
	status := FeedbackStatus(strings.ToUpper(strings.TrimSpace(statusToken)))
	if status != FeedbackAddressed && status != FeedbackDisputed && status != FeedbackUnresolved {
		return FeedbackEntry{ID: strings.TrimSpace(id)}, true
	}
	return FeedbackEntry{
		ID:       strings.TrimSpace(id),
		Status:   status,
		Evidence: trimCoverageSeparator(statusRest),
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
	re := regexp.MustCompile(`(?m)^\s*(?:-\s*)?\*\*File\*\*:\s*(.+?)\s*$`)
	matches := re.FindStringSubmatch(content)
	if len(matches) < 2 {
		return "", 0
	}

	location := strings.TrimSpace(matches[1])
	if strings.HasPrefix(location, "`") {
		if closing := strings.Index(location[1:], "`"); closing >= 0 {
			closing++
			location = location[1:closing] + strings.TrimSpace(location[closing+1:])
		}
	}

	locationRe := regexp.MustCompile(`^(.+?):(\d+)(?:-\d+)?$`)
	locationMatches := locationRe.FindStringSubmatch(location)
	if len(locationMatches) < 3 {
		return strings.TrimSpace(strings.Trim(location, "`")), 0
	}

	line, err := strconv.Atoi(locationMatches[2])
	if err != nil {
		return strings.TrimSpace(strings.Trim(location, "`")), 0
	}
	return strings.TrimSpace(strings.Trim(locationMatches[1], "`")), line
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
	decision, err := parseVerdictDecision(section)
	if err != nil {
		return false, err
	}
	return decision == "APPROVE" || decision == "YES", nil
}

func parseVerdictDecision(section string) (string, error) {
	matches := verdictDecisionLineRE.FindAllStringSubmatch(section, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf(
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
			return value, nil
		case "NO":
			return value, nil
		default:
			return "", fmt.Errorf("**Approved** must be exactly YES or NO, got %q", matches[0][2])
		}
	case "recommendation":
		switch value {
		case "APPROVE":
			return value, nil
		case "REQUEST CHANGES", "NEEDS DISCUSSION":
			return value, nil
		default:
			return "", fmt.Errorf(
				"**Recommendation** must be exactly APPROVE, REQUEST CHANGES, or NEEDS DISCUSSION, got %q",
				matches[0][2],
			)
		}
	default:
		return "", fmt.Errorf("unsupported Verdict decision field %q", matches[0][1])
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
