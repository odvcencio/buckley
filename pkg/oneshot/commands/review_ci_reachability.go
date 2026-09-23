package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/reviewpolicy"
)

const maxPRTestLogBytes = 8 << 20
const maxPRGoTestFiles = 32

type requiredCheckLink struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Link  string `json:"link"`
}

type prTestRun struct {
	HeadSHA    string `json:"headSha"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Jobs       []struct {
		DatabaseID int64  `json:"databaseId"`
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Steps      []struct {
			Name        string    `json:"name"`
			Conclusion  string    `json:"conclusion"`
			StartedAt   time.Time `json:"startedAt"`
			CompletedAt time.Time `json:"completedAt"`
		} `json:"steps"`
	} `json:"jobs"`
}

func capturePRGoTestReachability(run prCommandRunner, target prReference, pr *PRInfo, changedFiles []string) (*reviewpolicy.CIReachabilityEvidence, error) {
	files := recognizedChangedTestFiles(changedFiles)
	if pr == nil || len(files) == 0 {
		return nil, nil
	}
	if len(files) > maxPRGoTestFiles {
		return nil, fmt.Errorf("%d changed tests exceed Go reachability evidence limit", len(files))
	}
	for _, file := range changedFiles {
		if file == "scripts/test.sh" || file == ".github/workflows/ci.yml" {
			return nil, fmt.Errorf("Go test CI entrypoint changed in this PR")
		}
	}
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			return nil, fmt.Errorf("unsupported test file %s", file)
		}
	}
	args := withPRTarget([]string{"pr", "checks", strconv.Itoa(target.Number), "--json", "name,state,link", "--required"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return nil, fmt.Errorf("fetch required check links: %w", err)
	}
	var links []requiredCheckLink
	if err := json.Unmarshal(output, &links); err != nil {
		return nil, fmt.Errorf("decode required check links: %w", err)
	}
	if err := verifyPRGoTestTree(run, pr, files); err != nil {
		return nil, err
	}
	module, err := readPRGoModule(run, pr)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if err := verifyPRGoTestFile(run, pr, file); err != nil {
			return nil, err
		}
	}
	for _, check := range links {
		if check.State != "SUCCESS" && check.State != "PASS" {
			continue
		}
		runID, jobID, err := parsePRRequiredJobLink(check.Link, pr)
		if err != nil {
			continue
		}
		if err := verifyPRTestWorkflow(run, pr, runID); err != nil {
			continue
		}
		metadata, err := readPRTestRun(run, target, runID)
		if err != nil || metadata.HeadSHA != pr.HeadSHA || metadata.Event != "pull_request" ||
			metadata.Status != "completed" || metadata.Conclusion != "success" {
			continue
		}
		for _, job := range metadata.Jobs {
			if job.DatabaseID != jobID || job.Name != check.Name || job.Conclusion != "success" {
				continue
			}
			for _, step := range job.Steps {
				if step.Name != "Run Go tests" || step.Conclusion != "success" || !step.CompletedAt.After(step.StartedAt) {
					continue
				}
				packages, err := readPRGoTestPackages(run, target, jobID, step.StartedAt, step.CompletedAt)
				if err != nil {
					continue
				}
				return &reviewpolicy.CIReachabilityEvidence{
					Source: "github_actions_go_test_v1", HeadSHA: pr.HeadSHA,
					RunID: runID, JobID: jobID, Check: check.Name,
					Module: module, Packages: packages,
				}, nil
			}
		}
	}
	return nil, fmt.Errorf("no completed required Go test job for PR head %s", pr.HeadSHA)
}

func verifyPRTestWorkflow(run prCommandRunner, pr *PRInfo, runID int64) error {
	endpoint := "repos/" + pr.Repository + "/actions/runs/" + strconv.FormatInt(runID, 10)
	output, err := run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return err
	}
	var data struct {
		Path    string `json:"path"`
		HeadSHA string `json:"head_sha"`
		Event   string `json:"event"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return err
	}
	if data.Path != ".github/workflows/ci.yml" || data.HeadSHA != pr.HeadSHA || data.Event != "pull_request" {
		return fmt.Errorf("required check is not from the expected CI workflow and PR head")
	}
	return nil
}

func verifyPRGoTestTree(run prCommandRunner, pr *PRInfo, files []string) error {
	endpoint := "repos/" + pr.Repository + "/git/trees/" + url.PathEscape(pr.HeadSHA) + "?recursive=1"
	output, err := run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return fmt.Errorf("read head tree: %w", err)
	}
	if len(output) > 16<<20 {
		return fmt.Errorf("head tree exceeds reachability evidence limit")
	}
	var tree struct {
		Truncated bool `json:"truncated"`
		Entries   []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(output, &tree); err != nil || tree.Truncated {
		return fmt.Errorf("head tree is unavailable or truncated")
	}
	entries := make(map[string]bool, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Type == "blob" && (entry.Mode == "100644" || entry.Mode == "100755") {
			entries[entry.Path] = true
		}
	}
	if !entries["go.mod"] {
		return fmt.Errorf("head go.mod is not a regular file")
	}
	for _, file := range files {
		if !entries[file] {
			return fmt.Errorf("changed Go test %s is not a regular head file", file)
		}
		for dir := path.Dir(file); dir != "."; dir = path.Dir(dir) {
			if entries[dir+"/go.mod"] {
				return fmt.Errorf("Go test %s is in a nested module", file)
			}
		}
	}
	return nil
}

func parsePRRequiredJobLink(raw string, pr *PRInfo) (int64, int64, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, pr.Host) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, 0, fmt.Errorf("required check link is not on the PR host")
	}
	want := "/" + strings.Trim(pr.Repository, "/") + "/actions/runs/"
	if !strings.HasPrefix(parsed.Path, want) {
		return 0, 0, fmt.Errorf("required check link is not for the PR repository")
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, want), "/")
	if len(parts) != 3 || parts[1] != "job" {
		return 0, 0, fmt.Errorf("required check link has no run/job pair")
	}
	runID, runErr := strconv.ParseInt(parts[0], 10, 64)
	jobID, jobErr := strconv.ParseInt(parts[2], 10, 64)
	if runErr != nil || jobErr != nil || runID <= 0 || jobID <= 0 {
		return 0, 0, fmt.Errorf("required check link has invalid run/job IDs")
	}
	return runID, jobID, nil
}

func readPRTestRun(run prCommandRunner, target prReference, runID int64) (prTestRun, error) {
	args := withPRTarget([]string{"run", "view", strconv.FormatInt(runID, 10), "--json", "headSha,event,status,conclusion,jobs"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return prTestRun{}, err
	}
	var data prTestRun
	if err := json.Unmarshal(output, &data); err != nil {
		return prTestRun{}, err
	}
	return data, nil
}

func readPRGoTestPackages(run prCommandRunner, target prReference, jobID int64, start, end time.Time) ([]string, error) {
	args := withPRTarget([]string{"run", "view", "--job", strconv.FormatInt(jobID, 10), "--log"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}
	if len(output) > maxPRTestLogBytes {
		return nil, fmt.Errorf("Go test log exceeds %d bytes", maxPRTestLogBytes)
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		fields := strings.Fields(parts[2])
		if len(fields) != 4 || fields[1] != "ok" {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, fields[0])
		if err != nil || stamp.Before(start) || stamp.After(end) {
			continue
		}
		if fields[3] == "(cached)" || validGoTestDuration(fields[3]) {
			seen[fields[2]] = true
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("Go test step has no package results")
	}
	packages := make([]string, 0, len(seen))
	for pkg := range seen {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	return packages, nil
}

func validGoTestDuration(value string) bool {
	if !strings.HasSuffix(value, "s") {
		return false
	}
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(value, "s"), 64)
	return err == nil && seconds >= 0 && !math.IsInf(seconds, 0)
}

func readPRGoModule(run prCommandRunner, pr *PRInfo) (string, error) {
	content, err := readPRHeadRawFile(run, pr, "go.mod")
	if err != nil {
		return "", fmt.Errorf("read head go.mod: %w", err)
	}
	for _, line := range strings.Split(content, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("head go.mod has no module directive")
}

func verifyPRGoTestFile(run prCommandRunner, pr *PRInfo, file string) error {
	if strings.Contains(file, "\\") || path.IsAbs(file) || path.Clean(file) != file || strings.HasPrefix(file, "../") {
		return fmt.Errorf("invalid changed test path %s", file)
	}
	base := strings.TrimSuffix(path.Base(file), "_test.go")
	for _, suffix := range strings.Split(base, "_") {
		if goPlatformSuffixes[suffix] {
			return fmt.Errorf("platform-specific Go test %s needs explicit CI file evidence", file)
		}
	}
	content, err := readPRHeadRawFile(run, pr, file)
	if err != nil {
		return fmt.Errorf("read head test file %s: %w", file, err)
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//go:build") || strings.HasPrefix(trimmed, "// +build") {
			return fmt.Errorf("build-constrained Go test %s needs explicit CI file evidence", file)
		}
	}
	return nil
}

var goPlatformSuffixes = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true,
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mips64": true, "mips64le": true, "mipsle": true,
	"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true,
}

func readPRHeadRawFile(run prCommandRunner, pr *PRInfo, file string) (string, error) {
	segments := strings.Split(file, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	endpoint := "repos/" + pr.Repository + "/contents/" + strings.Join(segments, "/") + "?ref=" + url.QueryEscape(pr.HeadSHA)
	args := withPRAPIHostname([]string{"api", endpoint, "-H", "Accept: application/vnd.github.raw+json"}, pr.Host)
	output, err := run("gh", args...)
	if err != nil {
		return "", err
	}
	if len(output) > 1<<20 {
		return "", fmt.Errorf("head file %s exceeds reachability evidence limit", file)
	}
	return string(output), nil
}
