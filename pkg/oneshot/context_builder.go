package oneshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	gopath "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"m31labs.dev/buckley/internal/gitdiff"
	"m31labs.dev/buckley/pkg/diffsignal"
)

// ContextOpts configures context building behavior.
type ContextOpts struct {
	// MaxDiffBytes limits the size of gathered diffs.
	MaxDiffBytes int

	// WorkDir overrides the working directory for git commands.
	// If empty, the current directory is used.
	WorkDir string

	// RankDiffForPR selects diffsignal.PrioritizeForPR over the default
	// diffsignal.Prioritize when gathering a git_diff source: high-signal
	// files are ranked source-first/tests-next/docs-config-after/dotdirs-
	// last instead of git's alphabetical emission order, and the per-file
	// cap is smaller so the budget spreads across more files. `buckley pr`
	// sets this; commit and review keep the default ordering.
	RankDiffForPR bool
}

// DefaultContextOpts returns sensible defaults.
func DefaultContextOpts() ContextOpts {
	return ContextOpts{
		MaxDiffBytes: 80_000,
	}
}

// BuildContext gathers content from the given sources and returns a unified Context.
func BuildContext(sources []ContextSource, opts ContextOpts) (*Context, error) {
	if opts.MaxDiffBytes <= 0 {
		opts.MaxDiffBytes = 80_000
	}

	ctx := &Context{
		Sources: make(map[string]string, len(sources)),
	}

	for _, src := range sources {
		content, stats, err := gatherSourceWithStats(src, opts)
		if err != nil {
			return nil, fmt.Errorf("gathering %s: %w", src.Type, err)
		}
		if src.Type == "git_diff" {
			ctx.Diff = stats
		}
		if content == "" {
			continue
		}

		label := sourceLabel(src)
		ctx.Sources[label] = content
		ctx.Tokens += contextEstimateTokens(content)
	}

	return ctx, nil
}

// sourceLabel returns a key for the source in the context map.
func sourceLabel(src ContextSource) string {
	switch src.Type {
	case "git_diff":
		if src.Params["staged"] == "true" {
			return "git_diff:staged"
		}
		if base := src.Params["base"]; base != "" {
			return "git_diff:" + base
		}
		return "git_diff"
	case "git_log":
		if base := src.Params["base"]; base != "" {
			return "git_log:" + base
		}
		return "git_log"
	case "git_files":
		if src.Params["staged"] == "true" {
			return "git_files:staged"
		}
		if base := src.Params["base"]; base != "" {
			return "git_files:" + base
		}
		return "git_files"
	case "env":
		name := src.Params["name"]
		if name != "" {
			return "env:" + name
		}
		return "env"
	default:
		return src.Type
	}
}

// gatherSource fetches content for a single ContextSource.
func gatherSourceWithStats(src ContextSource, opts ContextOpts) (string, DiffStats, error) {
	if src.Type == "git_diff" {
		return gatherGitDiffStats(src.Params, opts)
	}
	out, err := gatherSourceText(src, opts)
	return out, DiffStats{}, err
}

func gatherSourceText(src ContextSource, opts ContextOpts) (string, error) {
	switch src.Type {
	case "git_diff":
		return gatherGitDiff(src.Params, opts)
	case "git_log":
		return gatherGitLog(src.Params)
	case "git_files":
		return gatherGitFiles(src.Params)
	case "agents_md":
		return gatherAgentsMD(src.Params, opts)
	case "env":
		return gatherEnv(src.Params)
	case "command":
		return gatherCommand(src.Params)
	default:
		return "", fmt.Errorf("unknown context source type: %s", src.Type)
	}
}

// diffSafetyArgs are appended to every "git diff" invocation whose output is
// parsed programmatically (by diffsignal, then read verbatim by a model):
//   - --no-ext-diff: a repository-local diff.external config otherwise runs
//     in place of git's own diff machinery, and its output (never a
//     parseable unified diff) becomes what diffsignal parses and the model
//     reads.
//   - --no-color: a repository-local color.ui=always config otherwise
//     injects ANSI escape sequences that diffsignal's literal "diff --git "
//     boundary matching does not expect.
func diffSafetyArgs() []string {
	return []string{"--no-ext-diff", "--no-textconv", "--no-color"}
}

// rangeRefs returns the base and head refs of a git range source. Head
// defaults to HEAD. A ref that starts with "-" would reach git as an option
// (for example --output=file), so it is rejected.
func rangeRefs(params map[string]string) (base, head string, err error) {
	base, head = params["base"], params["head"]
	if head == "" {
		head = "HEAD"
	}
	for _, ref := range []string{base, head} {
		if strings.HasPrefix(ref, "-") {
			return "", "", fmt.Errorf("git ref %q looks like an option", ref)
		}
	}
	return base, head, nil
}

// gatherGitDiff runs git diff with appropriate flags.
//
// The raw diff is reshaped by diffsignal before it reaches the model:
// low-signal bulk (binary, generated, minified files) is reduced to summary
// lines so it cannot starve hand-written changes out of the byte budget.
func gatherGitDiff(params map[string]string, opts ContextOpts) (string, error) {
	out, _, err := gatherGitDiffStats(params, opts)
	return out, err
}

func gatherGitDiffStats(params map[string]string, opts ContextOpts) (string, DiffStats, error) {
	var stats DiffStats
	base, head, err := rangeRefs(params)
	if err != nil {
		return "", stats, err
	}
	attrSource := headAttrSource(params)
	args := append([]string{"diff"}, diffSafetyArgs()...)

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if base != "" {
		args = append(args, base+"..."+head)
	}

	// Optional pathspec: "paths" param is a NUL-separated list of paths.
	var pathsArgs []string
	if rawPaths := params["paths"]; rawPaths != "" {
		for _, p := range strings.Split(rawPaths, "\x00") {
			if p != "" {
				pathsArgs = append(pathsArgs, p)
			}
		}
	}
	if len(pathsArgs) > 0 {
		args = append(args, "--")
		args = append(args, pathsArgs...)
	}

	diff, err := gitdiff.Capture(context.Background(), opts.WorkDir, contextGitArgs(params, attrSource, args)...)
	if err != nil {
		// Git exits 128 for a range with no merge base (unrelated or shallow
		// history). Compare the two commits directly: `git diff <base>` alone
		// would compare the base with the working tree.
		var exit *exec.ExitError
		if base != "" && errors.As(err, &exit) && exit.ExitCode() == 128 {
			args = append([]string{"diff"}, diffSafetyArgs()...)
			args = append(args, base, head)
			if len(pathsArgs) > 0 {
				args = append(args, "--")
				args = append(args, pathsArgs...)
			}
			diff, err = gitdiff.Capture(context.Background(), opts.WorkDir, contextGitArgs(params, attrSource, args)...)
		}
		if err != nil {
			return "", stats, err
		}
	}

	// Reserve space for the truncation marker so appending it never pushes the
	// output past MaxDiffBytes (marker is 16 bytes: "\n... (truncated)").
	const truncMarker = "\n... (truncated)"
	budget := opts.MaxDiffBytes
	if budget > len(truncMarker) {
		budget -= len(truncMarker)
	}
	var paths []string
	for _, fd := range diff.Files {
		paths = append(paths, fd.Path)
	}
	isGenerated := anyGenerated(
		generatedByAttrs(paths, params["staged"] == "true", attrSource, opts.WorkDir),
		diffsignal.DefaultGenerated(paths),
	)
	stats = statsForFiles(diff.Files, isGenerated)
	res := diff.Prioritize(budget, diffsignal.Options{ForPR: opts.RankDiffForPR, Generated: isGenerated})
	output := res.Context
	if res.Truncated {
		output += truncMarker
		if opts.MaxDiffBytes > 0 && len(output) > opts.MaxDiffBytes {
			output = output[:opts.MaxDiffBytes]
		}
	}
	return output, stats, nil
}

// gitLogBodyMaxLinesPerCommit bounds how many body lines gatherGitLog keeps
// per commit when include_body is requested: enough for a short verification
// note, not a full essay.
const gitLogBodyMaxLinesPerCommit = 12

// gitLogBodyMaxBytes bounds the total size of a body-included git log:
// large branches can carry hundreds of commits, and an unbounded log would
// itself consume a PR's diff-adjacent context budget.
const gitLogBodyMaxBytes = 20_000

// gitLogRecordSep and gitLogFieldSep delimit gatherGitLog's include_body
// format. Commit messages can contain arbitrary text but essentially never
// contain these ASCII control characters, so a body's own newlines never get
// confused with a field or record boundary.
const (
	gitLogRecordSep = "\x1e"
	gitLogFieldSep  = "\x1f"
)

// gatherGitLog runs git log. By default it matches the existing
// `git log --oneline` shape (hash + subject only). When
// params["include_body"] == "true", each commit's body is included too
// (bounded per commit and in total): a commit body is often where a
// contributor records the verification evidence (commands run, manual
// repro steps) that a PR synthesizing "what changed and why does it work"
// needs, and --oneline never surfaces it.
func gatherGitLog(params map[string]string) (string, error) {
	if params["include_body"] == "true" {
		return gatherGitLogWithBody(params)
	}

	base, head, err := rangeRefs(params)
	if err != nil {
		return "", err
	}
	args := []string{"log", "--oneline"}

	if base != "" {
		args = append(args, base+".."+head)
	} else {
		args = append(args, "-20", head)
	}

	output, err := contextGitOutput(args...)
	if err != nil {
		return "", nil // Log failures are non-fatal
	}
	return output, nil
}

func gatherGitLogWithBody(params map[string]string) (string, error) {
	base, head, err := rangeRefs(params)
	if err != nil {
		return "", err
	}
	format := "%h" + gitLogFieldSep + "%s" + gitLogFieldSep + "%b" + gitLogRecordSep
	args := []string{"log", "--no-color", "--pretty=format:" + format}

	if base != "" {
		args = append(args, base+".."+head)
	} else {
		args = append(args, "-20", head)
	}

	raw, err := contextGitOutput(args...)
	if err != nil {
		return "", nil // Log failures are non-fatal
	}
	return renderGitLogWithBody(raw), nil
}

// renderGitLogWithBody formats gatherGitLogWithBody's raw, delimiter-joined
// git output into readable "<hash> <subject>" + indented body text, applying
// the per-commit line cap and the total byte cap.
func renderGitLogWithBody(raw string) string {
	var b strings.Builder
	omitted := 0
	for _, record := range strings.Split(raw, gitLogRecordSep) {
		record = strings.Trim(record, "\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, gitLogFieldSep, 3)
		if len(fields) < 2 {
			continue
		}
		hash, subject := fields[0], fields[1]
		var body string
		if len(fields) == 3 {
			body = strings.Trim(fields[2], "\n")
		}

		var entry strings.Builder
		entry.WriteString(hash)
		entry.WriteString(" ")
		entry.WriteString(subject)
		if body != "" {
			lines := strings.Split(body, "\n")
			truncatedBody := len(lines) > gitLogBodyMaxLinesPerCommit
			if truncatedBody {
				lines = lines[:gitLogBodyMaxLinesPerCommit]
			}
			for _, line := range lines {
				entry.WriteString("\n    ")
				entry.WriteString(line)
			}
			if truncatedBody {
				entry.WriteString("\n    ...")
			}
		}
		entry.WriteString("\n")

		if b.Len()+entry.Len() > gitLogBodyMaxBytes {
			omitted++
			continue
		}
		b.WriteString(entry.String())
	}

	out := strings.TrimRight(b.String(), "\n")
	if omitted > 0 {
		out += fmt.Sprintf("\n... and %d more commits omitted (log body budget)", omitted)
	}
	return out
}

// gatherGitFiles runs git diff --name-status.
func gatherGitFiles(params map[string]string) (string, error) {
	base, head, err := rangeRefs(params)
	if err != nil {
		return "", err
	}
	attrSource := headAttrSource(params)
	args := append([]string{"diff", "--name-status"}, diffSafetyArgs()...)

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if base != "" {
		args = append(args, base+"..."+head)
	}

	// Optional pathspec: "paths" param is a NUL-separated list of paths.
	var pathsArgs []string
	if rawPaths := params["paths"]; rawPaths != "" {
		for _, p := range strings.Split(rawPaths, "\x00") {
			if p != "" {
				pathsArgs = append(pathsArgs, p)
			}
		}
	}
	if len(pathsArgs) > 0 {
		args = append(args, "--")
		args = append(args, pathsArgs...)
	}

	output, err := contextGitOutput(contextGitArgs(params, attrSource, args)...)
	if err != nil {
		// No merge base (unrelated or shallow history): compare the two
		// commits directly rather than the base with the working tree.
		if base != "" {
			args = append([]string{"diff", "--name-status"}, diffSafetyArgs()...)
			args = append(args, base, head)
			if len(pathsArgs) > 0 {
				args = append(args, "--")
				args = append(args, pathsArgs...)
			}
			output, err = contextGitOutput(contextGitArgs(params, attrSource, args)...)
		}
		if err != nil {
			return "", err
		}
	}
	return output, nil
}

// agentsMDMaxBytes bounds the AGENTS.md text that reaches the prompt.
const agentsMDMaxBytes = 10_000

// gatherAgentsMD reads AGENTS.md from the repository root. With a "head" param
// it reads the file as committed at that ref, so a dirty or different working
// tree cannot change what the model is told. Without one it reads the working
// tree file.
func gatherAgentsMD(params map[string]string, opts ContextOpts) (string, error) {
	if head := params["head"]; head != "" {
		return gatherAgentsMDAtRef(head)
	}
	root, err := contextGitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil // Not in a repo is non-fatal for this source
	}
	root = strings.TrimSpace(root)

	agentsPath := filepath.Join(root, "AGENTS.md")
	content, err := contextReadFileLimited(agentsPath, agentsMDMaxBytes)
	if err != nil {
		return "", nil // Missing AGENTS.md is non-fatal
	}
	return content, nil
}

// gatherAgentsMDAtRef reads AGENTS.md as committed at ref. A symlink is
// followed one hop inside the repository, as reading the working tree file
// would; git show alone prints the link target instead of the file.
func gatherAgentsMDAtRef(ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("git ref %q looks like an option", ref)
	}
	name := "AGENTS.md"
	mode, err := contextTreeMode(ref, name)
	if err != nil {
		return "", nil // Missing AGENTS.md is non-fatal
	}
	if mode == gitModeSymlink {
		link, err := contextShowLimited(ref, name, maxSymlinkTargetBytes)
		if err != nil {
			return "", nil
		}
		name = gopath.Join(gopath.Dir(name), strings.TrimSpace(link))
		if gopath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return "", nil
		}
		if mode, err = contextTreeMode(ref, name); err != nil {
			return "", nil
		}
	}
	if mode != gitModeFile && mode != gitModeExecutable {
		return "", nil
	}
	content, err := contextShowLimited(ref, name, agentsMDMaxBytes)
	if err != nil {
		return "", nil
	}
	return content, nil
}

const (
	gitModeFile           = "100644"
	gitModeExecutable     = "100755"
	gitModeSymlink        = "120000"
	maxSymlinkTargetBytes = 4096
)

// contextTreeMode returns the git file mode of path in ref's tree, with path
// taken from the repository root.
func contextTreeMode(ref, path string) (string, error) {
	out, err := contextGitOutput("ls-tree", "--full-tree", ref, "--", path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("%s is not in %s", path, ref)
	}
	return fields[0], nil
}

// contextShowLimited returns at most maxBytes of path as committed at ref. It
// stops reading at the limit, so a large committed file costs no more than
// reading it from the working tree with head -c did.
func contextShowLimited(ref, path string, maxBytes int) (string, error) {
	cmd := exec.Command("git", "--no-pager", "show", "--no-textconv", ref+":"+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxBytes)))
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if readErr != nil {
		return "", readErr
	}
	if len(data) < maxBytes && waitErr != nil {
		return "", waitErr
	}
	return string(data), nil
}

// gatherEnv reads an environment variable.
func gatherEnv(params map[string]string) (string, error) {
	name := params["name"]
	if name == "" {
		return "", fmt.Errorf("env source requires 'name' param")
	}
	return os.Getenv(name), nil
}

// gatherCommand runs a shell command and returns its output.
func gatherCommand(params map[string]string) (string, error) {
	cmdStr := params["cmd"]
	if cmdStr == "" {
		return "", fmt.Errorf("command source requires 'cmd' param")
	}

	cmd := exec.Command("sh", "-c", cmdStr)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("command %q failed: %w", cmdStr, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// contextGitArgs puts the global git options a source needs in front of the
// subcommand in args: literal pathspecs, and the tree that supplies
// gitattributes (empty means the working tree).
func contextGitArgs(params map[string]string, attrSource string, args []string) []string {
	var global []string
	if params["literal_paths"] == "true" {
		global = append(global, "--literal-pathspecs")
	}
	global = append(global, attrSourceArgs(attrSource)...)
	return append(global, args...)
}

// contextGitOutput runs a git command and returns its output.
func contextGitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

// contextReadFileLimited reads up to maxBytes from a file.
func contextReadFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// contextEstimateTokens provides a rough token estimate (~4 chars per token).
func contextEstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// StatsForDiff classifies every file in a unified diff. generated marks paths
// that gitattributes call generated; nil means path and content rules only.
func StatsForDiff(raw string, generated func(path string) bool) DiffStats {
	return statsForFiles(diffsignal.SplitWith(raw, generated), nil)
}

func statsForFiles(files []diffsignal.FileDiff, generated func(string) bool) DiffStats {
	var stats DiffStats
	for _, fd := range files {
		if generated != nil && generated(fd.Path) {
			fd.Reason = diffsignal.ReasonGeneratedAttr
		}
		stats.Files++
		if fd.LowSignal() {
			stats.LowSignal++
		}
		if fd.Generated() {
			stats.GeneratedPaths = append(stats.GeneratedPaths, fd.Path)
			if fd.GeneratedBy != "" && !slices.Contains(stats.GeneratedBy, fd.GeneratedBy) {
				stats.GeneratedBy = append(stats.GeneratedBy, fd.GeneratedBy)
			}
		} else {
			stats.SourcePaths = append(stats.SourcePaths, fd.Path)
		}
	}
	return stats
}

// anyGenerated combines predicates: a path is generated when any says so. Nil
// predicates are skipped, and the result is nil when none remain.
func anyGenerated(predicates ...func(string) bool) func(string) bool {
	var live []func(string) bool
	for _, p := range predicates {
		if p != nil {
			live = append(live, p)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	}
	return func(path string) bool {
		for _, p := range live {
			if p(path) {
				return true
			}
		}
		return false
	}
}
