// Package reviewvalidate deterministically grounds the file:line claims a code
// review makes against the actual reviewed source. It is the "positioning /
// reflection" layer that lets buckley trust review output from ANY model: a
// weaker or hallucination-prone model that cites a nonexistent file, an
// out-of-range line, or code that isn't there gets its finding flagged, without
// the validator itself ever calling a model.
package reviewvalidate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// OSFileSource reads reviewed files from a repository root on disk.
type OSFileSource struct{ Root string }

// ReadFile implements FileSource against the working tree.
func (s OSFileSource) ReadFile(path string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(s.Root, filepath.Clean("/"+path)[1:]))
	if err != nil {
		return nil, false
	}
	return b, true
}

// RepoFileSource indexes a repository so it can resolve the PARTIAL paths reviews
// actually cite — a bare `hack_scanner.go` that really lives at
// `grammars/hack_scanner.go`, or a suffix like `docker/Dockerfile`. Without this,
// a validator flags real files as "missing" simply because the model dropped the
// directory prefix.
type RepoFileSource struct {
	root   string
	byRel  map[string]bool     // exact repo-relative paths (slash-normalized)
	byBase map[string][]string // basename -> repo-relative paths
}

// NewRepoFileSource walks root once and builds the path index (skipping .git and
// other VCS/noise dirs).
func NewRepoFileSource(root string) (*RepoFileSource, error) {
	s := &RepoFileSource{root: root, byRel: map[string]bool{}, byBase: map[string][]string{}}
	skip := map[string]bool{".git": true, ".hg": true, ".svn": true, "node_modules": true}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		s.byRel[rel] = true
		base := d.Name()
		s.byBase[base] = append(s.byBase[base], rel)
		return nil
	})
	return s, err
}

// resolve maps a cited path to a real repo-relative path: exact, then unique
// suffix match, then unique basename. Ambiguous (multiple) matches are treated
// as existing-but-unresolved (returns the first, still "exists").
func (s *RepoFileSource) resolve(path string) (string, bool) {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	if s.byRel[path] {
		return path, true
	}
	// Suffix match: cited path is the tail of a real path.
	var suffixHits []string
	needle := "/" + path
	for rel := range s.byRel {
		if strings.HasSuffix(rel, needle) {
			suffixHits = append(suffixHits, rel)
		}
	}
	if len(suffixHits) == 1 {
		return suffixHits[0], true
	}
	if len(suffixHits) > 1 {
		return suffixHits[0], true // ambiguous but real
	}
	// Basename match.
	if hits := s.byBase[filepath.Base(path)]; len(hits) >= 1 {
		return hits[0], true
	}
	return "", false
}

// ReadFile implements FileSource with repo-aware resolution.
func (s *RepoFileSource) ReadFile(path string) ([]byte, bool) {
	rel, ok := s.resolve(path)
	if !ok {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	return b, true
}

// Summary aggregates grounding verdicts across a whole review.
type Summary struct {
	TotalRefs      int
	Verified       int
	LineDrifted    int
	FileMissing    int
	LineOutOfRange int
	Ungrounded     int
	Unlocated      int
	Verdicts       []Verdict
}

// GroundRatio is grounded refs (line-accurate + line-drifted) over all located
// refs — the share of concrete claims that point at code that actually exists.
// Returns 1 when there are no located refs.
func (s Summary) GroundRatio() float64 {
	located := s.Verified + s.LineDrifted + s.LineOutOfRange + s.Ungrounded
	if located == 0 {
		return 1
	}
	return float64(s.Verified+s.LineDrifted) / float64(located)
}

// SuspectCount is the number of refs that look fabricated: a missing file, an
// out-of-range line, or a token that appears nowhere in the cited file.
func (s Summary) SuspectCount() int { return s.FileMissing + s.LineOutOfRange + s.Ungrounded }

func (s Summary) String() string {
	return fmt.Sprintf("refs=%d verified=%d line_drifted=%d file_missing=%d line_out_of_range=%d ungrounded=%d unlocated=%d (ground_ratio=%.0f%%, suspect=%d)",
		s.TotalRefs, s.Verified, s.LineDrifted, s.FileMissing, s.LineOutOfRange, s.Ungrounded, s.Unlocated, s.GroundRatio()*100, s.SuspectCount())
}

// GroundReview validates every source reference in a review against src.
// Grounding tokens are scoped to the paragraph a reference appears in, so a
// claim is only "verified" when the file/line exists AND a distinctive token
// from the SAME finding shows up near that line. This catches models that cite a
// real file with a fabricated location or claim.
func GroundReview(reviewText string, src FileSource, lineTolerance int) Summary {
	var sum Summary
	for _, block := range splitParagraphs(reviewText) {
		refs := ExtractRefs(block)
		if len(refs) == 0 {
			continue
		}
		tokens := GroundingTokens(block)
		for _, ref := range refs {
			v := ValidateRef(src, ref, tokens, lineTolerance)
			sum.Verdicts = append(sum.Verdicts, v)
			sum.TotalRefs++
			switch v.Status {
			case StatusVerified:
				sum.Verified++
			case StatusLineDrifted:
				sum.LineDrifted++
			case StatusFileMissing:
				sum.FileMissing++
			case StatusLineOutOfRange:
				sum.LineOutOfRange++
			case StatusUngrounded:
				sum.Ungrounded++
			case StatusUnlocated:
				sum.Unlocated++
			}
		}
	}
	return sum
}

// splitParagraphs breaks review text into blocks on blank lines so a finding's
// refs are grounded by that finding's own tokens.
func splitParagraphs(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(raw))
	for _, b := range raw {
		if strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

// Ref is a source reference extracted from review text: a path with an optional
// line (or line range).
type Ref struct {
	Path    string
	Line    int    // 0 when no line was cited
	EndLine int    // 0 unless a range (path:start-end) was cited
	Raw     string // the matched text, for reporting
}

// Status is the grounding verdict for a single Ref.
type Status string

const (
	// Verified: the file exists, any cited line is in range, and a distinctive
	// token from the finding appears near it.
	StatusVerified Status = "verified"
	// FileMissing: the cited path is not in the reviewed snapshot — a strong
	// hallucination signal.
	StatusFileMissing Status = "file_missing"
	// LineOutOfRange: the file exists but the cited line is past its end.
	StatusLineOutOfRange Status = "line_out_of_range"
	// LineDrifted: the file exists and a distinctive token from the finding IS in
	// the file, but not near the cited line. Real models (e.g. Kimi K3) routinely
	// cite approximate lines, so this is a GROUNDED claim with a stale location —
	// not a hallucination.
	StatusLineDrifted Status = "line_drifted"
	// Ungrounded: the file exists but NONE of the finding's distinctive tokens
	// appear anywhere in it — a strong fabrication signal.
	StatusUngrounded Status = "ungrounded"
	// Unlocated: file exists and no line was cited, so only existence is known.
	StatusUnlocated Status = "unlocated"
)

// Verdict is the result of grounding one Ref.
type Verdict struct {
	Ref
	Status      Status
	FileExists  bool
	LineInRange bool
	Grounded    bool
}

// FileSource reads a reviewed file's bytes. Returns (content, true) if the path
// exists in the snapshot. Backed by the review snapshot or the working tree.
type FileSource interface {
	ReadFile(path string) ([]byte, bool)
}

// refPattern matches "path.ext", "path.ext:123", or "path.ext:123-456". The
// extension anchor avoids matching prose, versions ("0.25.1"), or "e.g.".
var refPattern = regexp.MustCompile(`([A-Za-z0-9_./-]+\.[A-Za-z][A-Za-z0-9]{0,4})(?::(\d+)(?:-(\d+))?)?`)

// codeExtensions bounds what counts as a source reference (keeps prose like
// "Node.js" or "README" from being treated as a path when it shouldn't).
var codeExtensions = map[string]bool{
	"go": true, "py": true, "js": true, "ts": true, "tsx": true, "jsx": true,
	"rs": true, "c": true, "h": true, "cc": true, "cpp": true, "hpp": true,
	"java": true, "rb": true, "php": true, "cs": true, "swift": true, "kt": true,
	"yml": true, "yaml": true, "toml": true, "json": true, "md": true, "sh": true,
	"bash": true, "sql": true, "proto": true, "mod": true, "sum": true, "lock": true,
	"dockerfile": true, "makefile": true, "cfg": true, "ini": true, "xml": true,
}

// ExtractRefs pulls unique source references out of a finding's text.
func ExtractRefs(text string) []Ref {
	seen := map[string]bool{}
	var refs []Ref
	for _, m := range refPattern.FindAllStringSubmatch(text, -1) {
		path := m[1]
		dot := strings.LastIndex(path, ".")
		if dot < 0 {
			continue
		}
		ext := strings.ToLower(path[dot+1:])
		if !codeExtensions[ext] {
			continue
		}
		// Require a cited line OR a directory separator. A bare filename in prose
		// ("the parser.go file", "Node.js") is too ambiguous to treat as a
		// reference worth validating; a path with a slash or a :line is precise.
		if m[2] == "" && !strings.Contains(path, "/") {
			continue
		}
		ref := Ref{Path: path, Raw: m[0]}
		if m[2] != "" {
			ref.Line, _ = strconv.Atoi(m[2])
		}
		if m[3] != "" {
			ref.EndLine, _ = strconv.Atoi(m[3])
		}
		key := path + ":" + m[2] + "-" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	return refs
}

// tokenPattern extracts distinctive code identifiers to ground a claim: dotted
// or _/CamelCase identifiers of length >= 4, which are unlikely to appear by
// chance near an arbitrary line.
var tokenPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

// GroundingTokens pulls candidate identifiers from finding text, preferring
// backtick-quoted spans (which reviews use for code) and dropping common English
// words so grounding is meaningful.
func GroundingTokens(text string) []string {
	seen := map[string]bool{}
	var toks []string
	add := func(s string) {
		for _, t := range tokenPattern.FindAllString(s, -1) {
			lt := strings.ToLower(t)
			if len(t) < 4 || stopWords[lt] || seen[lt] {
				continue
			}
			seen[lt] = true
			toks = append(toks, t)
		}
	}
	// Prefer backtick-quoted code spans; fall back to the whole text.
	if spans := backtickPattern.FindAllStringSubmatch(text, -1); len(spans) > 0 {
		for _, s := range spans {
			add(s[1])
		}
	}
	add(text)
	return toks
}

var backtickPattern = regexp.MustCompile("`([^`]+)`")

// ValidateRef grounds a single ref: existence, line-in-range, and whether any
// grounding token appears within lineTolerance lines of the cited line.
func ValidateRef(src FileSource, ref Ref, groundingTokens []string, lineTolerance int) Verdict {
	v := Verdict{Ref: ref}
	content, ok := src.ReadFile(ref.Path)
	if !ok {
		v.Status = StatusFileMissing
		return v
	}
	v.FileExists = true
	if ref.Line <= 0 {
		v.Status = StatusUnlocated
		return v
	}
	lines := strings.Split(string(content), "\n")
	if ref.Line > len(lines) {
		v.Status = StatusLineOutOfRange
		return v
	}
	v.LineInRange = true

	if len(groundingTokens) == 0 {
		// Nothing distinctive to ground against; existence + range is all we know.
		v.Status = StatusVerified
		return v
	}

	// Tier 1: a distinctive token near the cited line → line-accurate.
	lo := ref.Line - 1 - lineTolerance
	if lo < 0 {
		lo = 0
	}
	hi := ref.Line - 1 + lineTolerance
	if ref.EndLine > ref.Line {
		hi = ref.EndLine - 1 + lineTolerance
	}
	if hi >= len(lines) {
		hi = len(lines) - 1
	}
	if containsAnyToken(strings.Join(lines[lo:hi+1], "\n"), groundingTokens) {
		v.Grounded = true
		v.Status = StatusVerified
		return v
	}

	// Tier 2: token elsewhere in the file → real claim, drifted line (common).
	if containsAnyToken(string(content), groundingTokens) {
		v.Grounded = true
		v.Status = StatusLineDrifted
		return v
	}

	// Tier 3: token nowhere in the file → likely fabricated.
	v.Status = StatusUngrounded
	return v
}

func containsAnyToken(haystack string, tokens []string) bool {
	lower := strings.ToLower(haystack)
	for _, t := range tokens {
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// stopWords are common English/review words that must not be treated as
// grounding tokens.
var stopWords = map[string]bool{
	"this": true, "that": true, "with": true, "when": true, "then": true, "have": true,
	"from": true, "into": true, "code": true, "file": true, "line": true, "lines": true,
	"test": true, "tests": true, "case": true, "cases": true, "will": true, "must": true,
	"should": true, "review": true, "issue": true, "fix": true, "what": true, "why": true,
	"where": true, "effort": true, "high": true, "medium": true, "small": true, "large": true,
	"function": true, "struct": true, "method": true, "value": true, "return": true, "which": true,
	"only": true, "each": true, "every": true, "some": true, "same": true, "path": true, "name": true,
}
