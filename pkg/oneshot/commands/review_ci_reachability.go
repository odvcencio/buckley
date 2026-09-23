package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/reviewpolicy"
)

const maxPRGoTestFiles = 32

const (
	buckleyCITestScriptSHA256 = "689e4d66670f6091f441e9f31095219f602a81c99d79c2662a3f7d76c54a5d74"
	buckleyCIWorkflowSHA256   = "e1556b2688783110439606468f2f7ecf2149292d39040dc3c0ffd4b2916e7859"
)

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
		if strings.HasPrefix(file, "scripts/") || strings.HasPrefix(file, ".github/workflows/") {
			return nil, fmt.Errorf("Go test CI workflow or script changed in this PR")
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
	mergeSHA, err := readPRCIMergeCommit(run, pr)
	if err != nil {
		return nil, err
	}
	if err := verifyPRCIContract(run, pr, mergeSHA); err != nil {
		return nil, err
	}
	treeFiles, err := verifyPRGoTestTree(run, pr, mergeSHA, files)
	if err != nil {
		return nil, err
	}
	module, err := readPRGoModule(run, pr, mergeSHA)
	if err != nil {
		return nil, err
	}
	packages, err := provePRGoTestPackages(run, pr, mergeSHA, module, files, treeFiles)
	if err != nil {
		return nil, err
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
				return &reviewpolicy.CIReachabilityEvidence{
					Source: "buckley_ci_go_test_v1", HeadSHA: pr.HeadSHA, MergeSHA: mergeSHA,
					RunID: runID, JobID: jobID, Check: check.Name,
					Module: module, Packages: packages,
				}, nil
			}
		}
	}
	return nil, fmt.Errorf("no completed required Go test job for PR head %s", pr.HeadSHA)
}

func readPRCIMergeCommit(run prCommandRunner, pr *PRInfo) (string, error) {
	endpoint := "repos/" + pr.Repository + "/pulls/" + strconv.Itoa(pr.Number)
	output, err := run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return "", fmt.Errorf("read PR merge commit: %w", err)
	}
	var pull struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if err := json.Unmarshal(output, &pull); err != nil || pull.MergeCommitSHA == "" {
		return "", fmt.Errorf("PR merge commit is unavailable")
	}
	endpoint = "repos/" + pr.Repository + "/git/commits/" + url.PathEscape(pull.MergeCommitSHA)
	output, err = run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return "", fmt.Errorf("read PR merge commit parents: %w", err)
	}
	var commit struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := json.Unmarshal(output, &commit); err != nil || len(commit.Parents) != 2 ||
		commit.Parents[0].SHA != pr.BaseSHA || commit.Parents[1].SHA != pr.HeadSHA {
		return "", fmt.Errorf("PR merge commit is not from the current base and head")
	}
	return pull.MergeCommitSHA, nil
}

func verifyPRCIContract(run prCommandRunner, pr *PRInfo, mergeSHA string) error {
	for _, item := range []struct {
		path string
		sha  string
	}{
		{"scripts/test.sh", buckleyCITestScriptSHA256},
		{".github/workflows/ci.yml", buckleyCIWorkflowSHA256},
	} {
		content, err := readPRSnapshotRawFile(run, pr, mergeSHA, item.path)
		if err != nil {
			return fmt.Errorf("read CI contract %s: %w", item.path, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(content))); got != item.sha {
			return fmt.Errorf("CI contract %s differs from the verified Go test command", item.path)
		}
	}
	return nil
}

func verifyPRTestWorkflow(run prCommandRunner, pr *PRInfo, runID int64) error {
	endpoint := "repos/" + pr.Repository + "/actions/runs/" + strconv.FormatInt(runID, 10)
	output, err := run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return err
	}
	var data struct {
		Path         string `json:"path"`
		HeadSHA      string `json:"head_sha"`
		Event        string `json:"event"`
		PullRequests []struct {
			Number int `json:"number"`
			Base   struct {
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return err
	}
	if data.Path != ".github/workflows/ci.yml" || data.HeadSHA != pr.HeadSHA || data.Event != "pull_request" {
		return fmt.Errorf("required check is not from the expected CI workflow and PR head")
	}
	for _, pull := range data.PullRequests {
		if pull.Number == pr.Number && pull.Base.SHA == pr.BaseSHA && pull.Head.SHA == pr.HeadSHA {
			return nil
		}
	}
	return fmt.Errorf("required check is not from the current PR base and head")
}

func verifyPRGoTestTree(run prCommandRunner, pr *PRInfo, mergeSHA string, files []string) (map[string]bool, error) {
	endpoint := "repos/" + pr.Repository + "/git/trees/" + url.PathEscape(mergeSHA) + "?recursive=1"
	output, err := run("gh", withPRAPIHostname([]string{"api", endpoint}, pr.Host)...)
	if err != nil {
		return nil, fmt.Errorf("read PR merge tree: %w", err)
	}
	if len(output) > 16<<20 {
		return nil, fmt.Errorf("PR merge tree exceeds reachability evidence limit")
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
		return nil, fmt.Errorf("PR merge tree is unavailable or truncated")
	}
	entries := make(map[string]bool, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Type == "blob" && (entry.Mode == "100644" || entry.Mode == "100755") {
			entries[entry.Path] = true
		}
	}
	if !entries["go.mod"] {
		return nil, fmt.Errorf("PR merge go.mod is not a regular file")
	}
	for _, file := range files {
		if !entries[file] {
			return nil, fmt.Errorf("changed Go test %s is not a regular PR merge file", file)
		}
		for dir := path.Dir(file); dir != "."; dir = path.Dir(dir) {
			if entries[dir+"/go.mod"] {
				return nil, fmt.Errorf("Go test %s is in a nested module", file)
			}
		}
	}
	return entries, nil
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

func provePRGoTestPackages(run prCommandRunner, pr *PRInfo, mergeSHA, module string, files []string, treeFiles map[string]bool) ([]string, error) {
	seen := make(map[string]bool)
	for _, file := range files {
		if err := verifyPRGoTestFile(run, pr, mergeSHA, file); err != nil {
			return nil, err
		}
		dir := path.Dir(file)
		if !goTestScriptIncludesDir(dir) {
			return nil, fmt.Errorf("Go test %s is outside scripts/test.sh package targets", file)
		}
		if seen[dir] {
			continue
		}
		candidates := make([]string, 0)
		for candidate := range treeFiles {
			base := path.Base(candidate)
			if path.Dir(candidate) == dir && strings.HasSuffix(candidate, ".go") && !strings.HasSuffix(candidate, "_test.go") &&
				!strings.HasPrefix(base, ".") && !strings.HasPrefix(base, "_") && !platformSpecificGoFile(candidate) {
				candidates = append(candidates, candidate)
			}
		}
		sort.Strings(candidates)
		buildable := false
		for index, candidate := range candidates {
			if index == 16 {
				break
			}
			content, err := readPRSnapshotRawFile(run, pr, mergeSHA, candidate)
			if err == nil && plainPRGoSource(content) {
				buildable = true
				break
			}
		}
		if !buildable {
			return nil, fmt.Errorf("no plain Go source proves package %s is selected by scripts/test.sh", dir)
		}
		seen[dir] = true
	}
	packages := make([]string, 0, len(seen))
	for dir := range seen {
		packages = append(packages, module+"/"+dir)
	}
	sort.Strings(packages)
	return packages, nil
}

func goTestScriptIncludesDir(dir string) bool {
	if dir == "cmd/buckley" {
		return true
	}
	if dir != "pkg" && !strings.HasPrefix(dir, "pkg/") {
		return false
	}
	for _, part := range strings.Split(dir, "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") || part == "testdata" || part == "vendor" {
			return false
		}
	}
	return true
}

func plainPRGoSource(content string) bool {
	if hasGoBuildConstraint(content) {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), "", content, parser.ImportsOnly)
	if err != nil {
		return false
	}
	for _, imported := range file.Imports {
		if imported.Path.Value == `"C"` {
			return false
		}
	}
	return true
}

func readPRGoModule(run prCommandRunner, pr *PRInfo, mergeSHA string) (string, error) {
	content, err := readPRSnapshotRawFile(run, pr, mergeSHA, "go.mod")
	if err != nil {
		return "", fmt.Errorf("read PR merge go.mod: %w", err)
	}
	for _, line := range strings.Split(content, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("PR merge go.mod has no module directive")
}

func verifyPRGoTestFile(run prCommandRunner, pr *PRInfo, mergeSHA, file string) error {
	if strings.Contains(file, "\\") || path.IsAbs(file) || path.Clean(file) != file || strings.HasPrefix(file, "../") {
		return fmt.Errorf("invalid changed test path %s", file)
	}
	if base := path.Base(file); strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return fmt.Errorf("Go ignores changed test file %s", file)
	}
	if platformSpecificGoFile(file) {
		return fmt.Errorf("platform-specific Go test %s needs explicit CI file evidence", file)
	}
	content, err := readPRSnapshotRawFile(run, pr, mergeSHA, file)
	if err != nil {
		return fmt.Errorf("read PR merge test file %s: %w", file, err)
	}
	if hasGoBuildConstraint(content) {
		return fmt.Errorf("build-constrained Go test %s needs explicit CI file evidence", file)
	}
	return nil
}

func platformSpecificGoFile(file string) bool {
	base := strings.TrimSuffix(path.Base(file), ".go")
	for _, suffix := range strings.Split(base, "_") {
		if goPlatformSuffixes[suffix] {
			return true
		}
	}
	return false
}

func hasGoBuildConstraint(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "\uFEFF")
		if strings.HasPrefix(trimmed, "//go:build") || strings.HasPrefix(trimmed, "// +build") {
			return true
		}
	}
	return false
}

var goPlatformSuffixes = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true,
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mips64": true, "mips64le": true, "mipsle": true,
	"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true,
}

func readPRSnapshotRawFile(run prCommandRunner, pr *PRInfo, ref, file string) (string, error) {
	segments := strings.Split(file, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	endpoint := "repos/" + pr.Repository + "/contents/" + strings.Join(segments, "/") + "?ref=" + url.QueryEscape(ref)
	args := withPRAPIHostname([]string{"api", endpoint, "-H", "Accept: application/vnd.github.raw+json"}, pr.Host)
	output, err := run("gh", args...)
	if err != nil {
		return "", err
	}
	if len(output) > 1<<20 {
		return "", fmt.Errorf("PR snapshot file %s exceeds reachability evidence limit", file)
	}
	return string(output), nil
}
