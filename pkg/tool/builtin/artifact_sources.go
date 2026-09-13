package builtin

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

const (
	maxCapturedSources     = 32
	maxCapturedSourceBytes = 16 * 1024
	maxCapturedTotalBytes  = 128 * 1024
)

type capturedSource struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
}

// CaptureReadSource retains only the requested page of a successful, host-owned
// read_file result. It never reads a path, and returned references are usable
// only while their snapshot exists in this submission sink.
func (s *ArtifactSubmission) CaptureReadSource(result *Result) (string, error) {
	if s == nil || result == nil || !result.Success {
		return "", fmt.Errorf("successful read result required")
	}
	source, err := capturedReadPage(result.Data)
	if err != nil {
		return "", err
	}
	if len(source.Content) > maxCapturedSourceBytes {
		return "", fmt.Errorf("source page exceeds %d bytes; read a narrower range", maxCapturedSourceBytes)
	}
	if result.ShouldAbridge && len(result.DisplayData) > 0 {
		page := result.Data["page"].(map[string]any)
		visiblePage, ok := result.DisplayData["page"].(map[string]any)
		visiblePath, pathOK := result.DisplayData["path"].(string)
		visible, contentOK := result.DisplayData["content"].(string)
		if !ok || !pathOK || !contentOK || visiblePath != source.Path || visiblePage["start_line"] != page["start_line"] || visiblePage["end_line"] != page["end_line"] {
			return "", fmt.Errorf("visible read metadata differs from source; capture unavailable")
		}
		pageText := strings.TrimSuffix(source.Content, "\n")
		if visible != pageText {
			var numbered strings.Builder
			if source.StartLine > 0 {
				for i, line := range strings.Split(pageText, "\n") {
					if i > 0 {
						numbered.WriteByte('\n')
					}
					fmt.Fprintf(&numbered, "%d: %s", source.StartLine+i, line)
				}
			}
			if visible != numbered.String() {
				return "", fmt.Errorf("visible read page differs from source; capture unavailable")
			}
		}
	} else if result.Data["content"].(string) != source.Content {
		return "", fmt.Errorf("visible read page differs from source; capture unavailable")
	}
	if !utf8.ValidString(source.Content) || !utf8.ValidString(source.Path) {
		return "", fmt.Errorf("source capture requires UTF-8 text")
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("src_%x", sha256.Sum256(encoded))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return "", fmt.Errorf("artifact already submitted")
	}
	if _, ok := s.sources[id]; ok {
		return id, nil
	}
	size := len(source.Content) + len(source.Path)
	if len(s.sources) >= maxCapturedSources || s.sourceBytes+size > maxCapturedTotalBytes {
		return "", fmt.Errorf("source capture budget exhausted")
	}
	if s.sources == nil {
		s.sources = make(map[string]capturedSource)
	}
	source.Content = strings.Clone(source.Content)
	source.Path = strings.Clone(source.Path)
	s.sources[id] = source
	s.sourceBytes += size
	return id, nil
}

func capturedReadPage(data map[string]any) (capturedSource, error) {
	path, pathOK := data["path"].(string)
	content, contentOK := data["content"].(string)
	page, pageOK := data["page"].(map[string]any)
	if !pathOK || path == "" || !contentOK || !pageOK {
		return capturedSource{}, fmt.Errorf("read result requires path, content and page")
	}
	start, err := readFileLineNumber("start_line", page["start_line"])
	if err != nil {
		return capturedSource{}, err
	}
	zeroEnd := false
	switch v := page["end_line"].(type) {
	case int:
		zeroEnd = v == 0
	case float64:
		zeroEnd = v == 0
	}
	if content == "" && start == 1 && zeroEnd {
		return capturedSource{Path: path}, nil
	}
	end, err := readFileLineNumber("end_line", page["end_line"])
	if err != nil {
		return capturedSource{}, err
	}
	if end < start || end-start >= readFilePageLines {
		return capturedSource{}, fmt.Errorf("invalid source range")
	}
	offset := 0
	for line := 1; line < start; line++ {
		n := strings.IndexByte(content[offset:], '\n')
		if n < 0 {
			return capturedSource{}, fmt.Errorf("source range exceeds file lines")
		}
		offset += n + 1
	}
	begin := offset
	for line := start; ; line++ {
		if offset >= len(content) {
			return capturedSource{}, fmt.Errorf("source range exceeds file lines")
		}
		n := strings.IndexByte(content[offset:], '\n')
		if n < 0 {
			if line != end {
				return capturedSource{}, fmt.Errorf("source range exceeds file lines")
			}
			offset = len(content)
		} else {
			offset += n + 1
		}
		if line == end {
			break
		}
	}
	return capturedSource{Path: path, StartLine: start, EndLine: end, Content: content[begin:offset]}, nil
}

// orderedSourceIDs requires the caller to hold s.mu.
func (s *ArtifactSubmission) orderedSourceIDs() []string {
	ids := make([]string, 0, len(s.sources))
	for id := range s.sources {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		x, y := s.sources[a], s.sources[b]
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		if c := cmp.Compare(x.StartLine, y.StartLine); c != 0 {
			return c
		}
		if c := cmp.Compare(x.EndLine, y.EndLine); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids
}

// appendCapturedSources runs under the submission lock. Source fields are
// generated from captured bytes, not from model-authored paths or excerpts.
func (s *ArtifactSubmission) appendCapturedSources(artifact artifactv1.Artifact, refs []string) (artifactv1.Artifact, error) {
	for _, ref := range artifact.EvidenceRefs {
		if strings.EqualFold(strings.TrimSpace(ref.Kind), "captured_source") {
			return artifact, fmt.Errorf("captured_source evidence is host-owned; use source_refs from read_file and leave artifact evidence_refs empty")
		}
	}
	if len(refs) == 0 {
		return artifact, nil
	}
	if len(refs) > maxCapturedSources {
		return artifact, fmt.Errorf("at most %d source references may be submitted", maxCapturedSources)
	}
	if len(refs) == 1 && refs[0] == "all" {
		if len(s.sources) == 0 {
			return artifact, fmt.Errorf("no captured sources to select; capture source pages with read_file first")
		}
		refs = s.orderedSourceIDs()
	}
	if len(artifact.Blocks) != 0 || len(artifact.EvidenceRefs) != 0 {
		return artifact, fmt.Errorf("with source_refs, leave artifact blocks and evidence_refs empty; Buckley fills them from captured pages")
	}
	table := &artifactv1.Table{Headers: []string{"source_ref", "path", "start_line", "end_line", "content"}}
	artifact.EvidenceRefs = nil
	seen := make(map[string]bool, len(refs))
	for _, id := range refs {
		source, ok := s.sources[id]
		if !ok {
			return artifact, fmt.Errorf("unknown source reference %q; use source_ref from a read in this run", id)
		}
		if seen[id] {
			return artifact, fmt.Errorf("duplicate source reference %q", id)
		}
		seen[id] = true
		table.Rows = append(table.Rows, []string{id, source.Path, strconv.Itoa(source.StartLine), strconv.Itoa(source.EndLine), source.Content})
		location := url.URL{Scheme: "file", Path: source.Path}
		if source.StartLine > 0 {
			location.Fragment = fmt.Sprintf("L%d-L%d", source.StartLine, source.EndLine)
		}
		artifact.EvidenceRefs = append(artifact.EvidenceRefs, artifactv1.EvidenceRef{ID: id, Kind: "captured_source", Label: source.Path, URI: location.String()})
	}
	artifact.Blocks = []artifactv1.Block{{Kind: artifactv1.BlockTable, Table: table}}
	artifact.ArtifactID = ""
	return artifact, nil
}

func parseSourceRefs(raw any) ([]string, error) {
	var refs []string
	switch value := raw.(type) {
	case []string:
		refs = value
	case []any:
		if len(value) > maxCapturedSources {
			return nil, fmt.Errorf("too many source references")
		}
		refs = make([]string, len(value))
		for i, item := range value {
			var ok bool
			refs[i], ok = item.(string)
			if !ok {
				return nil, fmt.Errorf("source_refs must be an array of strings")
			}
		}
	default:
		return nil, fmt.Errorf("source_refs must be an array of strings")
	}
	if len(refs) > maxCapturedSources {
		return nil, fmt.Errorf("too many source references")
	}
	for _, id := range refs {
		if id == "all" {
			if len(refs) == 1 {
				continue
			}
			return nil, fmt.Errorf("source_refs \"all\" must be the only reference")
		}
		if len(id) != 68 || !strings.HasPrefix(id, "src_") {
			return nil, fmt.Errorf("invalid source reference; use source_ref returned by read_file")
		}
	}
	return refs, nil
}
