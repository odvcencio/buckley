// Package artifactv1 defines the versioned, provider-neutral result contract
// shared by Buckley's agents, terminal surfaces, ACP projections, and durable
// evidence. The package deliberately owns data and deterministic projections,
// not transport or UI widgets, so adapters can evolve independently.
package artifactv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const (
	// SchemaVersion is the immutable wire-contract identifier for Artifact.
	SchemaVersion = "buckley.artifact/v1"
	// MediaType is the evidence and transport content type for a v1 artifact.
	MediaType = "application/vnd.buckley.artifact+json"
)

// ArtifactKind describes the purpose of a completed or in-progress artifact.
// Values are intentionally open for forward-compatible domain-specific kinds.
type ArtifactKind string

const (
	KindAnalysis        ArtifactKind = "analysis"
	KindPlan            ArtifactKind = "plan"
	KindExecution       ArtifactKind = "execution"
	KindReview          ArtifactKind = "review"
	KindGoal            ArtifactKind = "goal"
	KindSubagentResult  ArtifactKind = "subagent_result"
	KindOperationReport ArtifactKind = "operation_report"
)

// ArtifactStatus is the lifecycle state represented by an artifact.
type ArtifactStatus string

const (
	StatusDraft      ArtifactStatus = "draft"
	StatusInProgress ArtifactStatus = "in_progress"
	StatusCompleted  ArtifactStatus = "completed"
	StatusFailed     ArtifactStatus = "failed"
	StatusBlocked    ArtifactStatus = "blocked"
	StatusIncomplete ArtifactStatus = "incomplete"
)

// BlockKind identifies one typed unit in an artifact body.
type BlockKind string

const (
	BlockProse            BlockKind = "prose"
	BlockHeading          BlockKind = "heading"
	BlockFacts            BlockKind = "facts"
	BlockTable            BlockKind = "table"
	BlockCode             BlockKind = "code"
	BlockDiff             BlockKind = "diff"
	BlockChecklist        BlockKind = "checklist"
	BlockFinding          BlockKind = "finding"
	BlockOperationSummary BlockKind = "operation_summary"
	BlockEvidenceLink     BlockKind = "evidence_link"
)

// Artifact is Buckley's canonical structured result. Artifact instances are
// immutable once persisted; New and Normalized derive a deterministic ID when
// callers do not provide one, which makes replay comparison straightforward.
type Artifact struct {
	SchemaVersion     string            `json:"schema_version"`
	ArtifactID        string            `json:"artifact_id"`
	Kind              ArtifactKind      `json:"kind"`
	Status            ArtifactStatus    `json:"status"`
	Title             string            `json:"title"`
	Summary           string            `json:"summary"`
	Blocks            []Block           `json:"blocks,omitempty"`
	Findings          []Finding         `json:"findings,omitempty"`
	Diagnostics       []Diagnostic      `json:"diagnostics,omitempty"`
	EvidenceRefs      []EvidenceRef     `json:"evidence_refs,omitempty"`
	NextActions       []NextAction      `json:"next_actions,omitempty"`
	IncompleteReasons []string          `json:"incomplete_reasons,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// Block carries exactly one payload matching Kind. Keeping payloads explicit
// avoids stringly-typed rendering branches while remaining simple for models
// to emit through native JSON schema or submit_artifact.
type Block struct {
	Kind      BlockKind         `json:"kind"`
	Text      string            `json:"text,omitempty"`
	Level     int               `json:"level,omitempty"`
	Facts     []Fact            `json:"facts,omitempty"`
	Table     *Table            `json:"table,omitempty"`
	Code      *CodeBlock        `json:"code,omitempty"`
	Diff      *DiffBlock        `json:"diff,omitempty"`
	Checklist []ChecklistItem   `json:"checklist,omitempty"`
	Finding   *Finding          `json:"finding,omitempty"`
	Operation *OperationSummary `json:"operation,omitempty"`
	Evidence  *EvidenceLink     `json:"evidence,omitempty"`
}

// Fact is one labeled scalar suitable for a compact facts block.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Table is a rectangular plain-text table. Headers are required and every row
// has exactly the same width so terminal and Markdown renderers agree.
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows,omitempty"`
}

// CodeBlock preserves a code excerpt without asking renderers to infer a
// language from its content.
type CodeBlock struct {
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
}

// DiffBlock preserves a unified diff and, when known, its workspace-relative
// source path.
type DiffBlock struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content"`
}

// ChecklistItem is a reviewable action item. State values are descriptive so
// adapters can retain provider distinctions without losing renderability.
type ChecklistItem struct {
	Text   string `json:"text"`
	State  string `json:"state,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// OperationSummary records a bounded operation result without embedding raw
// logs. Full bodies belong in EvidenceRefs.
type OperationSummary struct {
	Operation    string        `json:"operation"`
	Status       string        `json:"status"`
	DurationMS   int64         `json:"duration_ms,omitempty"`
	Detail       string        `json:"detail,omitempty"`
	Metrics      []Fact        `json:"metrics,omitempty"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
}

// Finding is a structured observation that can be rendered as review text,
// diagnostics, or SARIF without reparsing prose.
type Finding struct {
	ID             string        `json:"id"`
	Severity       string        `json:"severity"`
	Confidence     float64       `json:"confidence"`
	Title          string        `json:"title"`
	Summary        string        `json:"summary"`
	Location       *Location     `json:"location,omitempty"`
	Recommendation string        `json:"recommendation,omitempty"`
	EvidenceRefs   []EvidenceRef `json:"evidence_refs,omitempty"`
}

// Location points at a workspace artifact without requiring a particular LSP
// or source-control provider.
type Location struct {
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
}

// Diagnostic captures validation, tool, or renderer observations. It remains
// separate from Findings because diagnostics explain harness behavior while
// findings describe the task subject.
type Diagnostic struct {
	Level    string    `json:"level"`
	Code     string    `json:"code,omitempty"`
	Message  string    `json:"message"`
	Location *Location `json:"location,omitempty"`
}

// EvidenceRef names a replayable evidence object, optionally with a human
// label and URL-like locator supplied by an adapter.
type EvidenceRef struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind,omitempty"`
	URI   string `json:"uri,omitempty"`
}

// EvidenceLink is the block-local spelling of EvidenceRef. Keeping the alias
// documents the block's intent without creating a second wire representation.
type EvidenceLink = EvidenceRef

// NextAction is an explicit, ordered follow-up. The slice order carries the
// priority supplied by the producer.
type NextAction struct {
	ID           string        `json:"id,omitempty"`
	Description  string        `json:"description"`
	Priority     string        `json:"priority,omitempty"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs,omitempty"`
}

// New creates a normalized artifact with a stable content-derived ID. The
// returned artifact is valid when kind, title, and summary are non-empty.
func New(kind ArtifactKind, status ArtifactStatus, title, summary string) Artifact {
	return Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          kind,
		Status:        status,
		Title:         title,
		Summary:       summary,
	}.Normalized()
}

// Normalized applies safe presentation normalization and fills omission-only
// defaults. It never overwrites a supplied schema version, identity, status,
// or semantic value, so callers can still validate incompatible input.
func (a Artifact) Normalized() Artifact {
	a = cloneArtifact(a)
	a.SchemaVersion = strings.TrimSpace(a.SchemaVersion)
	if a.SchemaVersion == "" {
		a.SchemaVersion = SchemaVersion
	}
	a.ArtifactID = strings.TrimSpace(a.ArtifactID)
	a.Kind = ArtifactKind(strings.TrimSpace(string(a.Kind)))
	if a.Kind == "" {
		a.Kind = KindAnalysis
	}
	a.Status = ArtifactStatus(strings.TrimSpace(string(a.Status)))
	if a.Status == "" {
		a.Status = StatusIncomplete
	}
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		a.Title = "Untitled artifact"
	}
	a.Summary = strings.TrimSpace(a.Summary)
	if a.Summary == "" {
		a.Summary = summaryFromBlocks(a.Blocks)
	}
	if a.Summary == "" {
		a.Summary = a.Title
	}
	for i := range a.Blocks {
		a.Blocks[i] = normalizeBlock(a.Blocks[i])
	}
	for i := range a.Findings {
		a.Findings[i] = normalizeFinding(a.Findings[i])
	}
	for i := range a.Diagnostics {
		a.Diagnostics[i] = normalizeDiagnostic(a.Diagnostics[i])
	}
	for i := range a.EvidenceRefs {
		a.EvidenceRefs[i] = normalizeEvidence(a.EvidenceRefs[i])
	}
	for i := range a.NextActions {
		a.NextActions[i] = normalizeAction(a.NextActions[i])
	}
	for i := range a.IncompleteReasons {
		a.IncompleteReasons[i] = strings.TrimSpace(a.IncompleteReasons[i])
	}
	if a.Metadata != nil {
		metadata := make(map[string]string, len(a.Metadata))
		for key, value := range a.Metadata {
			key = strings.TrimSpace(key)
			if key != "" {
				metadata[key] = strings.TrimSpace(value)
			}
		}
		a.Metadata = metadata
	}
	if a.ArtifactID == "" {
		a.ArtifactID = deriveArtifactID(a)
	}
	return a
}

func normalizeBlock(block Block) Block {
	block.Kind = BlockKind(strings.TrimSpace(string(block.Kind)))
	block.Text = strings.TrimSpace(block.Text)
	if block.Kind == BlockHeading && block.Level == 0 {
		block.Level = 2
	}
	for i := range block.Facts {
		block.Facts[i] = normalizeFact(block.Facts[i])
	}
	if block.Table != nil {
		for i := range block.Table.Headers {
			block.Table.Headers[i] = strings.TrimSpace(block.Table.Headers[i])
		}
		for row := range block.Table.Rows {
			for column := range block.Table.Rows[row] {
				block.Table.Rows[row][column] = strings.TrimSpace(block.Table.Rows[row][column])
			}
		}
	}
	if block.Code != nil {
		block.Code.Language = strings.TrimSpace(block.Code.Language)
		block.Code.Content = strings.TrimRight(block.Code.Content, "\n")
	}
	if block.Diff != nil {
		block.Diff.Path = strings.TrimSpace(block.Diff.Path)
		block.Diff.Content = strings.TrimRight(block.Diff.Content, "\n")
	}
	for i := range block.Checklist {
		block.Checklist[i].Text = strings.TrimSpace(block.Checklist[i].Text)
		block.Checklist[i].State = strings.TrimSpace(block.Checklist[i].State)
		block.Checklist[i].Detail = strings.TrimSpace(block.Checklist[i].Detail)
	}
	if block.Finding != nil {
		found := normalizeFinding(*block.Finding)
		block.Finding = &found
	}
	if block.Operation != nil {
		block.Operation.Operation = strings.TrimSpace(block.Operation.Operation)
		block.Operation.Status = strings.TrimSpace(block.Operation.Status)
		block.Operation.Detail = strings.TrimSpace(block.Operation.Detail)
		for i := range block.Operation.Metrics {
			block.Operation.Metrics[i] = normalizeFact(block.Operation.Metrics[i])
		}
		for i := range block.Operation.EvidenceRefs {
			block.Operation.EvidenceRefs[i] = normalizeEvidence(block.Operation.EvidenceRefs[i])
		}
	}
	if block.Evidence != nil {
		evidence := normalizeEvidence(*block.Evidence)
		block.Evidence = &evidence
	}
	return block
}

func normalizeFact(fact Fact) Fact {
	fact.Label = strings.TrimSpace(fact.Label)
	fact.Value = strings.TrimSpace(fact.Value)
	return fact
}

func normalizeFinding(finding Finding) Finding {
	finding.ID = strings.TrimSpace(finding.ID)
	finding.Severity = strings.ToLower(strings.TrimSpace(finding.Severity))
	finding.Title = strings.TrimSpace(finding.Title)
	finding.Summary = strings.TrimSpace(finding.Summary)
	finding.Recommendation = strings.TrimSpace(finding.Recommendation)
	if finding.Location != nil {
		location := normalizeLocation(*finding.Location)
		finding.Location = &location
	}
	for i := range finding.EvidenceRefs {
		finding.EvidenceRefs[i] = normalizeEvidence(finding.EvidenceRefs[i])
	}
	return finding
}

func normalizeDiagnostic(diagnostic Diagnostic) Diagnostic {
	diagnostic.Level = strings.ToLower(strings.TrimSpace(diagnostic.Level))
	diagnostic.Code = strings.TrimSpace(diagnostic.Code)
	diagnostic.Message = strings.TrimSpace(diagnostic.Message)
	if diagnostic.Location != nil {
		location := normalizeLocation(*diagnostic.Location)
		diagnostic.Location = &location
	}
	return diagnostic
}

func normalizeLocation(location Location) Location {
	location.Path = strings.TrimSpace(location.Path)
	location.Symbol = strings.TrimSpace(location.Symbol)
	return location
}

func normalizeEvidence(evidence EvidenceRef) EvidenceRef {
	evidence.ID = strings.TrimSpace(evidence.ID)
	evidence.Label = strings.TrimSpace(evidence.Label)
	evidence.Kind = strings.TrimSpace(evidence.Kind)
	evidence.URI = strings.TrimSpace(evidence.URI)
	return evidence
}

func normalizeAction(action NextAction) NextAction {
	action.ID = strings.TrimSpace(action.ID)
	action.Description = strings.TrimSpace(action.Description)
	action.Priority = strings.TrimSpace(action.Priority)
	for i := range action.EvidenceRefs {
		action.EvidenceRefs[i] = normalizeEvidence(action.EvidenceRefs[i])
	}
	return action
}

func summaryFromBlocks(blocks []Block) string {
	for _, block := range blocks {
		if block.Kind == BlockProse && strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}

func deriveArtifactID(a Artifact) string {
	a.ArtifactID = ""
	payload, err := json.Marshal(a)
	if err != nil {
		return "art_unavailable"
	}
	digest := sha256.Sum256(payload)
	return "art_" + hex.EncodeToString(digest[:10])
}

func cloneArtifact(a Artifact) Artifact {
	a.Blocks = append([]Block(nil), a.Blocks...)
	for index := range a.Blocks {
		a.Blocks[index] = cloneBlock(a.Blocks[index])
	}
	a.Findings = cloneFindings(a.Findings)
	a.Diagnostics = cloneDiagnostics(a.Diagnostics)
	a.EvidenceRefs = cloneEvidence(a.EvidenceRefs)
	a.NextActions = cloneActions(a.NextActions)
	a.IncompleteReasons = append([]string(nil), a.IncompleteReasons...)
	if a.Metadata != nil {
		a.Metadata = make(map[string]string, len(a.Metadata))
		for key, value := range a.Metadata {
			a.Metadata[key] = value
		}
	}
	return a
}

func cloneBlock(block Block) Block {
	block.Facts = append([]Fact(nil), block.Facts...)
	if block.Table != nil {
		table := *block.Table
		table.Headers = append([]string(nil), table.Headers...)
		rows := table.Rows
		table.Rows = make([][]string, len(rows))
		for index := range rows {
			table.Rows[index] = append([]string(nil), rows[index]...)
		}
		block.Table = &table
	}
	if block.Code != nil {
		code := *block.Code
		block.Code = &code
	}
	if block.Diff != nil {
		diff := *block.Diff
		block.Diff = &diff
	}
	block.Checklist = append([]ChecklistItem(nil), block.Checklist...)
	if block.Finding != nil {
		finding := cloneFinding(*block.Finding)
		block.Finding = &finding
	}
	if block.Operation != nil {
		operation := *block.Operation
		operation.Metrics = append([]Fact(nil), operation.Metrics...)
		operation.EvidenceRefs = cloneEvidence(operation.EvidenceRefs)
		block.Operation = &operation
	}
	if block.Evidence != nil {
		evidence := *block.Evidence
		block.Evidence = &evidence
	}
	return block
}

func cloneFindings(findings []Finding) []Finding {
	cloned := append([]Finding(nil), findings...)
	for index := range cloned {
		cloned[index] = cloneFinding(cloned[index])
	}
	return cloned
}

func cloneFinding(finding Finding) Finding {
	if finding.Location != nil {
		location := *finding.Location
		finding.Location = &location
	}
	finding.EvidenceRefs = cloneEvidence(finding.EvidenceRefs)
	return finding
}

func cloneDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	cloned := append([]Diagnostic(nil), diagnostics...)
	for index := range cloned {
		if cloned[index].Location != nil {
			location := *cloned[index].Location
			cloned[index].Location = &location
		}
	}
	return cloned
}

func cloneEvidence(evidence []EvidenceRef) []EvidenceRef {
	return append([]EvidenceRef(nil), evidence...)
}

func cloneActions(actions []NextAction) []NextAction {
	cloned := append([]NextAction(nil), actions...)
	for index := range cloned {
		cloned[index].EvidenceRefs = cloneEvidence(cloned[index].EvidenceRefs)
	}
	return cloned
}
