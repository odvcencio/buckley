package commands

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const workflowRiskEvidencePriority = 95

type workflowRiskContextProvider struct{}

// NewWorkflowRiskContextProvider returns a deterministic provider that turns
// high-signal workflow constructs into compact falsification targets.
func NewWorkflowRiskContextProvider() PRContextProvider {
	return workflowRiskContextProvider{}
}

func (workflowRiskContextProvider) Name() string {
	return "workflow-risk-signals"
}

func (workflowRiskContextProvider) Required() bool {
	return false
}

func (workflowRiskContextProvider) Collect(
	ctx context.Context,
	request PRContextProviderRequest,
) ([]PRContextEvidence, error) {
	root := strings.TrimSpace(request.RepositoryRoot)
	if root == "" {
		return nil, nil
	}

	files := append([]string(nil), request.ChangedFiles...)
	sort.Strings(files)
	evidence := make([]PRContextEvidence, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file = strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
		if !isGitHubWorkflowPath(file) {
			continue
		}
		filename, ok := reviewSnapshotFile(root, file)
		if !ok {
			return nil, fmt.Errorf("workflow risk path escapes repository: %s", file)
		}
		content, err := os.ReadFile(filename)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read workflow risk source %s: %w", file, err)
		}
		if body := workflowRefSHARiskEvidence(string(content)); body != "" {
			evidence = append(evidence, PRContextEvidence{
				Title:    "Workflow ref/SHA provenance violation",
				Body:     body,
				Priority: workflowRiskEvidencePriority,
				Files:    []string{file},
			})
		}
	}
	return evidence, nil
}

func isGitHubWorkflowPath(file string) bool {
	text := strings.ToLower(file)
	return strings.HasPrefix(text, ".github/workflows/") &&
		(strings.HasSuffix(text, ".yml") || strings.HasSuffix(text, ".yaml"))
}

func reviewSnapshotFile(root, file string) (string, bool) {
	clean := path.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", false
	}
	root = filepath.Clean(root)
	filename := filepath.Join(root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(root, filename)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filename, true
}

func workflowRefSHARiskEvidence(content string) string {
	if !strings.Contains(content, "workflow_dispatch:") ||
		!workflowHasCheckoutRef(content) ||
		!workflowUsesEventSHA(content) ||
		!workflowPublishesArtifact(content) {
		return ""
	}

	lines := strings.Split(content, "\n")
	dispatchLine := firstWorkflowLine(lines, "workflow_dispatch:")
	checkoutLine := firstWorkflowLine(lines, "actions/checkout@")
	refLine := firstWorkflowLineAfter(lines, checkoutLine, "ref:", 10)
	shaLine := firstWorkflowLineAny(lines, []string{"GITHUB_SHA", "github.sha"})

	return fmt.Sprintf(`- **Detected identity chain**: manual dispatch at line %d selects an explicit checkout ref near lines %d-%d, while a later gate reads an event SHA at line %d.
- **Static failure**: actions/checkout changes the workspace but does not rewrite workflow event variables such as GITHUB_SHA. For a manual dispatch, that event SHA identifies the default-branch dispatch commit, so an ancestry gate using it does not validate the selected ref. A tag whose commit is outside main can pass while different bytes are built and published.
- **Required disposition**: treat this as a demonstrated MAJOR defect unless later code explicitly resolves the checked-out HEAD or selected tag commit and uses that exact value for the ancestry gate. Tag-name, version, syntax, and unrelated green checks do not prove commit identity.
- **Fix direction**: resolve the selected ref to a commit after checkout and validate that commit (for example, git rev-parse HEAD) through ancestry, build, and publish.`,
		dispatchLine,
		checkoutLine,
		refLine,
		shaLine,
	)
}

func workflowHasCheckoutRef(content string) bool {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "actions/checkout@") {
			continue
		}
		end := min(len(lines), i+10)
		for _, candidate := range lines[i+1 : end] {
			trimmed := strings.TrimSpace(candidate)
			if strings.HasPrefix(trimmed, "- name:") || strings.HasPrefix(trimmed, "- uses:") {
				break
			}
			if strings.HasPrefix(trimmed, "ref:") {
				return true
			}
		}
	}
	return false
}

func workflowUsesEventSHA(content string) bool {
	return strings.Contains(content, "GITHUB_SHA") || strings.Contains(content, "github.sha")
}

func workflowPublishesArtifact(content string) bool {
	lower := strings.ToLower(content)
	for _, signal := range []string{"goreleaser", " release", "publish", "docker push", "upload"} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func firstWorkflowLine(lines []string, signal string) int {
	return firstWorkflowLineAfter(lines, 0, signal, len(lines))
}

func firstWorkflowLineAny(lines []string, signals []string) int {
	for i, line := range lines {
		for _, signal := range signals {
			if strings.Contains(line, signal) {
				return i + 1
			}
		}
	}
	return 0
}

func firstWorkflowLineAfter(lines []string, start int, signal string, limit int) int {
	start = max(0, start-1)
	end := min(len(lines), start+limit)
	for i := start; i < end; i++ {
		if strings.Contains(lines[i], signal) {
			return i + 1
		}
	}
	return 0
}
