package commands

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"m31labs.dev/buckley/pkg/reviewpolicy"
)

const (
	matrixCheckoutAction = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
	matrixGoAction       = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	matrixNodeAction     = "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020"
)

// recognizeGoMatrixWorkflow accepts only a direct, pinned-actions workflow.
// Matching the complete job prevents env, scripts, conditionals, matrix include,
// alternate checkouts, and additional steps from changing the test invocation.
func recognizeGoMatrixWorkflow(content string) ([]string, error) {
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(content), &workflow); err != nil {
		return nil, err
	}
	for key := range workflow {
		switch key {
		case "name", "on", "permissions", "concurrency", "jobs":
		default:
			return nil, fmt.Errorf("unsupported workflow field %s", key)
		}
	}
	if !reflect.DeepEqual(workflow["permissions"], map[string]any{"contents": "read"}) {
		return nil, fmt.Errorf("unsupported workflow permissions")
	}
	triggers, ok := workflow["on"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow has no pull-request trigger")
	}
	pull, ok := triggers["pull_request"]
	if !ok || pull != nil {
		return nil, fmt.Errorf("filtered pull-request trigger is unsupported")
	}
	for trigger, config := range triggers {
		if trigger == "pull_request" {
			continue
		}
		push, ok := config.(map[string]any)
		if trigger != "push" || !ok || len(push) != 1 {
			return nil, fmt.Errorf("unsupported workflow trigger")
		}
		branches, ok := push["branches"].([]any)
		if !ok || len(branches) == 0 {
			return nil, fmt.Errorf("unsupported push branches")
		}
		for _, branch := range branches {
			if _, ok := branch.(string); !ok {
				return nil, fmt.Errorf("unsupported push branch")
			}
		}
	}
	if concurrency, exists := workflow["concurrency"]; exists {
		fields, ok := concurrency.(map[string]any)
		if !ok || len(fields) != 2 || fields["cancel-in-progress"] != true {
			return nil, fmt.Errorf("unsupported workflow concurrency")
		}
		group, ok := fields["group"].(string)
		if !ok || !strings.HasSuffix(group, "${{ github.workflow }}-${{ github.ref }}") {
			return nil, fmt.Errorf("concurrency is not workflow/ref bound")
		}
	}
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok || len(jobs) < 1 || len(jobs) > 2 {
		return nil, fmt.Errorf("unsupported workflow jobs")
	}
	// This independent hosted-runner job cannot supply inputs to the Go job:
	// neither job has needs/outputs, and the exact Go job remains matched below.
	if len(jobs) == 2 && !recognizedIndependentBrowserJob(jobs["browser"]) {
		return nil, fmt.Errorf("unsupported independent workflow job")
	}
	job, ok := jobs["go"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow has no Go job")
	}
	strategy, ok := job["strategy"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Go job has no module strategy")
	}
	matrix, ok := strategy["matrix"].(map[string]any)
	if !ok || len(matrix) != 1 {
		return nil, fmt.Errorf("unsupported Go matrix")
	}
	rawModules, ok := matrix["module"].([]any)
	if !ok || len(rawModules) == 0 || len(rawModules) > maxPRGoTestFiles {
		return nil, fmt.Errorf("invalid module matrix")
	}
	modules, seen := make([]string, 0, len(rawModules)), map[string]bool{}
	for _, raw := range rawModules {
		dir, ok := raw.(string)
		if !ok || dir == "" || path.IsAbs(dir) || path.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") || strings.ContainsAny(dir, "\\$\n\r") || seen[dir] {
			return nil, fmt.Errorf("invalid matrix module directory")
		}
		for _, part := range strings.Split(dir, "/") {
			if part != "." && (strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") || part == "vendor" || part == "testdata") {
				return nil, fmt.Errorf("Go ignores module directory")
			}
		}
		modules, seen[dir] = append(modules, dir), true
	}
	expected := map[string]any{
		"name": "Go (${{ matrix.module }})", "runs-on": "ubuntu-latest", "timeout-minutes": 15,
		"strategy": map[string]any{"fail-fast": false, "matrix": map[string]any{"module": rawModules}},
		"defaults": map[string]any{"run": map[string]any{"working-directory": "${{ matrix.module }}"}},
		"steps": []any{
			map[string]any{"uses": matrixCheckoutAction, "with": map[string]any{"ref": "${{ github.event.pull_request.head.sha || github.sha }}", "persist-credentials": false}},
			map[string]any{"uses": matrixGoAction, "with": map[string]any{"go-version-file": "${{ matrix.module }}/go.mod", "cache-dependency-path": "${{ matrix.module }}/go.sum"}},
			map[string]any{"uses": matrixNodeAction, "with": map[string]any{"node-version": "26"}},
			map[string]any{"name": "Tests and race detector", "run": "go test -race -count=1 ./..."},
			map[string]any{"name": "Vet", "run": "go vet ./..."},
			map[string]any{"name": "Build CLI", "if": "matrix.module == '.'", "run": "go build ./cmd/slides"},
		},
	}
	// The CLI build is optional and occurs after test/vet; it cannot mask them.
	if steps, ok := job["steps"].([]any); ok && len(steps) == 5 {
		expected["steps"] = expected["steps"].([]any)[:5]
	} else if ok && len(steps) == 6 {
		build, ok := steps[5].(map[string]any)
		command, commandOK := build["run"].(string)
		fields := strings.Fields(command)
		if !ok || !commandOK || len(fields) != 3 || fields[0] != "go" || fields[1] != "build" || !strings.HasPrefix(fields[2], "./cmd/") || strings.ContainsAny(fields[2], "$;&|`\n\r\\") || path.Clean(strings.TrimPrefix(fields[2], "./")) != strings.TrimPrefix(fields[2], "./") {
			return nil, fmt.Errorf("unsupported post-test CLI build")
		}
		expected["steps"].([]any)[5] = map[string]any{"name": "Build CLI", "if": "matrix.module == '.'", "run": command}
	}
	if !reflect.DeepEqual(job, expected) {
		return nil, fmt.Errorf("Go matrix job differs from the pinned direct race-test contract")
	}
	return modules, nil
}

func recognizedIndependentBrowserJob(job any) bool {
	configureChrome := "capture_browser=\"$(command -v google-chrome)\"\n" +
		"printf '#!/bin/sh\\nexec \"%s\" --no-sandbox \"$@\"\\n' \"$capture_browser\" > chrome-capture\n" +
		"chmod +x chrome-capture\n" +
		"echo \"SLIDES_BROWSER=$capture_browser\" >> \"$GITHUB_ENV\"\n" +
		"echo \"SLIDES_CHROME=$PWD/chrome-capture\" >> \"$GITHUB_ENV\"\n"
	expected := map[string]any{
		"name":    "Browser (authoring, navigation, graphics, export)",
		"runs-on": "ubuntu-latest", "timeout-minutes": 20,
		"steps": []any{
			map[string]any{"uses": matrixCheckoutAction, "with": map[string]any{"ref": "${{ github.event.pull_request.head.sha || github.sha }}", "persist-credentials": false}},
			map[string]any{"uses": matrixGoAction, "with": map[string]any{"go-version-file": "go.mod"}},
			map[string]any{"uses": matrixNodeAction, "with": map[string]any{"node-version": "26"}},
			map[string]any{"run": "npm ci --ignore-scripts"},
			map[string]any{"run": "go build -o slides ./cmd/slides"},
			map[string]any{"name": "Configure runner Chrome", "run": configureChrome},
			map[string]any{"run": "node scripts/browser-ci.cjs ./slides"},
			map[string]any{"uses": "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02", "if": "always()", "with": map[string]any{"name": "browser-export-evidence", "path": "browser-test-output/", "if-no-files-found": "ignore"}},
		},
	}
	return reflect.DeepEqual(job, expected)
}

func capturePRGoMatrixReachability(run prCommandRunner, target prReference, pr *PRInfo, changedFiles []string) (*reviewpolicy.CIReachabilityEvidence, error) {
	files := recognizedChangedTestFiles(changedFiles)
	if pr == nil || len(files) == 0 || len(files) > maxPRGoTestFiles {
		return nil, fmt.Errorf("unsupported changed test set")
	}
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			return nil, fmt.Errorf("unsupported test file %s", file)
		}
	}
	output, err := run("gh", withPRTarget([]string{"pr", "checks", strconv.Itoa(target.Number), "--json", "name,state,link", "--required"}, target)...)
	if err != nil {
		return nil, err
	}
	var links []requiredCheckLink
	if err := json.Unmarshal(output, &links); err != nil {
		return nil, err
	}
	mergeSHA, err := readPRCIMergeCommit(run, pr)
	if err != nil {
		return nil, err
	}
	if err := verifyPRHeadMergeTreeEquality(run, pr, mergeSHA); err != nil {
		return nil, err
	}
	tree, symlinkedManifests, err := readPRGoMatrixTree(run, pr, mergeSHA)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	for _, file := range files {
		if !tree[file] {
			return nil, fmt.Errorf("changed test %s is not a regular snapshot file", file)
		}
		if err := verifyPRGoTestFile(run, pr, mergeSHA, file); err != nil {
			return nil, err
		}
		// A symlinked go.mod is still a module boundary for Go, but it is not a
		// regular file this adapter can read: refuse every test below one.
		for ancestor := path.Dir(file); ; ancestor = path.Dir(ancestor) {
			if symlinkedManifests[path.Join(ancestor, "go.mod")] {
				return nil, fmt.Errorf("changed test %s is under a symlinked go.mod (%s)", file, path.Join(ancestor, "go.mod"))
			}
			if ancestor == "." || ancestor == "/" {
				break
			}
		}
		dir := path.Dir(file)
		for !tree[path.Join(dir, "go.mod")] && dir != "." {
			dir = path.Dir(dir)
		}
		if !tree[path.Join(dir, "go.mod")] {
			return nil, fmt.Errorf("changed test %s has no module", file)
		}
		relative := strings.TrimPrefix(file, dir+"/")
		for _, part := range strings.Split(path.Dir(relative), "/") {
			if part != "." && (strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") || part == "vendor" || part == "testdata") {
				return nil, fmt.Errorf("Go ignores changed test directory %s", file)
			}
		}
		groups[dir] = append(groups[dir], file)
	}
	runIDs := map[int64]bool{}
	for _, link := range links {
		if link.State != "SUCCESS" && link.State != "PASS" {
			continue
		}
		runID, _, err := parsePRRequiredJobLink(link.Link, pr)
		if err == nil {
			runIDs[runID] = true
		}
	}
	for runID := range runIDs {
		workflowPath, err := readPRGoMatrixRunIdentity(run, pr, runID)
		if err != nil {
			continue
		}
		if !tree[workflowPath] {
			continue
		}
		content, err := readPRSnapshotRawFile(run, pr, mergeSHA, workflowPath)
		if err != nil {
			continue
		}
		modules, err := recognizeGoMatrixWorkflow(content)
		if err != nil {
			continue
		}
		metadata, err := readPRTestRun(run, target, runID)
		if err != nil || metadata.HeadSHA != pr.HeadSHA || metadata.Event != "pull_request" || metadata.Status != "completed" || metadata.Conclusion != "success" {
			continue
		}
		evidence := &reviewpolicy.CIReachabilityEvidence{Source: "github_ci_go_matrix_v1", HeadSHA: pr.HeadSHA, MergeSHA: mergeSHA, RunID: runID}
		selected := map[string]bool{}
		for _, dir := range modules {
			selected[dir] = true
		}
		for dir, tests := range groups {
			if !selected[dir] {
				continue
			}
			want := "Go (" + dir + ")"
			var jobID int64
			for _, link := range links {
				linkRun, id, err := parsePRRequiredJobLink(link.Link, pr)
				if err != nil || linkRun != runID || link.Name != want || (link.State != "SUCCESS" && link.State != "PASS") {
					continue
				}
				for _, job := range metadata.Jobs {
					if job.DatabaseID != id || job.Name != want || job.Conclusion != "success" {
						continue
					}
					for _, step := range job.Steps {
						if step.Name == "Tests and race detector" && step.Conclusion == "success" && step.CompletedAt.After(step.StartedAt) {
							jobID = id
						}
					}
				}
			}
			if jobID == 0 {
				continue
			}
			module, err := readPRMatrixGoModule(run, pr, mergeSHA, dir)
			if err != nil {
				return nil, err
			}
			packages, err := provePRMatrixGoPackages(run, pr, mergeSHA, dir, module, tests)
			if err != nil {
				return nil, err
			}
			evidence.Modules = append(evidence.Modules, reviewpolicy.CIReachabilityModule{Directory: dir, Module: module, JobID: jobID, Check: want, TestFiles: tests, Packages: packages})
		}
		if len(evidence.Modules) == len(groups) {
			sort.Slice(evidence.Modules, func(i, j int) bool { return evidence.Modules[i].Directory < evidence.Modules[j].Directory })
			return evidence, nil
		}
	}
	return nil, fmt.Errorf("no exact-snapshot pinned Go matrix run covers all changed tests in required jobs")
}

func verifyPRHeadMergeTreeEquality(run prCommandRunner, pr *PRInfo, mergeSHA string) error {
	var trees []string
	for _, sha := range []string{mergeSHA, pr.HeadSHA} {
		output, err := run("gh", withPRAPIHostname([]string{"api", "repos/" + pr.Repository + "/git/commits/" + url.PathEscape(sha)}, pr.Host)...)
		if err != nil {
			return err
		}
		var commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		}
		if err := json.Unmarshal(output, &commit); err != nil || commit.Tree.SHA == "" {
			return fmt.Errorf("snapshot tree SHA unavailable")
		}
		trees = append(trees, commit.Tree.SHA)
	}
	if trees[0] != trees[1] {
		return fmt.Errorf("head CI does not cover the current merge tree")
	}
	return nil
}

func readPRGoMatrixTree(run prCommandRunner, pr *PRInfo, sha string) (regular, symlinkedManifests map[string]bool, err error) {
	output, err := run("gh", withPRAPIHostname([]string{"api", "repos/" + pr.Repository + "/git/trees/" + url.PathEscape(sha) + "?recursive=1"}, pr.Host)...)
	if err != nil {
		return nil, nil, err
	}
	if len(output) > 16<<20 {
		return nil, nil, fmt.Errorf("snapshot tree exceeds evidence limit")
	}
	var tree struct {
		Truncated bool                                `json:"truncated"`
		Entries   []struct{ Path, Mode, Type string } `json:"tree"`
	}
	if err := json.Unmarshal(output, &tree); err != nil || tree.Truncated {
		return nil, nil, fmt.Errorf("snapshot tree unavailable or truncated")
	}
	entries := map[string]bool{}
	symlinks := map[string]bool{}
	for _, entry := range tree.Entries {
		if entry.Path == "go.work" || strings.HasSuffix(entry.Path, "/go.work") {
			return nil, nil, fmt.Errorf("Go workspace overrides need explicit reachability support")
		}
		if entry.Type == "blob" && (entry.Mode == "100644" || entry.Mode == "100755") {
			entries[entry.Path] = true
		}
		if entry.Mode == "120000" && path.Base(entry.Path) == "go.mod" {
			symlinks[entry.Path] = true
		}
	}
	return entries, symlinks, nil
}

func readPRGoMatrixRunIdentity(run prCommandRunner, pr *PRInfo, runID int64) (string, error) {
	output, err := run("gh", withPRAPIHostname([]string{"api", "repos/" + pr.Repository + "/actions/runs/" + strconv.FormatInt(runID, 10)}, pr.Host)...)
	if err != nil {
		return "", err
	}
	var data struct {
		Path                      string
		HeadSHA                   string `json:"head_sha"`
		Event, Status, Conclusion string
		PullRequests              []struct {
			Number     int
			Base, Head struct{ SHA string }
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return "", err
	}
	if data.HeadSHA != pr.HeadSHA || data.Event != "pull_request" || data.Status != "completed" || data.Conclusion != "success" || !strings.HasPrefix(data.Path, ".github/workflows/") || path.Clean(data.Path) != data.Path || !(strings.HasSuffix(data.Path, ".yml") || strings.HasSuffix(data.Path, ".yaml")) {
		return "", fmt.Errorf("CI run is not an exact-head workflow")
	}
	for _, pull := range data.PullRequests {
		if pull.Number == pr.Number && pull.Base.SHA == pr.BaseSHA && pull.Head.SHA == pr.HeadSHA {
			return data.Path, nil
		}
	}
	return "", fmt.Errorf("CI run has stale base/head association")
}

func readPRMatrixGoModule(run prCommandRunner, pr *PRInfo, sha, dir string) (string, error) {
	content, err := readPRSnapshotRawFile(run, pr, sha, path.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	module := ""
	for _, line := range strings.Split(content, "\n") {
		if before, _, found := strings.Cut(line, "//"); found {
			line = before
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "ignore" {
			// "ignore" removes directories from ./... , so a changed test below
			// one is not run by the matrix job. Refuse until the exclusions are
			// evaluated.
			return "", fmt.Errorf("module %s has go.mod ignore directives; coverage of its tests is not proven", dir)
		}
		if len(fields) == 2 && fields[0] == "module" && module == "" {
			module = fields[1]
		}
	}
	if module == "" {
		return "", fmt.Errorf("module %s has no module directive", dir)
	}
	return module, nil
}

func provePRMatrixGoPackages(run prCommandRunner, pr *PRInfo, sha, dir, module string, files []string) ([]string, error) {
	packages := map[string]bool{}
	for _, file := range files {
		content, err := readPRSnapshotRawFile(run, pr, sha, file)
		if err != nil || !plainPRGoSource(content) {
			return nil, fmt.Errorf("changed Go test %s is not plain buildable source", file)
		}
		// Go ./... selects packages made entirely of test files as well.
		relative := file
		if dir != "." {
			relative = strings.TrimPrefix(file, dir+"/")
		}
		pkg := module
		if packageDir := path.Dir(relative); packageDir != "." {
			pkg += "/" + packageDir
		}
		packages[pkg] = true
	}
	result := make([]string, 0, len(packages))
	for pkg := range packages {
		result = append(result, pkg)
	}
	sort.Strings(result)
	return result, nil
}
