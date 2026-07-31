package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const gotreesitterModule = "github.com/odvcencio/gotreesitter"

// grammarProbeSource is written to a temp file and run with the target
// repo's directory as the working directory, so `go run` resolves imports
// against that repo's own module graph (its go.mod/go.sum) without ever
// modifying a file inside the repo itself.
const grammarProbeSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	langs := grammars.AllLanguages()
	names := make([]string, 0, len(langs))
	for _, l := range langs {
		names = append(names, l.Name)
	}
	sort.Strings(names)
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(map[string]any{"count": len(names), "names": names}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

// RepoInfo captures the per-repository measurements the PR 0 baseline
// records: identity, resolved gotreesitter version, and grammar coverage.
// Available is false (with Reason set) when the repo directory could not be
// measured, matching the spec's "external components degrade gracefully"
// design decision rather than failing the whole benchmark run.
type RepoInfo struct {
	Name                string          `json:"name"`
	Path                string          `json:"path,omitempty"`
	Available           bool            `json:"available"`
	Reason              string          `json:"reason,omitempty"`
	Module              string          `json:"module,omitempty"`
	GoDirective         string          `json:"go_directive,omitempty"`
	GitCommit           string          `json:"git_commit,omitempty"`
	GitBranch           string          `json:"git_branch,omitempty"`
	GitDirty            bool            `json:"git_dirty"`
	GotreesitterVersion string          `json:"gotreesitter_version,omitempty"`
	GotreesitterDirect  bool            `json:"gotreesitter_direct"`
	Grammars            *GrammarSummary `json:"grammars,omitempty"`
	Build               *BuildResult    `json:"build,omitempty"`
}

// GrammarSummary is the gotreesitter grammar catalog visible to a repo's
// resolved module version, built with default (untagged) settings.
type GrammarSummary struct {
	Count         int      `json:"count"`
	Names         []string `json:"names"`
	BuildTagsNote string   `json:"build_tags_note"`
}

func inspectRepo(name, dir string) *RepoInfo {
	info := &RepoInfo{Name: name, Path: dir}
	if dir == "" {
		info.Reason = "no directory configured"
		return info
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		info.Reason = fmt.Sprintf("directory not found: %s", dir)
		return info
	}
	info.Available = true

	if mod, goDirective, err := readGoMod(dir); err == nil {
		info.Module = mod
		info.GoDirective = goDirective
	}

	if commit, err := gitOutput(dir, "rev-parse", "HEAD"); err == nil {
		info.GitCommit = commit
	}
	if branch, err := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		info.GitBranch = branch
	}
	if status, err := gitOutput(dir, "status", "--porcelain"); err == nil {
		info.GitDirty = status != ""
	}

	if version, direct, err := gotreesitterVersion(dir); err == nil {
		info.GotreesitterVersion = version
		info.GotreesitterDirect = direct
	}

	if summary, err := grammarCoverage(dir); err == nil {
		info.Grammars = summary
	}

	return info
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// readGoMod extracts the module path and go directive without adding a
// golang.org/x/mod dependency; the file format for these two lines is
// stable and simple to parse directly.
func readGoMod(dir string) (module, goDirective string, err error) {
	data, err := os.ReadFile(dir + "/go.mod")
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "module "):
			module = strings.TrimSpace(strings.TrimPrefix(line, "module"))
		case strings.HasPrefix(line, "go "):
			goDirective = strings.TrimSpace(strings.TrimPrefix(line, "go"))
		}
	}
	return module, goDirective, nil
}

// gotreesitterVersion asks `go list -m` for the MVS-resolved gotreesitter
// version in dir's module graph, and reports whether the module's own
// go.mod requires it directly (vs. only indirectly, through a dependency).
func gotreesitterVersion(dir string) (version string, direct bool, err error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", gotreesitterModule)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("go list -m %s: %w: %s", gotreesitterModule, err, stderr.String())
	}
	version = strings.TrimSpace(out.String())

	data, readErr := os.ReadFile(dir + "/go.mod")
	if readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, gotreesitterModule+" ") && !strings.Contains(trimmed, "// indirect") {
				direct = true
				break
			}
		}
	}
	return version, direct, nil
}

// grammarCoverage runs a throwaway probe program (never written inside dir)
// with dir as its working directory, so Go resolves the grammars import
// against that repo's own module graph. It measures the default (untagged)
// registry; production builds may apply a curated subset via build tags
// (e.g. canopy's `grammar_subset` tag, see its Dockerfile).
func grammarCoverage(dir string) (*GrammarSummary, error) {
	tmp, err := os.CreateTemp("", "ctxfabric-grammar-probe-*.go")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(grammarProbeSource); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "run", tmp.Name())
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("grammar probe: %w: %s", err, stderr.String())
	}

	var parsed struct {
		Count int      `json:"count"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("parsing grammar probe output: %w", err)
	}
	sort.Strings(parsed.Names)
	return &GrammarSummary{
		Count: parsed.Count,
		Names: parsed.Names,
		BuildTagsNote: "default (untagged) registry; production builds may apply a " +
			"curated grammar subset via build tags",
	}, nil
}
