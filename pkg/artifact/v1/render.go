package artifactv1

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/mdpp"
)

// FluffyUIProjection is a dependency-free view model for a retained FluffyUI
// surface. The UI adapter owns colors, wrapping, focus, and key bindings;
// this projection guarantees that it receives the same typed content as every
// other output surface.
type FluffyUIProjection struct {
	SchemaVersion     string         `json:"schema_version"`
	ArtifactID        string         `json:"artifact_id"`
	Kind              ArtifactKind   `json:"kind"`
	Status            ArtifactStatus `json:"status"`
	Title             string         `json:"title"`
	Summary           string         `json:"summary"`
	Blocks            []Block        `json:"blocks,omitempty"`
	Findings          []Finding      `json:"findings,omitempty"`
	Diagnostics       []Diagnostic   `json:"diagnostics,omitempty"`
	EvidenceRefs      []EvidenceRef  `json:"evidence_refs,omitempty"`
	NextActions       []NextAction   `json:"next_actions,omitempty"`
	IncompleteReasons []string       `json:"incomplete_reasons,omitempty"`
	Markdown          string         `json:"markdown"`
}

// ACPProjection is a transport-neutral ACP-compatible content payload. ACP
// adapters can place Markdown in a content block and Artifact in structured
// metadata without reinterpreting a model's prose.
type ACPProjection struct {
	Type     string   `json:"type"`
	Text     string   `json:"text"`
	Artifact Artifact `json:"artifact"`
}

// RenderTerminal renders a compact, ANSI-free presentation suitable for
// terminals, logs, and narrow TUI panes. Color and width decisions remain in
// the presentation adapter so recordings stay deterministic.
func RenderTerminal(artifact Artifact) (string, error) {
	if err := artifact.ValidateStrict(); err != nil {
		return "", err
	}
	var output strings.Builder
	fmt.Fprintf(&output, "%s [%s · %s]\n", artifact.Title, artifact.Status, artifact.Kind)
	output.WriteString(artifact.Summary)

	for _, block := range artifact.Blocks {
		output.WriteString("\n\n")
		writeTerminalBlock(&output, block)
	}
	if len(artifact.Findings) > 0 {
		output.WriteString("\n\nFindings\n")
		for _, finding := range artifact.Findings {
			writeTerminalFinding(&output, finding, "- ")
		}
	}
	if len(artifact.Diagnostics) > 0 {
		output.WriteString("\nDiagnostics\n")
		for _, diagnostic := range artifact.Diagnostics {
			fmt.Fprintf(&output, "- [%s] %s", strings.ToUpper(diagnostic.Level), diagnostic.Message)
			if diagnostic.Code != "" {
				fmt.Fprintf(&output, " (%s)", diagnostic.Code)
			}
			if rendered := renderLocation(diagnostic.Location); rendered != "" {
				fmt.Fprintf(&output, " @ %s", rendered)
			}
			output.WriteByte('\n')
		}
	}
	if len(artifact.EvidenceRefs) > 0 {
		output.WriteString("\nEvidence\n")
		for _, evidence := range artifact.EvidenceRefs {
			fmt.Fprintf(&output, "- %s", evidence.ID)
			if evidence.Label != "" {
				fmt.Fprintf(&output, ": %s", evidence.Label)
			}
			output.WriteByte('\n')
		}
	}
	if len(artifact.NextActions) > 0 {
		output.WriteString("\nNext actions\n")
		for index, action := range artifact.NextActions {
			fmt.Fprintf(&output, "%d. %s", index+1, action.Description)
			if action.Priority != "" {
				fmt.Fprintf(&output, " [%s]", action.Priority)
			}
			output.WriteByte('\n')
		}
	}
	if len(artifact.IncompleteReasons) > 0 {
		output.WriteString("\nIncomplete\n")
		for _, reason := range artifact.IncompleteReasons {
			fmt.Fprintf(&output, "- %s\n", reason)
		}
	}
	return strings.TrimRight(output.String(), "\n"), nil
}

func writeTerminalBlock(output *strings.Builder, block Block) {
	switch block.Kind {
	case BlockProse:
		output.WriteString(block.Text)
	case BlockHeading:
		output.WriteString(strings.ToUpper(block.Text))
	case BlockFacts:
		for _, fact := range block.Facts {
			fmt.Fprintf(output, "%s: %s\n", fact.Label, fact.Value)
		}
	case BlockTable:
		writeTerminalTable(output, *block.Table)
	case BlockCode:
		if block.Code.Language != "" {
			fmt.Fprintf(output, "[%s]\n", block.Code.Language)
		}
		output.WriteString(block.Code.Content)
	case BlockDiff:
		if block.Diff.Path != "" {
			fmt.Fprintf(output, "Diff: %s\n", block.Diff.Path)
		}
		output.WriteString(block.Diff.Content)
	case BlockChecklist:
		for _, item := range block.Checklist {
			check := " "
			if checklistDone(item.State) {
				check = "x"
			}
			fmt.Fprintf(output, "- [%s] %s", check, item.Text)
			if item.Detail != "" {
				fmt.Fprintf(output, " — %s", item.Detail)
			}
			output.WriteByte('\n')
		}
	case BlockFinding:
		writeTerminalFinding(output, *block.Finding, "")
	case BlockOperationSummary:
		operation := block.Operation
		fmt.Fprintf(output, "%s: %s", operation.Operation, operation.Status)
		if operation.DurationMS > 0 {
			fmt.Fprintf(output, " (%d ms)", operation.DurationMS)
		}
		if operation.Detail != "" {
			fmt.Fprintf(output, "\n%s", operation.Detail)
		}
		for _, metric := range operation.Metrics {
			fmt.Fprintf(output, "\n%s: %s", metric.Label, metric.Value)
		}
	case BlockEvidenceLink:
		fmt.Fprintf(output, "Evidence: %s", block.Evidence.ID)
		if block.Evidence.Label != "" {
			fmt.Fprintf(output, " — %s", block.Evidence.Label)
		}
	}
}

func writeTerminalTable(output *strings.Builder, table Table) {
	output.WriteString(strings.Join(table.Headers, " | "))
	output.WriteByte('\n')
	output.WriteString(strings.Repeat("---|", len(table.Headers)))
	output.WriteByte('\n')
	for _, row := range table.Rows {
		output.WriteString(strings.Join(row, " | "))
		output.WriteByte('\n')
	}
}

func writeTerminalFinding(output *strings.Builder, finding Finding, prefix string) {
	fmt.Fprintf(output, "%s[%s] %s: %s", prefix, strings.ToUpper(finding.Severity), finding.Title, finding.Summary)
	if location := renderLocation(finding.Location); location != "" {
		fmt.Fprintf(output, " @ %s", location)
	}
	if finding.Recommendation != "" {
		fmt.Fprintf(output, "\n  Recommendation: %s", finding.Recommendation)
	}
	output.WriteByte('\n')
}

// RenderMarkdownPP renders deterministic CommonMark/GFM that is also valid
// Markdown++. It deliberately uses no raw HTML, so the same result is safe to
// pass through the terminal's Markdown++ parser and ACP Markdown clients.
func RenderMarkdownPP(artifact Artifact) (string, error) {
	if err := artifact.ValidateStrict(); err != nil {
		return "", err
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# %s\n\n", markdownInline(artifact.Title))
	fmt.Fprintf(&output, "Status: `%s` · Kind: `%s` · Artifact: `%s`\n\n", artifact.Status, artifact.Kind, artifact.ArtifactID)
	output.WriteString(markdownText(artifact.Summary))

	for _, block := range artifact.Blocks {
		output.WriteString("\n\n")
		writeMarkdownBlock(&output, block)
	}
	if len(artifact.Findings) > 0 {
		output.WriteString("\n\n## Findings\n")
		for _, finding := range artifact.Findings {
			writeMarkdownFinding(&output, finding)
			output.WriteByte('\n')
		}
	}
	if len(artifact.Diagnostics) > 0 {
		output.WriteString("\n## Diagnostics\n")
		for _, diagnostic := range artifact.Diagnostics {
			fmt.Fprintf(&output, "- **%s**", strings.ToUpper(diagnostic.Level))
			if diagnostic.Code != "" {
				fmt.Fprintf(&output, " `%s`", markdownCode(diagnostic.Code))
			}
			fmt.Fprintf(&output, ": %s", markdownText(diagnostic.Message))
			if location := renderLocation(diagnostic.Location); location != "" {
				fmt.Fprintf(&output, " (`%s`)", markdownCode(location))
			}
			output.WriteByte('\n')
		}
	}
	if len(artifact.EvidenceRefs) > 0 {
		output.WriteString("\n## Evidence\n")
		for _, evidence := range artifact.EvidenceRefs {
			writeMarkdownEvidence(&output, evidence, "- ")
			output.WriteByte('\n')
		}
	}
	if len(artifact.NextActions) > 0 {
		output.WriteString("\n## Next actions\n")
		for _, action := range artifact.NextActions {
			fmt.Fprintf(&output, "- %s", markdownText(action.Description))
			if action.Priority != "" {
				fmt.Fprintf(&output, " (`%s`)", markdownCode(action.Priority))
			}
			output.WriteByte('\n')
		}
	}
	if len(artifact.IncompleteReasons) > 0 {
		output.WriteString("\n## Incomplete\n")
		for _, reason := range artifact.IncompleteReasons {
			fmt.Fprintf(&output, "- %s\n", markdownText(reason))
		}
	}
	return strings.TrimRight(output.String(), "\n") + "\n", nil
}

// RenderMarkdown is a concise alias for RenderMarkdownPP for callers that do
// not need to name the Markdown++ compatibility guarantee explicitly.
func RenderMarkdown(artifact Artifact) (string, error) {
	return RenderMarkdownPP(artifact)
}

func writeMarkdownBlock(output *strings.Builder, block Block) {
	switch block.Kind {
	case BlockProse:
		output.WriteString(markdownText(block.Text))
	case BlockHeading:
		level := block.Level
		if level < 2 {
			level = 2
		}
		fmt.Fprintf(output, "%s %s", strings.Repeat("#", level), markdownInline(block.Text))
	case BlockFacts:
		for _, fact := range block.Facts {
			fmt.Fprintf(output, "- **%s:** %s\n", markdownInline(fact.Label), markdownText(fact.Value))
		}
	case BlockTable:
		writeMarkdownTable(output, *block.Table)
	case BlockCode:
		writeMarkdownFence(output, block.Code.Language, block.Code.Content)
	case BlockDiff:
		if block.Diff.Path != "" {
			fmt.Fprintf(output, "Diff for `%s`\n\n", markdownCode(block.Diff.Path))
		}
		writeMarkdownFence(output, "diff", block.Diff.Content)
	case BlockChecklist:
		for _, item := range block.Checklist {
			check := " "
			if checklistDone(item.State) {
				check = "x"
			}
			fmt.Fprintf(output, "- [%s] %s", check, markdownText(item.Text))
			if item.Detail != "" {
				fmt.Fprintf(output, " — %s", markdownText(item.Detail))
			}
			output.WriteByte('\n')
		}
	case BlockFinding:
		writeMarkdownFinding(output, *block.Finding)
	case BlockOperationSummary:
		operation := block.Operation
		fmt.Fprintf(output, "**%s:** `%s`", markdownInline(operation.Operation), markdownCode(operation.Status))
		if operation.DurationMS > 0 {
			fmt.Fprintf(output, " (%d ms)", operation.DurationMS)
		}
		if operation.Detail != "" {
			fmt.Fprintf(output, "\n\n%s", markdownText(operation.Detail))
		}
		if len(operation.Metrics) > 0 {
			output.WriteString("\n\n")
			for _, metric := range operation.Metrics {
				fmt.Fprintf(output, "- **%s:** %s\n", markdownInline(metric.Label), markdownText(metric.Value))
			}
		}
		if len(operation.EvidenceRefs) > 0 {
			output.WriteString("\n")
			for _, evidence := range operation.EvidenceRefs {
				writeMarkdownEvidence(output, evidence, "- ")
				output.WriteByte('\n')
			}
		}
	case BlockEvidenceLink:
		writeMarkdownEvidence(output, *block.Evidence, "")
	}
}

func writeMarkdownTable(output *strings.Builder, table Table) {
	writeMarkdownTableRow(output, table.Headers)
	separator := make([]string, len(table.Headers))
	for index := range separator {
		separator[index] = "---"
	}
	writeMarkdownTableRow(output, separator)
	for _, row := range table.Rows {
		writeMarkdownTableRow(output, row)
	}
}

func writeMarkdownTableRow(output *strings.Builder, row []string) {
	output.WriteString("| ")
	for index, cell := range row {
		if index > 0 {
			output.WriteString(" | ")
		}
		output.WriteString(markdownTableCell(cell))
	}
	output.WriteString(" |\n")
}

func writeMarkdownFence(output *strings.Builder, language, content string) {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	fmt.Fprintf(output, "%s%s\n%s\n%s", fence, markdownLanguage(language), strings.TrimRight(content, "\n"), fence)
}

func writeMarkdownFinding(output *strings.Builder, finding Finding) {
	fmt.Fprintf(output, "### [%s] %s\n\n", strings.ToUpper(finding.Severity), markdownInline(finding.Title))
	output.WriteString(markdownText(finding.Summary))
	if location := renderLocation(finding.Location); location != "" {
		fmt.Fprintf(output, "\n\nLocation: `%s`", markdownCode(location))
	}
	fmt.Fprintf(output, "\n\nConfidence: %.2f", finding.Confidence)
	if finding.Recommendation != "" {
		fmt.Fprintf(output, "\n\nRecommendation: %s", markdownText(finding.Recommendation))
	}
	if len(finding.EvidenceRefs) > 0 {
		output.WriteString("\n\nEvidence:\n")
		for _, evidence := range finding.EvidenceRefs {
			writeMarkdownEvidence(output, evidence, "- ")
			output.WriteByte('\n')
		}
	}
}

func writeMarkdownEvidence(output *strings.Builder, evidence EvidenceRef, prefix string) {
	output.WriteString(prefix)
	label := evidence.Label
	if label == "" {
		label = evidence.ID
	}
	if evidence.URI != "" {
		fmt.Fprintf(output, "[%s](%s)", markdownInline(label), markdownURL(evidence.URI))
	} else {
		fmt.Fprintf(output, "`%s`", markdownCode(evidence.ID))
		if evidence.Label != "" {
			fmt.Fprintf(output, " — %s", markdownText(evidence.Label))
		}
	}
	if evidence.Kind != "" {
		fmt.Fprintf(output, " (%s)", markdownInline(evidence.Kind))
	}
}

func markdownInline(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", " ")
	for _, character := range []string{"*", "_", "[", "]", "`"} {
		value = strings.ReplaceAll(value, character, "\\"+character)
	}
	return value
}

func markdownText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

func markdownCode(value string) string {
	return strings.ReplaceAll(value, "`", "'")
}

func markdownTableCell(value string) string {
	value = markdownText(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " / ")
	return value
}

func markdownLanguage(language string) string {
	var output strings.Builder
	for _, character := range strings.TrimSpace(strings.ToLower(language)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func markdownURL(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "%20")
	value = strings.ReplaceAll(value, ")", "%29")
	return value
}

func checklistDone(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "completed", "complete", "passed", "pass", "checked":
		return true
	default:
		return false
	}
}

func renderLocation(location *Location) string {
	if location == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if location.Path != "" {
		path := location.Path
		if location.StartLine > 0 {
			path += fmt.Sprintf(":%d", location.StartLine)
			if location.EndLine > location.StartLine {
				path += fmt.Sprintf("-%d", location.EndLine)
			}
		}
		parts = append(parts, path)
	}
	if location.Symbol != "" {
		parts = append(parts, location.Symbol)
	}
	return strings.Join(parts, " · ")
}

// ValidateMarkdownPP verifies that rendered Markdown has no Markdown++ parse
// errors. It is intentionally separate from the hot render path so TUI frames
// do not pay parser cost after the artifact was already validated.
func ValidateMarkdownPP(markdown string) error {
	document, err := mdpp.Parse([]byte(markdown))
	if err != nil {
		return fmt.Errorf("parse Markdown++ artifact: %w", err)
	}
	if document == nil {
		return fmt.Errorf("parse Markdown++ artifact: empty document")
	}
	for _, diagnostic := range document.Diagnostics() {
		if diagnostic.Severity == mdpp.SeverityError {
			return fmt.Errorf("parse Markdown++ artifact: %s", diagnostic.Message)
		}
	}
	return nil
}

// RenderJSON returns indented, deterministic JSON after verifying the typed
// artifact contract.
func RenderJSON(artifact Artifact) ([]byte, error) {
	if err := artifact.ValidateStrict(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(artifact, "", "  ")
}

// RenderFluffyUI converts a valid artifact into a presentation-neutral view
// model. It contains typed blocks for rich widgets and Markdown for the
// existing Markdown++ text widget, keeping either surface in lockstep.
func RenderFluffyUI(artifact Artifact) (FluffyUIProjection, error) {
	markdown, err := RenderMarkdownPP(artifact)
	if err != nil {
		return FluffyUIProjection{}, err
	}
	cloned := cloneArtifact(artifact)
	return FluffyUIProjection{
		SchemaVersion:     cloned.SchemaVersion,
		ArtifactID:        cloned.ArtifactID,
		Kind:              cloned.Kind,
		Status:            cloned.Status,
		Title:             cloned.Title,
		Summary:           cloned.Summary,
		Blocks:            cloned.Blocks,
		Findings:          cloned.Findings,
		Diagnostics:       cloned.Diagnostics,
		EvidenceRefs:      cloned.EvidenceRefs,
		NextActions:       cloned.NextActions,
		IncompleteReasons: cloned.IncompleteReasons,
		Markdown:          markdown,
	}, nil
}

// RenderACP provides both a client-readable Markdown body and its canonical
// structured form for ACP extensions or content annotations.
func RenderACP(artifact Artifact) (ACPProjection, error) {
	markdown, err := RenderMarkdownPP(artifact)
	if err != nil {
		return ACPProjection{}, err
	}
	return ACPProjection{Type: "buckley.artifact", Text: markdown, Artifact: cloneArtifact(artifact)}, nil
}

// RenderSARIF turns findings and diagnostics into a deterministic SARIF 2.1.0
// report. Non-review artifacts naturally produce an empty result set.
func RenderSARIF(artifact Artifact) ([]byte, error) {
	if err := artifact.ValidateStrict(); err != nil {
		return nil, err
	}
	findings := append([]Finding(nil), artifact.Findings...)
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].ID != findings[j].ID {
			return findings[i].ID < findings[j].ID
		}
		return findings[i].Title < findings[j].Title
	})
	diagnostics := append([]Diagnostic(nil), artifact.Diagnostics...)
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Code != diagnostics[j].Code {
			return diagnostics[i].Code < diagnostics[j].Code
		}
		return diagnostics[i].Message < diagnostics[j].Message
	})

	rules := make([]sarifRule, 0, len(findings)+len(diagnostics))
	results := make([]sarifResult, 0, len(findings)+len(diagnostics))
	for _, finding := range findings {
		ruleID := "buckley.finding." + finding.ID
		rules = append(rules, sarifRule{ID: ruleID, ShortDescription: sarifMessage{Text: finding.Title}, Properties: map[string]any{"severity": finding.Severity}})
		result := sarifResult{
			RuleID:  ruleID,
			Level:   sarifLevel(finding.Severity),
			Message: sarifMessage{Text: finding.Summary},
			Properties: map[string]any{
				"confidence":     finding.Confidence,
				"recommendation": finding.Recommendation,
				"evidence_refs":  finding.EvidenceRefs,
			},
		}
		result.Locations = sarifLocations(finding.Location)
		results = append(results, result)
	}
	for index, diagnostic := range diagnostics {
		code := diagnostic.Code
		if code == "" {
			code = fmt.Sprintf("diagnostic-%03d", index+1)
		}
		ruleID := "buckley." + code
		rules = append(rules, sarifRule{ID: ruleID, ShortDescription: sarifMessage{Text: diagnostic.Message}})
		result := sarifResult{RuleID: ruleID, Level: sarifDiagnosticLevel(diagnostic.Level), Message: sarifMessage{Text: diagnostic.Message}}
		result.Locations = sarifLocations(diagnostic.Location)
		results = append(results, result)
	}
	return json.MarshalIndent(sarifLog{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool:    sarifTool{Driver: sarifDriver{Name: "Buckley Artifact v1", Rules: rules}},
			Results: results,
			Properties: map[string]any{
				"artifact_id":     artifact.ArtifactID,
				"artifact_kind":   artifact.Kind,
				"artifact_status": artifact.Status,
			},
		}},
	}, "", "  ")
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifRule struct {
	ID               string         `json:"id"`
	ShortDescription sarifMessage   `json:"shortDescription"`
	Properties       map[string]any `json:"properties,omitempty"`
}

type sarifDriver struct {
	Name  string      `json:"name"`
	Rules []sarifRule `json:"rules,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifResult struct {
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level"`
	Message    sarifMessage    `json:"message"`
	Locations  []sarifLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties,omitempty"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties,omitempty"`
}

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

func sarifLevel(severity string) string {
	switch strings.ToLower(severity) {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	default:
		return "note"
	}
}

func sarifDiagnosticLevel(level string) string {
	switch strings.ToLower(level) {
	case "error":
		return "error"
	case "warning":
		return "warning"
	default:
		return "note"
	}
}

func sarifLocations(location *Location) []sarifLocation {
	if location == nil || strings.TrimSpace(location.Path) == "" {
		return nil
	}
	physical := sarifPhysicalLocation{ArtifactLocation: sarifArtifactLocation{URI: location.Path}}
	if location.StartLine > 0 || location.EndLine > 0 {
		physical.Region = &sarifRegion{StartLine: location.StartLine, EndLine: location.EndLine}
	}
	return []sarifLocation{{PhysicalLocation: physical}}
}
