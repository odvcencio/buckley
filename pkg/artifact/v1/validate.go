package artifactv1

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxProviderBytes bounds an untrusted provider result before decoding. Full
	// logs, diffs, and traces belong in evidence rather than an artifact body.
	MaxProviderBytes = 256 * 1024

	maxBlocks             = 256
	maxFindings           = 256
	maxDiagnostics        = 256
	maxEvidenceRefs       = 512
	maxNextActions        = 256
	maxTextBytes          = 64 * 1024
	maxSummaryBytes       = 8 * 1024
	maxTitleBytes         = 512
	maxArtifactIDBytes    = 128
	maxMetadataEntries    = 128
	maxChecklistItems     = 256
	maxTableColumns       = 64
	maxTableRows          = 512
	maxOperationMetricSet = 128
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ValidationError is returned by ValidateStrict when an artifact cannot be
// safely persisted or rendered as a conforming v1 artifact.
type ValidationError struct {
	Diagnostics []Diagnostic
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "artifact validation failed"
	}
	if len(e.Diagnostics) == 1 {
		return e.Diagnostics[0].Message
	}
	return fmt.Sprintf("artifact validation failed with %d diagnostics", len(e.Diagnostics))
}

// Validate returns all schema and safety diagnostics without mutating a.
func (a Artifact) Validate() []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if a.SchemaVersion != SchemaVersion {
		diagnostics = append(diagnostics, invalid("schema_version", "schema_version must equal "+SchemaVersion))
	}
	if !validIdentifier(a.ArtifactID) || len(a.ArtifactID) > maxArtifactIDBytes {
		diagnostics = append(diagnostics, invalid("artifact_id", "artifact_id must be a non-empty stable identifier"))
	}
	if !validKind(a.Kind) {
		diagnostics = append(diagnostics, invalid("kind", "kind must be a non-empty lowercase identifier"))
	}
	if !validStatus(a.Status) {
		diagnostics = append(diagnostics, invalid("status", "status must be a supported artifact lifecycle state"))
	}
	if strings.TrimSpace(a.Title) == "" || len(a.Title) > maxTitleBytes {
		diagnostics = append(diagnostics, invalid("title", "title is required and must be bounded"))
	}
	if strings.TrimSpace(a.Summary) == "" || len(a.Summary) > maxSummaryBytes {
		diagnostics = append(diagnostics, invalid("summary", "summary is required and must be bounded"))
	}
	if len(a.Blocks) > maxBlocks {
		diagnostics = append(diagnostics, invalid("blocks", "blocks exceeds the artifact safety limit"))
	}
	if len(a.Findings) > maxFindings {
		diagnostics = append(diagnostics, invalid("findings", "findings exceeds the artifact safety limit"))
	}
	if len(a.Diagnostics) > maxDiagnostics {
		diagnostics = append(diagnostics, invalid("diagnostics", "diagnostics exceeds the artifact safety limit"))
	}
	if len(a.EvidenceRefs) > maxEvidenceRefs {
		diagnostics = append(diagnostics, invalid("evidence_refs", "evidence_refs exceeds the artifact safety limit"))
	}
	if len(a.NextActions) > maxNextActions {
		diagnostics = append(diagnostics, invalid("next_actions", "next_actions exceeds the artifact safety limit"))
	}
	if len(a.Metadata) > maxMetadataEntries {
		diagnostics = append(diagnostics, invalid("metadata", "metadata exceeds the artifact safety limit"))
	}

	for index, block := range a.Blocks {
		diagnostics = append(diagnostics, validateBlock(block, index)...)
	}
	for index, finding := range a.Findings {
		diagnostics = append(diagnostics, validateFinding(finding, fmt.Sprintf("findings[%d]", index))...)
	}
	for index, diagnostic := range a.Diagnostics {
		diagnostics = append(diagnostics, validateDiagnostic(diagnostic, fmt.Sprintf("diagnostics[%d]", index))...)
	}
	for index, evidence := range a.EvidenceRefs {
		diagnostics = append(diagnostics, validateEvidence(evidence, fmt.Sprintf("evidence_refs[%d]", index))...)
	}
	for index, action := range a.NextActions {
		diagnostics = append(diagnostics, validateAction(action, fmt.Sprintf("next_actions[%d]", index))...)
	}
	for index, reason := range a.IncompleteReasons {
		if strings.TrimSpace(reason) == "" {
			diagnostics = append(diagnostics, invalid(fmt.Sprintf("incomplete_reasons[%d]", index), "incomplete reason must not be empty"))
		}
	}
	for key, value := range a.Metadata {
		if strings.TrimSpace(key) == "" || len(key) > maxTitleBytes {
			diagnostics = append(diagnostics, invalid("metadata", "metadata keys must be non-empty and bounded"))
			break
		}
		if len(value) > maxTextBytes {
			diagnostics = append(diagnostics, invalid("metadata", "metadata values must be bounded"))
			break
		}
	}
	return diagnostics
}

// ValidateStrict reports a single error carrying every validation diagnostic.
func (a Artifact) ValidateStrict() error {
	if diagnostics := a.Validate(); len(diagnostics) > 0 {
		return &ValidationError{Diagnostics: diagnostics}
	}
	return nil
}

// NormalizeAndValidate is the safe handoff used by adapters that accept
// omission-only provider repairs before persisting an artifact.
func NormalizeAndValidate(a Artifact) (Artifact, error) {
	a = a.Normalized()
	if err := a.ValidateStrict(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func validateBlock(block Block, index int) []Diagnostic {
	prefix := fmt.Sprintf("blocks[%d]", index)
	diagnostics := make([]Diagnostic, 0)
	if !validBlockKind(block.Kind) {
		return []Diagnostic{invalid(prefix+".kind", "block kind is unsupported")}
	}
	if len(block.Text) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(prefix+".text", "block text must be bounded"))
	}
	if len(block.Checklist) > maxChecklistItems {
		diagnostics = append(diagnostics, invalid(prefix+".checklist", "checklist exceeds the artifact safety limit"))
	}

	switch block.Kind {
	case BlockProse:
		if strings.TrimSpace(block.Text) == "" {
			diagnostics = append(diagnostics, invalid(prefix+".text", "prose block requires text"))
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "text")...)
	case BlockHeading:
		if strings.TrimSpace(block.Text) == "" {
			diagnostics = append(diagnostics, invalid(prefix+".text", "heading block requires text"))
		}
		if block.Level < 1 || block.Level > 6 {
			diagnostics = append(diagnostics, invalid(prefix+".level", "heading level must be between 1 and 6"))
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "text", "level")...)
	case BlockFacts:
		if len(block.Facts) == 0 {
			diagnostics = append(diagnostics, invalid(prefix+".facts", "facts block requires at least one fact"))
		}
		for factIndex, fact := range block.Facts {
			diagnostics = append(diagnostics, validateFact(fact, fmt.Sprintf("%s.facts[%d]", prefix, factIndex))...)
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "facts")...)
	case BlockTable:
		if block.Table == nil {
			diagnostics = append(diagnostics, invalid(prefix+".table", "table block requires table payload"))
		} else {
			diagnostics = append(diagnostics, validateTable(*block.Table, prefix+".table")...)
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "table")...)
	case BlockCode:
		if block.Code == nil || strings.TrimSpace(block.Code.Content) == "" {
			diagnostics = append(diagnostics, invalid(prefix+".code", "code block requires content"))
		} else if len(block.Code.Content) > maxTextBytes {
			diagnostics = append(diagnostics, invalid(prefix+".code", "code content must be bounded"))
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "code")...)
	case BlockDiff:
		if block.Diff == nil || strings.TrimSpace(block.Diff.Content) == "" {
			diagnostics = append(diagnostics, invalid(prefix+".diff", "diff block requires content"))
		} else if len(block.Diff.Content) > maxTextBytes {
			diagnostics = append(diagnostics, invalid(prefix+".diff", "diff content must be bounded"))
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "diff")...)
	case BlockChecklist:
		if len(block.Checklist) == 0 {
			diagnostics = append(diagnostics, invalid(prefix+".checklist", "checklist block requires at least one item"))
		}
		for itemIndex, item := range block.Checklist {
			if strings.TrimSpace(item.Text) == "" {
				diagnostics = append(diagnostics, invalid(fmt.Sprintf("%s.checklist[%d].text", prefix, itemIndex), "checklist item text is required"))
			}
			if len(item.Text) > maxTextBytes || len(item.Detail) > maxTextBytes {
				diagnostics = append(diagnostics, invalid(fmt.Sprintf("%s.checklist[%d]", prefix, itemIndex), "checklist item content must be bounded"))
			}
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "checklist")...)
	case BlockFinding:
		if block.Finding == nil {
			diagnostics = append(diagnostics, invalid(prefix+".finding", "finding block requires finding payload"))
		} else {
			diagnostics = append(diagnostics, validateFinding(*block.Finding, prefix+".finding")...)
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "finding")...)
	case BlockOperationSummary:
		if block.Operation == nil {
			diagnostics = append(diagnostics, invalid(prefix+".operation", "operation summary block requires operation payload"))
		} else {
			diagnostics = append(diagnostics, validateOperation(*block.Operation, prefix+".operation")...)
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "operation")...)
	case BlockEvidenceLink:
		if block.Evidence == nil {
			diagnostics = append(diagnostics, invalid(prefix+".evidence", "evidence link block requires evidence payload"))
		} else {
			diagnostics = append(diagnostics, validateEvidence(*block.Evidence, prefix+".evidence")...)
		}
		diagnostics = append(diagnostics, requireNoBlockPayload(block, prefix, "evidence")...)
	}
	return diagnostics
}

func requireNoBlockPayload(block Block, prefix string, allowed ...string) []Diagnostic {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	populated := blockPayloadFields(block)
	diagnostics := make([]Diagnostic, 0)
	for _, field := range populated {
		if _, ok := allowedSet[field]; !ok {
			diagnostics = append(diagnostics, invalid(prefix+"."+field, "block payload does not match its kind"))
		}
	}
	return diagnostics
}

func blockPayloadFields(block Block) []string {
	fields := make([]string, 0, 8)
	if block.Text != "" {
		fields = append(fields, "text")
	}
	if block.Level != 0 {
		fields = append(fields, "level")
	}
	if len(block.Facts) > 0 {
		fields = append(fields, "facts")
	}
	if block.Table != nil {
		fields = append(fields, "table")
	}
	if block.Code != nil {
		fields = append(fields, "code")
	}
	if block.Diff != nil {
		fields = append(fields, "diff")
	}
	if len(block.Checklist) > 0 {
		fields = append(fields, "checklist")
	}
	if block.Finding != nil {
		fields = append(fields, "finding")
	}
	if block.Operation != nil {
		fields = append(fields, "operation")
	}
	if block.Evidence != nil {
		fields = append(fields, "evidence")
	}
	return fields
}

func validateFact(fact Fact, path string) []Diagnostic {
	if strings.TrimSpace(fact.Label) == "" || strings.TrimSpace(fact.Value) == "" {
		return []Diagnostic{invalid(path, "fact label and value are required")}
	}
	if len(fact.Label) > maxTitleBytes || len(fact.Value) > maxTextBytes {
		return []Diagnostic{invalid(path, "fact label and value must be bounded")}
	}
	return nil
}

func validateTable(table Table, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if len(table.Headers) == 0 || len(table.Headers) > maxTableColumns {
		diagnostics = append(diagnostics, invalid(path+".headers", "table requires bounded headers"))
	}
	if len(table.Rows) > maxTableRows {
		diagnostics = append(diagnostics, invalid(path+".rows", "table rows exceed the artifact safety limit"))
	}
	for index, header := range table.Headers {
		if strings.TrimSpace(header) == "" || len(header) > maxTitleBytes {
			diagnostics = append(diagnostics, invalid(fmt.Sprintf("%s.headers[%d]", path, index), "table headers must be non-empty and bounded"))
		}
	}
	for rowIndex, row := range table.Rows {
		if len(row) != len(table.Headers) {
			diagnostics = append(diagnostics, invalid(fmt.Sprintf("%s.rows[%d]", path, rowIndex), "table row width must match headers"))
		}
		for columnIndex, value := range row {
			if len(value) > maxTextBytes {
				diagnostics = append(diagnostics, invalid(fmt.Sprintf("%s.rows[%d][%d]", path, rowIndex, columnIndex), "table cell must be bounded"))
			}
		}
	}
	return diagnostics
}

func validateFinding(finding Finding, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if !validIdentifier(finding.ID) {
		diagnostics = append(diagnostics, invalid(path+".id", "finding id must be a non-empty stable identifier"))
	}
	if !validSeverity(finding.Severity) {
		diagnostics = append(diagnostics, invalid(path+".severity", "finding severity must be critical, high, medium, low, or info"))
	}
	if finding.Confidence < 0 || finding.Confidence > 1 {
		diagnostics = append(diagnostics, invalid(path+".confidence", "finding confidence must be between 0 and 1"))
	}
	if strings.TrimSpace(finding.Title) == "" || len(finding.Title) > maxTitleBytes {
		diagnostics = append(diagnostics, invalid(path+".title", "finding title is required and must be bounded"))
	}
	if strings.TrimSpace(finding.Summary) == "" || len(finding.Summary) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(path+".summary", "finding summary is required and must be bounded"))
	}
	if len(finding.Recommendation) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(path+".recommendation", "finding recommendation must be bounded"))
	}
	if finding.Location != nil {
		diagnostics = append(diagnostics, validateLocation(*finding.Location, path+".location")...)
	}
	for index, evidence := range finding.EvidenceRefs {
		diagnostics = append(diagnostics, validateEvidence(evidence, fmt.Sprintf("%s.evidence_refs[%d]", path, index))...)
	}
	return diagnostics
}

func validateDiagnostic(diagnostic Diagnostic, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if !validDiagnosticLevel(diagnostic.Level) {
		diagnostics = append(diagnostics, invalid(path+".level", "diagnostic level must be error, warning, or info"))
	}
	if strings.TrimSpace(diagnostic.Message) == "" || len(diagnostic.Message) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(path+".message", "diagnostic message is required and must be bounded"))
	}
	if len(diagnostic.Code) > maxTitleBytes {
		diagnostics = append(diagnostics, invalid(path+".code", "diagnostic code must be bounded"))
	}
	if diagnostic.Location != nil {
		diagnostics = append(diagnostics, validateLocation(*diagnostic.Location, path+".location")...)
	}
	return diagnostics
}

func validateLocation(location Location, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if strings.TrimSpace(location.Path) == "" && strings.TrimSpace(location.Symbol) == "" {
		diagnostics = append(diagnostics, invalid(path, "location requires a path or symbol"))
	}
	if location.StartLine < 0 || location.EndLine < 0 {
		diagnostics = append(diagnostics, invalid(path, "location line numbers cannot be negative"))
	}
	if location.EndLine > 0 && location.StartLine > 0 && location.EndLine < location.StartLine {
		diagnostics = append(diagnostics, invalid(path, "location end_line cannot precede start_line"))
	}
	return diagnostics
}

func validateEvidence(evidence EvidenceRef, path string) []Diagnostic {
	if !validIdentifier(evidence.ID) {
		return []Diagnostic{invalid(path+".id", "evidence id must be a non-empty stable identifier")}
	}
	if len(evidence.Label) > maxTitleBytes || len(evidence.Kind) > maxTitleBytes || len(evidence.URI) > maxTextBytes {
		return []Diagnostic{invalid(path, "evidence reference fields must be bounded")}
	}
	return nil
}

func validateAction(action NextAction, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if strings.TrimSpace(action.Description) == "" || len(action.Description) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(path+".description", "next action description is required and must be bounded"))
	}
	if action.ID != "" && !validIdentifier(action.ID) {
		diagnostics = append(diagnostics, invalid(path+".id", "next action id must be a stable identifier when supplied"))
	}
	for index, evidence := range action.EvidenceRefs {
		diagnostics = append(diagnostics, validateEvidence(evidence, fmt.Sprintf("%s.evidence_refs[%d]", path, index))...)
	}
	return diagnostics
}

func validateOperation(operation OperationSummary, path string) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if strings.TrimSpace(operation.Operation) == "" {
		diagnostics = append(diagnostics, invalid(path+".operation", "operation name is required"))
	}
	if strings.TrimSpace(operation.Status) == "" {
		diagnostics = append(diagnostics, invalid(path+".status", "operation status is required"))
	}
	if operation.DurationMS < 0 {
		diagnostics = append(diagnostics, invalid(path+".duration_ms", "operation duration cannot be negative"))
	}
	if len(operation.Detail) > maxTextBytes {
		diagnostics = append(diagnostics, invalid(path+".detail", "operation detail must be bounded"))
	}
	if len(operation.Metrics) > maxOperationMetricSet {
		diagnostics = append(diagnostics, invalid(path+".metrics", "operation metrics exceed the artifact safety limit"))
	}
	for index, metric := range operation.Metrics {
		diagnostics = append(diagnostics, validateFact(metric, fmt.Sprintf("%s.metrics[%d]", path, index))...)
	}
	for index, evidence := range operation.EvidenceRefs {
		diagnostics = append(diagnostics, validateEvidence(evidence, fmt.Sprintf("%s.evidence_refs[%d]", path, index))...)
	}
	return diagnostics
}

func validIdentifier(value string) bool {
	return value != "" && identifierPattern.MatchString(value)
}

func validKind(kind ArtifactKind) bool {
	value := string(kind)
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	return identifierPattern.MatchString(value)
}

func validStatus(status ArtifactStatus) bool {
	switch status {
	case StatusDraft, StatusInProgress, StatusCompleted, StatusFailed, StatusBlocked, StatusIncomplete:
		return true
	default:
		return false
	}
}

func validBlockKind(kind BlockKind) bool {
	switch kind {
	case BlockProse, BlockHeading, BlockFacts, BlockTable, BlockCode, BlockDiff, BlockChecklist, BlockFinding, BlockOperationSummary, BlockEvidenceLink:
		return true
	default:
		return false
	}
}

func validSeverity(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high", "medium", "low", "info":
		return true
	default:
		return false
	}
}

func validDiagnosticLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "warning", "info":
		return true
	default:
		return false
	}
}

func invalid(code, message string) Diagnostic {
	return Diagnostic{Level: "error", Code: "artifact." + code, Message: message}
}

// StableDiagnostics returns a copy ordered by code and message. It is useful
// for assertions and repair prompts that must be replayable across runs.
func StableDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	copyDiagnostics := append([]Diagnostic(nil), diagnostics...)
	sort.SliceStable(copyDiagnostics, func(i, j int) bool {
		if copyDiagnostics[i].Code != copyDiagnostics[j].Code {
			return copyDiagnostics[i].Code < copyDiagnostics[j].Code
		}
		return copyDiagnostics[i].Message < copyDiagnostics[j].Message
	})
	return copyDiagnostics
}
