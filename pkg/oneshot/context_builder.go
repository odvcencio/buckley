package oneshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
		content, err := gatherSource(src, opts)
		if err != nil {
			return nil, fmt.Errorf("gathering %s: %w", src.Type, err)
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
func gatherSource(src ContextSource, opts ContextOpts) (string, error) {
	switch src.Type {
	case "git_diff":
		return gatherGitDiff(src.Params, opts)
	case "git_log":
		return gatherGitLog(src.Params)
	case "git_files":
		return gatherGitFiles(src.Params)
	case "agents_md":
		return gatherAgentsMD(opts)
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
	return []string{"--no-ext-diff", "--no-color"}
}

// gatherGitDiff runs git diff with appropriate flags.
//
// The raw diff is reshaped by diffsignal before it reaches the model:
// low-signal bulk (binary, generated, minified files) is reduced to summary
// lines so it cannot starve hand-written changes out of the byte budget.
func gatherGitDiff(params map[string]string, opts ContextOpts) (string, error) {
	args := append([]string{"diff"}, diffSafetyArgs()...)

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if base := params["base"]; base != "" {
		args = append(args, base+"...HEAD")
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

	// The raw git output is captured in full here (no size limit): Prioritize
	// itself enforces diffsignal.MaxParseBytes on its raw input, and unlike a
	// blind byte-cut at capture time, it does so at file boundaries and stub-
	// summarizes every file beyond that cutoff (scanBoundariesBeyond) instead
	// of silently discarding them. Capping raw capture here duplicated that
	// cutoff at a point before Prioritize ever saw the tail files, so they
	// vanished with no summary line at all (Important-2).
	output, _, err := contextGitOutputLimited(0, args...)
	if err != nil {
		// Retry without ...HEAD for base diff
		if base := params["base"]; base != "" {
			args = append([]string{"diff"}, diffSafetyArgs()...)
			args = append(args, base)
			if len(pathsArgs) > 0 {
				args = append(args, "--")
				args = append(args, pathsArgs...)
			}
			output, _, err = contextGitOutputLimited(0, args...)
		}
		if err != nil {
			return "", err
		}
	}

	// Reserve space for the truncation marker so appending it never pushes the
	// output past MaxDiffBytes (marker is 16 bytes: "\n... (truncated)").
	const truncMarker = "\n... (truncated)"
	budget := opts.MaxDiffBytes
	if budget > len(truncMarker) {
		budget -= len(truncMarker)
	}
	var res diffsignal.Result
	if opts.RankDiffForPR {
		res = diffsignal.PrioritizeForPR(output, budget)
	} else {
		res = diffsignal.Prioritize(output, budget)
	}
	output = res.Context
	if res.Truncated {
		output += truncMarker
	}
	return output, nil
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

	args := []string{"log", "--oneline"}

	if base := params["base"]; base != "" {
		args = append(args, base+"..HEAD")
	} else {
		args = append(args, "-20")
	}

	output, err := contextGitOutput(args...)
	if err != nil {
		return "", nil // Log failures are non-fatal
	}
	return output, nil
}

func gatherGitLogWithBody(params map[string]string) (string, error) {
	format := "%h" + gitLogFieldSep + "%s" + gitLogFieldSep + "%b" + gitLogRecordSep
	args := []string{"log", "--no-color", "--pretty=format:" + format}

	if base := params["base"]; base != "" {
		args = append(args, base+"..HEAD")
	} else {
		args = append(args, "-20")
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
	args := append([]string{"diff", "--name-status"}, diffSafetyArgs()...)

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if base := params["base"]; base != "" {
		args = append(args, base+"...HEAD")
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

	output, err := contextGitOutput(args...)
	if err != nil {
		// Retry without ...HEAD for base
		if base := params["base"]; base != "" {
			args = append([]string{"diff", "--name-status"}, diffSafetyArgs()...)
			args = append(args, base)
			if len(pathsArgs) > 0 {
				args = append(args, "--")
				args = append(args, pathsArgs...)
			}
			output, err = contextGitOutput(args...)
		}
		if err != nil {
			return "", err
		}
	}
	return output, nil
}

// gatherAgentsMD reads AGENTS.md from the repository root.
func gatherAgentsMD(opts ContextOpts) (string, error) {
	root, err := contextGitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil // Not in a repo is non-fatal for this source
	}
	root = strings.TrimSpace(root)

	agentsPath := filepath.Join(root, "AGENTS.md")
	content, err := contextReadFileLimited(agentsPath, 10_000)
	if err != nil {
		return "", nil // Missing AGENTS.md is non-fatal
	}
	return content, nil
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

// contextGitOutput runs a git command and returns its output.
func contextGitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// contextGitOutputLimited runs a git command, optionally capping output at
// maxBytes. maxBytes <= 0 means no limit: the full stdout is returned and
// truncated is always false. Callers that need a byte ceiling AND
// boundary-aware stub summaries for what falls past it (rather than a blind
// mid-content cut) should apply that ceiling downstream, e.g. via
// diffsignal.Prioritize's own MaxParseBytes handling, not here.
func contextGitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", false, err
	}

	if maxBytes > 0 && len(output) > maxBytes {
		return string(output[:maxBytes]), true, nil
	}
	return strings.TrimSpace(string(output)), false, nil
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
