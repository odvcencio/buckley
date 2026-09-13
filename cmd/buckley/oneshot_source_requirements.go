package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

const (
	sourceTextCoverageReserve     = 64 * 1024
	maxSourceTextRequirements     = 32
	maxSourceTextRequirementBytes = 256
	requiredSourceTextHeader      = "required_source_text"
)

// validateSourceTextRequirements checks caller-supplied required literals.
// Literal whitespace is preserved; only emptiness is judged by trimming.
func validateSourceTextRequirements(required []string) error {
	if len(required) > maxSourceTextRequirements {
		return fmt.Errorf("at most %d source text requirements allowed; got %d", maxSourceTextRequirements, len(required))
	}
	seen := make(map[string]bool, len(required))
	for i, literal := range required {
		if strings.TrimSpace(literal) == "" {
			return fmt.Errorf("source text requirement %d must contain non-whitespace text", i+1)
		}
		if !utf8.ValidString(literal) {
			return fmt.Errorf("source text requirement %d must be valid UTF-8", i+1)
		}
		if len(literal) > maxSourceTextRequirementBytes {
			return fmt.Errorf("source text requirement %d exceeds %d bytes", i+1, maxSourceTextRequirementBytes)
		}
		if seen[literal] {
			return fmt.Errorf("duplicate source text requirement %q", literal)
		}
		seen[literal] = true
	}
	return nil
}

// sourceTextRequirementInstruction returns the host instruction describing the
// required literals and how they must be reported. An observed entry asserts a
// literal occurrence in captured bytes only, never semantic proof.
func sourceTextRequirementInstruction(required []string) string {
	if len(required) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(required)
	return "Required source text coverage: Buckley computes the coverage table itself from host-captured pages; do NOT write a required_source_text table, and do not add blocks or evidence_refs when submitting source_refs. " +
		"Read the relevant pages with read_file, then submit source_refs [\"all\"] so every captured page counts as evidence. " +
		"In your summary, report each required literal verbatim: " + string(encoded) + ". " +
		"An observed entry asserts a literal substring occurrence only; it is not semantic proof of any property."
}

type capturedPage struct {
	startLine  int
	content    string
	hasContent bool
}

// applySourceTextRequirements is called only after trusted provenance
// enforcement (resolveOneShotArtifact or a sink RecoveryArtifact). With no
// requirements it returns the artifact unchanged. Otherwise it removes any
// model-authored required_source_text table, checks every required literal
// against host-captured source pages only, appends one final coverage table,
// and downgrades non-terminal statuses when evidence is missing.
func applySourceTextRequirements(a artifactv1.Artifact, required []string) (artifactv1.Artifact, error) {
	if err := validateSourceTextRequirements(required); err != nil {
		return artifactv1.Artifact{}, err
	}
	if len(required) == 0 {
		return a, nil
	}

	out := a.Normalized()
	blocks := make([]artifactv1.Block, 0, len(out.Blocks))
	for _, block := range out.Blocks {
		if block.Kind == artifactv1.BlockTable && block.Table != nil &&
			len(block.Table.Headers) > 0 && block.Table.Headers[0] == requiredSourceTextHeader {
			continue
		}
		blocks = append(blocks, block)
	}

	pages := collectCapturedSourcePages(blocks, out.EvidenceRefs)
	refs := make([]string, 0, len(pages))
	for ref := range pages {
		refs = append(refs, ref)
	}
	slices.Sort(refs)

	table := &artifactv1.Table{
		Headers: []string{requiredSourceTextHeader, "evidence_status", "source_ref", "start_line", "end_line"},
	}
	missing := 0
	for _, literal := range required {
		row := make([]string, 5)
		row[0] = literal
		row[1] = "not_observed"
		for _, ref := range refs {
			page := pages[ref]
			pos := strings.Index(page.content, literal)
			if pos < 0 {
				continue
			}
			row[1] = "observed"
			row[2] = ref
			row[3], row[4] = matchingLineRange(page, pos, literal)
			break
		}
		if row[1] != "observed" {
			missing++
		}
		table.Rows = append(table.Rows, row)
	}
	blocks = append(blocks, artifactv1.Block{Kind: artifactv1.BlockTable, Table: table})
	out.Blocks = blocks

	if missing > 0 {
		switch out.Status {
		case artifactv1.StatusFailed, artifactv1.StatusBlocked, artifactv1.StatusIncomplete:
			// Never upgrade an already-terminal or incomplete result.
		default:
			out.Status = artifactv1.StatusIncomplete
		}
		reason := fmt.Sprintf("required source text not observed: %d of %d literals are missing from captured source pages; see the required_source_text table", missing, len(required))
		if !slices.Contains(out.IncompleteReasons, reason) {
			out.IncompleteReasons = append(out.IncompleteReasons, reason)
		}
	}

	// The coverage table is host-authored; detach any prior artifact identity.
	out.ArtifactID = ""
	out, err := artifactv1.NormalizeAndValidate(out)
	if err != nil {
		return artifactv1.Artifact{}, fmt.Errorf("normalizing source-coverage artifact: %w", err)
	}
	rendered, err := artifactv1.RenderJSON(out)
	if err != nil {
		return artifactv1.Artifact{}, fmt.Errorf("rendering source-coverage artifact: %w", err)
	}
	if len(rendered) > artifactv1.MaxProviderBytes {
		return artifactv1.Artifact{}, fmt.Errorf("source-coverage artifact exceeds %d-byte provider limit", artifactv1.MaxProviderBytes)
	}
	return out, nil
}

// collectCapturedSourcePages indexes only exact host capture tables whose
// headers are source_ref,path,start_line,end_line,content and whose rows are
// backed by captured_source evidence refs. All other blocks are ignored.
func collectCapturedSourcePages(blocks []artifactv1.Block, refs []artifactv1.EvidenceRef) map[string]capturedPage {
	backed := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if strings.EqualFold(strings.TrimSpace(ref.Kind), "captured_source") {
			backed[ref.ID] = true
		}
	}
	pages := make(map[string]capturedPage)
	for _, block := range blocks {
		if block.Kind != artifactv1.BlockTable || block.Table == nil {
			continue
		}
		table := block.Table
		if len(table.Headers) != 5 ||
			table.Headers[0] != "source_ref" || table.Headers[1] != "path" ||
			table.Headers[2] != "start_line" || table.Headers[3] != "end_line" ||
			table.Headers[4] != "content" {
			continue
		}
		for _, row := range table.Rows {
			if len(row) != 5 || !backed[row[0]] || pages[row[0]].hasContent {
				continue
			}
			start, _, ok := parseCapturedLineRange(row[2], row[3])
			if !ok {
				continue
			}
			pages[row[0]] = capturedPage{startLine: start, content: row[4], hasContent: true}
		}
	}
	return pages
}

func parseCapturedLineRange(startText, endText string) (int, int, bool) {
	start, err := strconv.Atoi(startText)
	if err != nil || start <= 0 {
		return 0, 0, false
	}
	end, err := strconv.Atoi(endText)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// matchingLineRange computes the actual matching line range within a captured
// page. A literal ending in a newline terminates its own line and must not
// extend the reported range into the following line.
func matchingLineRange(page capturedPage, pos int, literal string) (string, string) {
	newlinesBefore := strings.Count(page.content[:pos], "\n")
	start := page.startLine + newlinesBefore
	newlinesIn := strings.Count(literal, "\n")
	if strings.HasSuffix(literal, "\n") {
		newlinesIn--
	}
	return fmt.Sprintf("%d", start), fmt.Sprintf("%d", start+newlinesIn)
}
