package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/reviewpolicy"
)

func matrixWorkflowFixture(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("testdata/go_matrix_workflow.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestRecognizeGoMatrixWorkflow_RejectsUnprovenExecution(t *testing.T) {
	original := matrixWorkflowFixture(t)
	modules, err := recognizeGoMatrixWorkflow(original)
	if err != nil || len(modules) != 3 {
		t.Fatalf("modules=%v error=%v", modules, err)
	}
	for _, test := range []struct{ name, old, new string }{
		{"unpinned checkout", matrixCheckoutAction, "actions/checkout@main"},
		{"wrong checkout", "ref: ${{ github.event.pull_request.head.sha || github.sha }}", "ref: main"},
		{"filtered tests", "go test -race -count=1 ./...", "go test -race -count=1 -run Safe ./..."},
		{"no race detector", "go test -race -count=1 ./...", "go test -count=1 ./..."},
		{"masked failure", "go test -race -count=1 ./...", "go test -race -count=1 ./... || true"},
		{"test conditional", "name: Tests and race detector", "name: Tests and race detector\n        if: false"},
		{"altered directory", "working-directory: ${{ matrix.module }}", "working-directory: ."},
		{"module escape", "examples/canopy-migration", "../outside"},
		{"extra script", "- name: Tests and race detector", "- run: ./scripts/prepare.sh\n      - name: Tests and race detector"},
		{"extra job", "jobs:\n", "jobs:\n  prepare:\n    runs-on: ubuntu-latest\n    steps: []\n"},
		{"job environment", "timeout-minutes: 15", "timeout-minutes: 15\n    env:\n      GOFLAGS: -run=Safe"},
		{"workflow environment", "permissions:\n", "env:\n  GOFLAGS: -run=Safe\n\npermissions:\n"},
		{"matrix exclusion", "module: [", "exclude: [{module: '.'}]\n        module: ["},
		{"build script injection", "go build ./cmd/slides", "go build ./cmd/slides; true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := recognizeGoMatrixWorkflow(strings.Replace(original, test.old, test.new, 1)); err == nil {
				t.Fatal("unproven workflow accepted")
			}
		})
	}
}

func TestRecognizeGoMatrixWorkflowIndependentBrowserJob(t *testing.T) {
	data, err := os.ReadFile("testdata/go_matrix_browser_workflow.yml")
	if err != nil {
		t.Fatal(err)
	}
	original := string(data)
	modules, err := recognizeGoMatrixWorkflow(original)
	if err != nil || len(modules) != 3 {
		t.Fatalf("modules=%v error=%v", modules, err)
	}
	for _, test := range []struct{ name, old, new string }{
		{"Go depends on browser", "  go:\n", "  go:\n    needs: browser\n"},
		{"browser depends on Go", "  browser:\n", "  browser:\n    needs: go\n"},
		{"browser outputs", "  browser:\n", "  browser:\n    outputs: {code: x}\n"},
		{"shared runner", "runs-on: ubuntu-latest", "runs-on: self-hosted"},
		{"browser checkout drift", "          ref: ${{ github.event.pull_request.head.sha || github.sha }}", "          ref: main"},
		{"browser failure masked", "node scripts/browser-ci.cjs ./slides", "node scripts/browser-ci.cjs ./slides || true"},
		{"browser environment", "  browser:\n", "  browser:\n    env: {GOFLAGS: unsafe}\n"},
		{"third job", "jobs:\n", "jobs:\n  other: {runs-on: ubuntu-latest, steps: []}\n"},
		{"Go tests still strict", "go test -race -count=1 ./...", "go test -count=1 ./..."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := recognizeGoMatrixWorkflow(strings.Replace(original, test.old, test.new, 1)); err == nil {
				t.Fatal("unproven workflow accepted")
			}
		})
	}
}

func matrixTestRunner(t *testing.T, mode string) (prCommandRunner, *PRInfo, []string) {
	t.Helper()
	pr := &PRInfo{Number: 2, Host: "github.com", Repository: "owner/slides", BaseBranch: "main", BaseSHA: "base", HeadBranch: "topic", HeadSHA: "head"}
	tests := []string{"graphics_test.go", "examples/canopy-migration/deck_test.go", "examples/gophercon2026/deck_test.go", ".github/workflows/test.yml"}
	dirs := []string{".", "examples/canopy-migration", "examples/gophercon2026"}
	return func(name string, args ...string) ([]byte, error) {
		encode := func(value any) ([]byte, error) { return json.Marshal(value) }
		if name != "gh" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		if len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
			links := []requiredCheckLink{}
			for index, dir := range dirs {
				if mode == "unrequired-module" && index == 2 {
					continue
				}
				state := "SUCCESS"
				if mode == "failed-check" && index == 2 {
					state = "FAILURE"
				}
				host := pr.Host
				if mode == "wrong-repository" {
					host = "other.example"
				}
				links = append(links, requiredCheckLink{Name: "Go (" + dir + ")", State: state, Link: fmt.Sprintf("https://%s/%s/actions/runs/123/job/%d", host, pr.Repository, index+100)})
			}
			return encode(links)
		}
		if len(args) > 1 && args[0] == "api" {
			endpoint := args[1]
			switch {
			case strings.HasSuffix(endpoint, "/pulls/2"):
				return []byte(`{"merge_commit_sha":"merge"}`), nil
			case strings.Contains(endpoint, "/git/commits/"):
				tree := "tree"
				if mode == "merge-tree-drift" && strings.HasSuffix(endpoint, "/head") {
					tree = "other"
				}
				base := pr.BaseSHA
				if mode == "merge-parent-drift" {
					base = "other"
				}
				return []byte(fmt.Sprintf(`{"tree":{"sha":%q},"parents":[{"sha":%q},{"sha":%q}]}`, tree, base, pr.HeadSHA)), nil
			case strings.Contains(endpoint, "/git/trees/"):
				entries := []map[string]string{{"path": ".github/workflows/test.yml", "mode": "100644", "type": "blob"}}
				for _, dir := range dirs {
					for _, file := range []string{"go.mod", "deck_test.go", "deck.go"} {
						if dir == "." && file == "deck_test.go" {
							file = "graphics_test.go"
						}
						modeValue := "100644"
						if mode == "symlink-test" && file == "graphics_test.go" {
							modeValue = "120000"
						}
						if mode == "no-source" && file == "deck.go" {
							continue
						}
						entries = append(entries, map[string]string{"path": path.Join(dir, file), "mode": modeValue, "type": "blob"})
					}
				}
				if mode == "go-work" {
					entries = append(entries, map[string]string{"path": "go.work", "type": "blob", "mode": "100644"})
				}
				return encode(map[string]any{"truncated": mode == "truncated-tree", "tree": entries})
			case strings.Contains(endpoint, "/contents/.github/workflows/test.yml?"):
				content := matrixWorkflowFixture(t)
				if mode == "unsafe-workflow" {
					content = strings.Replace(content, "go test -race -count=1 ./...", "true", 1)
				}
				if mode == "wrong-module" {
					content = strings.Replace(content, "examples/gophercon2026", "examples/other", 1)
				}
				return []byte(content), nil
			case strings.Contains(endpoint, "/contents/"):
				if strings.Contains(endpoint, "/go.mod?") {
					return []byte("module example.dev/slides\ngo 1.26.0\n"), nil
				}
				if mode == "test-build-tag" && strings.Contains(endpoint, "_test.go?") {
					return []byte("//go:build integration\n\npackage slides\n"), nil
				}
				return []byte("package slides\n"), nil
			case strings.HasSuffix(endpoint, "/actions/runs/123"):
				head, base := pr.HeadSHA, pr.BaseSHA
				if mode == "stale-head" {
					head = "old"
				}
				if mode == "stale-base" {
					base = "old"
				}
				workflow := ".github/workflows/test.yml"
				if mode == "wrong-workflow" {
					workflow = ".github/workflows/other.yml"
				}
				return []byte(fmt.Sprintf(`{"path":%q,"head_sha":%q,"event":"pull_request","status":"completed","conclusion":"success","pull_requests":[{"number":2,"base":{"sha":%q},"head":{"sha":%q}}]}`, workflow, head, base, head)), nil
			}
		}
		if len(args) > 2 && args[0] == "run" && args[1] == "view" {
			jobs := []map[string]any{}
			for index, dir := range dirs {
				conclusion := "success"
				if mode == "skipped-test" && index == 2 {
					conclusion = "skipped"
				}
				jobs = append(jobs, map[string]any{"databaseId": index + 100, "name": "Go (" + dir + ")", "conclusion": "success", "steps": []map[string]any{{"name": "Tests and race detector", "conclusion": conclusion, "startedAt": time.Unix(100, 0).UTC(), "completedAt": time.Unix(200, 0).UTC()}}})
			}
			return encode(map[string]any{"headSha": pr.HeadSHA, "event": "pull_request", "status": "completed", "conclusion": "success", "jobs": jobs})
		}
		return nil, fmt.Errorf("unexpected arguments %v", args)
	}, pr, tests
}

func TestCapturePRGoMatrixReachability_ExactRequiredModules(t *testing.T) {
	run, pr, files := matrixTestRunner(t, "no-source")
	target := prReference{Number: pr.Number, Host: pr.Host, Repository: pr.Repository}
	capture, err := capturePRCIAdmission(run, target, pr, files)
	if err != nil || capture.ReachabilityErr != nil {
		t.Fatalf("capture=%+v error=%v", capture, err)
	}
	if err := capture.Receipt.Authorize(prCIAdmissionExpectation(pr, files)); err != nil {
		t.Fatal(err)
	}
	evidence := capture.Receipt.TestReachabilityEvidence
	if evidence.Source != "github_ci_go_matrix_v1" || len(evidence.Modules) != 3 {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestCapturePRGoMatrixReachability_FailsClosed(t *testing.T) {
	for _, mode := range []string{"merge-tree-drift", "merge-parent-drift", "unsafe-workflow", "wrong-module", "unrequired-module", "failed-check", "wrong-repository", "stale-head", "stale-base", "wrong-workflow", "skipped-test", "symlink-test", "truncated-tree", "go-work", "test-build-tag"} {
		t.Run(mode, func(t *testing.T) {
			run, pr, files := matrixTestRunner(t, mode)
			target := prReference{Number: pr.Number, Host: pr.Host, Repository: pr.Repository}
			capture, err := capturePRCIAdmission(run, target, pr, files)
			if err != nil {
				t.Fatal(err)
			}
			if capture.Receipt.Decision == reviewpolicy.CIAdmissionAllow {
				t.Fatalf("unsafe evidence admitted: %+v", capture)
			}
			if err := capture.Receipt.Authorize(prCIAdmissionExpectation(pr, files)); err == nil {
				t.Fatal("unsafe evidence authorized")
			}
		})
	}
}

// This opt-in check reads immutable GitHub evidence and never posts a review.
func TestCapturePRGoMatrixReachability_LiveGitHubSnapshot(t *testing.T) {
	reference := os.Getenv("BUCKLEY_TEST_MATRIX_PR")
	if reference == "" {
		t.Skip("set BUCKLEY_TEST_MATRIX_PR to validate a live PR snapshot")
	}
	target, err := parsePRRef(reference)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := getPRInfo(defaultPRCommandRunner, target)
	if err != nil {
		t.Fatal(err)
	}
	files, err := getPRFiles(defaultPRCommandRunner, pr)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := capturePRCIAdmission(defaultPRCommandRunner, target, pr, files)
	if err != nil || capture.ReachabilityErr != nil {
		t.Fatalf("capture=%+v error=%v", capture, err)
	}
	if err := capture.Receipt.Authorize(prCIAdmissionExpectation(pr, files)); err != nil {
		t.Fatal(err)
	}
	evidence := capture.Receipt.TestReachabilityEvidence
	if evidence == nil || evidence.Source != "github_ci_go_matrix_v1" {
		t.Fatalf("unexpected evidence=%+v", evidence)
	}
	t.Logf("validated PR %s head=%s base=%s merge=%s required=%d run=%s modules=%+v", reference, pr.HeadSHA, pr.BaseSHA, evidence.MergeSHA, len(capture.Receipt.RequiredContexts), strconv.FormatInt(evidence.RunID, 10), evidence.Modules)
}
