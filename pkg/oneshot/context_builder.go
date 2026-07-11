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

// gatherGitDiff runs git diff with appropriate flags.
//
// The raw diff is reshaped by diffsignal before it reaches the model:
// low-signal bulk (binary, generated, minified files) is reduced to summary
// lines so it cannot starve hand-written changes out of the byte budget.
func gatherGitDiff(params map[string]string, opts ContextOpts) (string, error) {
	args := []string{"diff"}

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if revisionRange := params["range"]; revisionRange != "" {
		args = append(args, revisionRange)
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

	output, rawTruncated, err := contextGitOutputLimited(diffsignal.MaxParseBytes, args...)
	if err != nil {
		// A caller-supplied exact range must fail closed rather than silently
		// widening to a mutable branch. Legacy base-only sources retain their
		// historical fallback.
		if params["range"] == "" {
			base := params["base"]
			if base == "" {
				return "", err
			}
			args = []string{"diff", base}
			if len(pathsArgs) > 0 {
				args = append(args, "--")
				args = append(args, pathsArgs...)
			}
			output, rawTruncated, err = contextGitOutputLimited(diffsignal.MaxParseBytes, args...)
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
	res := diffsignal.Prioritize(output, budget)
	output = res.Context
	if rawTruncated || res.Truncated {
		output += truncMarker
	}
	return output, nil
}

// gatherGitLog runs git log.
func gatherGitLog(params map[string]string) (string, error) {
	args := []string{"log", "--oneline"}

	if revisionRange := params["range"]; revisionRange != "" {
		args = append(args, revisionRange)
	} else if base := params["base"]; base != "" {
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

// gatherGitFiles runs git diff --name-status.
func gatherGitFiles(params map[string]string) (string, error) {
	args := []string{"diff", "--name-status"}

	if params["staged"] == "true" {
		args = append(args, "--cached")
	} else if revisionRange := params["range"]; revisionRange != "" {
		args = append(args, revisionRange)
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
		// Exact ranges fail closed; only legacy base-only sources may fall back.
		if params["range"] == "" {
			base := params["base"]
			if base == "" {
				return "", err
			}
			args = []string{"diff", "--name-status", base}
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

// contextGitOutputLimited runs a git command with output size limit.
func contextGitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", false, err
	}

	if len(output) > maxBytes {
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
