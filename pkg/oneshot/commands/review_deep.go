package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odvcencio/gotreesitter/grammars"

	"m31labs.dev/buckley/pkg/transparency"
)

// Deep project review splits the repository into review units — Go-family
// files per directory (a directory of Go files is a package), every other
// gotreesitter-recognized language grouped by top-level directory — and
// reviews each unit with its FILE CONTENTS in the prompt. The classic
// -project mode sends only a structure summary (tree + go.mod + README +
// recent log), which caps it at architecture/hygiene findings; deep units
// give the model real code, fan out over a bounded worker pool, and reduce
// through a synthesis call into one graded, ParseReview-compatible report.

// DeepUnit is one independently reviewable slice of the repository.
type DeepUnit struct {
	// Name identifies the unit: "pkg:<import path>" for Go packages,
	// "files:<top-level dir>" for non-Go source groups.
	Name string
	// Dir is the unit's directory relative to the repo root ("." for root).
	Dir string
	// Files are repo-root-relative source paths belonging to the unit.
	Files []string
}

// DeepUnitContext is a unit plus its assembled prompt body.
type DeepUnitContext struct {
	Unit     DeepUnit
	Body     string
	Included []string
	// Omitted lists files that did not fit the unit token budget; they are
	// named in the prompt so the reviewer knows coverage was partial.
	Omitted []string
	Tokens  int
}

// DeepUnitReport is the outcome of one unit's review call.
type DeepUnitReport struct {
	Unit   DeepUnit
	Review string
	Err    error
}

// DeepReviewOptions bounds per-unit context assembly.
type DeepReviewOptions struct {
	// UnitTokenBudget caps the estimated tokens of file content per unit;
	// files past the budget are listed as omitted rather than truncated
	// mid-file.
	UnitTokenBudget int
	// MaxFileBytes caps any single file read.
	MaxFileBytes int
}

// DefaultDeepReviewOptions returns the standard deep-review budgets.
func DefaultDeepReviewOptions() DeepReviewOptions {
	return DeepReviewOptions{
		UnitTokenBudget: 20_000,
		MaxFileBytes:    64_000,
	}
}

// deepSkipDirs are directory names never descended into during the non-Go
// walk: VCS state, dependency trees, build artifacts, binary asset stores,
// fixtures, and agent/tool scratch state.
var deepSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"vendor":       true,
	"testdata":     true,
	"assets":       true,
	".analyses":    true,
	".buckley":     true,
	".claude":      true,
	".codex":       true,
	"tool-results": true,
}

// deepOrgDSLExts supplements the gotreesitter registry with the org's own
// DSLs it doesn't (yet) carry grammars for. .gsx is Go-family — pages and
// islands review best alongside the package sharing their directory; .sel
// (Selena materials) and .arb (Arbiter rules) carry as much behavior as
// the Go and group by top-level directory like any other language.
var deepOrgDSLExts = map[string]string{
	".gsx": "go",
	".sel": "selena",
	".arb": "arbiter",
}

// deepProseLangs are registry languages that are data/prose, not reviewable
// source — the registry knows them (it highlights everything), a code
// review does not want them.
var deepProseLangs = map[string]bool{
	"markdown": true,
	"json":     true,
	"jsonc":    true,
	"csv":      true,
	"tsv":      true,
	"xml":      true,
	"svg":      true,
	"html":     true,
	"text":     true,
	"diff":     true,
}

// deepLanguageOf classifies a file: org DSL supplement first, then the
// gotreesitter grammar registry (205 languages, exact-filename and
// extension aware) — no hand-rolled extension lists. Returns "" for files
// that are not reviewable source. Minified and sourcemap artifacts are
// excluded regardless of language.
// deepManifestFiles are dependency manifests and lockfiles — context, not
// review targets (the deep project header already carries go.mod).
var deepManifestFiles = map[string]bool{
	"go.mod":            true,
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"cargo.lock":        true,
	"gemfile.lock":      true,
	"composer.lock":     true,
}

func deepLanguageOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if deepManifestFiles[base] {
		return ""
	}
	if strings.Contains(base, ".min.") || strings.HasSuffix(base, ".map") {
		return ""
	}
	if lang, ok := deepOrgDSLExts[strings.ToLower(filepath.Ext(path))]; ok {
		return lang
	}
	entry := grammars.DetectLanguage(path)
	if entry == nil {
		return ""
	}
	name := strings.ToLower(entry.Name)
	if deepProseLangs[name] {
		return ""
	}
	return name
}

// EnumerateDeepUnits lists the repo's review units deterministically and
// toolchain-free: Go-family files group per DIRECTORY (a directory of Go
// files is a package — the same boundary `go list` would give, without
// forking a toolchain binary), every other recognized language groups by
// top-level directory. Language membership comes from the gotreesitter
// registry plus the org DSL supplement.
func EnumerateDeepUnits(repoRoot string) ([]DeepUnit, error) {
	goDirs := map[string][]string{}
	groups := map[string][]string{}

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if info.IsDir() {
			if path != repoRoot && (deepSkipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		lang := deepLanguageOf(path)
		if lang == "" {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if lang == "go" {
			dir := "."
			if idx := strings.LastIndex(rel, "/"); idx > 0 {
				dir = rel[:idx]
			}
			goDirs[dir] = append(goDirs[dir], rel)
			return nil
		}
		top := "root"
		if idx := strings.Index(rel, "/"); idx > 0 {
			top = rel[:idx]
		}
		groups[top] = append(groups[top], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var units []DeepUnit
	for dir, files := range goDirs {
		sort.Strings(files)
		name := "pkg:" + dir
		if dir == "." {
			name = "pkg:root"
		}
		units = append(units, DeepUnit{Name: name, Dir: dir, Files: files})
	}
	for top, files := range groups {
		sort.Strings(files)
		dir := top
		if top == "root" {
			dir = "."
		}
		units = append(units, DeepUnit{Name: "files:" + top, Dir: dir, Files: files})
	}

	sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
	if len(units) == 0 {
		return nil, fmt.Errorf("no reviewable units found under %s", repoRoot)
	}
	return units, nil
}

// AssembleDeepUnitContext reads a unit's files into a fenced prompt body,
// whole files only, until the token budget is spent; the remainder is
// recorded (and later prompted) as omitted.
func AssembleDeepUnitContext(repoRoot string, unit DeepUnit, opts DeepReviewOptions, audit *transparency.ContextAudit) DeepUnitContext {
	uc := DeepUnitContext{Unit: unit}
	var sb strings.Builder
	for _, rel := range unit.Files {
		if uc.Tokens >= opts.UnitTokenBudget {
			uc.Omitted = append(uc.Omitted, rel)
			continue
		}
		content, err := reviewReadFileLimited(filepath.Join(repoRoot, filepath.FromSlash(rel)), opts.MaxFileBytes)
		if err != nil || content == "" {
			uc.Omitted = append(uc.Omitted, rel)
			continue
		}
		tokens := reviewEstimateTokens(content)
		if uc.Tokens > 0 && uc.Tokens+tokens > opts.UnitTokenBudget {
			uc.Omitted = append(uc.Omitted, rel)
			continue
		}
		fmt.Fprintf(&sb, "### %s\n```%s\n%s\n```\n\n", rel, deepFenceLang(rel), content)
		uc.Tokens += tokens
		uc.Included = append(uc.Included, rel)
	}
	uc.Body = sb.String()
	if audit != nil {
		audit.Add("unit "+unit.Name, uc.Tokens)
	}
	return uc
}

func deepFenceLang(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".gsx":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".py":
		return "python"
	case ".css":
		return "css"
	case ".sh":
		return "bash"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	default:
		return ""
	}
}

// BuildDeepProjectHeader renders the shared per-unit preamble: repo
// identity plus go.mod so unit reviewers know the module and dependency
// context without each unit re-shipping the tree.
func BuildDeepProjectHeader(ctx *ProjectContext) string {
	var sb strings.Builder
	sb.WriteString("## Repository\n\n")
	fmt.Fprintf(&sb, "- **Root**: %s\n", ctx.RepoRoot)
	fmt.Fprintf(&sb, "- **Branch**: %s\n\n", ctx.Branch)
	if ctx.GoMod != "" {
		sb.WriteString("## go.mod\n\n```\n")
		sb.WriteString(ctx.GoMod)
		sb.WriteString("\n```\n\n")
	}
	return sb.String()
}

// BuildDeepUnitPrompt renders one unit's review request.
func BuildDeepUnitPrompt(projectHeader string, uc DeepUnitContext) string {
	var sb strings.Builder
	sb.WriteString(projectHeader)
	fmt.Fprintf(&sb, "## Review Unit: %s\n\n", uc.Unit.Name)
	fmt.Fprintf(&sb, "- **Directory**: %s\n", uc.Unit.Dir)
	fmt.Fprintf(&sb, "- **Files included**: %d (%d estimated tokens)\n", len(uc.Included), uc.Tokens)
	if len(uc.Omitted) > 0 {
		fmt.Fprintf(&sb, "- **Files omitted (unit budget)**: %s\n", strings.Join(uc.Omitted, ", "))
	}
	sb.WriteString("\n## Unit Source\n\n")
	sb.WriteString(uc.Body)
	return sb.String()
}

// BuildDeepSynthesisPrompt concatenates unit reports for the reduce call,
// trimming each report to an even share when the whole exceeds maxTokens.
func BuildDeepSynthesisPrompt(reports []DeepUnitReport, maxTokens int) string {
	ok := 0
	for _, r := range reports {
		if r.Err == nil {
			ok++
		}
	}
	perUnit := maxTokens
	if ok > 0 {
		perUnit = maxTokens / ok
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Synthesize the following %d per-unit review reports into one repository-wide review.\n\n", ok)
	for _, r := range reports {
		if r.Err != nil {
			fmt.Fprintf(&sb, "## Unit: %s\n\n(review FAILED: %v — note the coverage gap)\n\n", r.Unit.Name, r.Err)
			continue
		}
		body := r.Review
		if reviewEstimateTokens(body) > perUnit {
			// ~4 chars/token mirror of reviewEstimateTokens.
			cut := perUnit * 4
			if cut < len(body) {
				body = body[:cut] + "\n\n[... truncated for synthesis budget ...]"
			}
		}
		fmt.Fprintf(&sb, "## Unit: %s\n\n%s\n\n", r.Unit.Name, body)
	}
	return sb.String()
}

// BuildDeepReport assembles the final artifact: the synthesized graded
// review, then every per-unit report as an appendix so nothing a unit
// reviewer said is lost to the reduce step.
func BuildDeepReport(synthesis string, reports []DeepUnitReport) string {
	var sb strings.Builder
	sb.WriteString(synthesis)
	sb.WriteString("\n\n---\n\n# Per-Unit Reports (appendix)\n\n")
	for _, r := range reports {
		fmt.Fprintf(&sb, "## %s\n\n", r.Unit.Name)
		if r.Err != nil {
			fmt.Fprintf(&sb, "_Review failed: %v_\n\n", r.Err)
			continue
		}
		sb.WriteString(strings.TrimSpace(r.Review))
		sb.WriteString("\n\n")
	}
	return sb.String()
}
