package commands

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/reviewpolicy"
)

func appendReviewVerificationTargets(sb *strings.Builder, changedFiles []string, agentsMD string) {
	targets := reviewVerificationTargets(changedFiles)
	if sb == nil {
		return
	}
	constraints := reviewpolicy.ParseVerificationConstraints(agentsMD)
	appendReviewVerificationConstraints(sb, constraints)
	if len(targets) == 0 || constraints.TestsRequireContainer {
		return
	}
	sb.WriteString("## Required Local Verification Targets\n\n")
	sb.WriteString("For each Go target, call `run_verification` once with `kind=test`; that call supplies build and test evidence. Do not use `kind=build` because it does not execute tests. For other languages, call both kinds. Use each exact language and repository-relative path.\n\n")
	if hasNestedGoVerificationTarget(targets) {
		sb.WriteString("Never call `run_verification` with Go `path: .` for these nested targets. A root `go test .` does not cover them and wastes a verification call; use each exact Go directory listed below.\n\n")
	}
	for _, target := range targets {
		sb.WriteString("- ")
		sb.WriteString(target)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

// ReviewVerificationCallBudget reports the number of focused verification
// calls required to satisfy the generated target plan. Go tests cover both
// build and test evidence, while other languages need one call for each.
func ReviewVerificationCallBudget(changedFiles []string) int {
	return len(reviewVerificationEvidenceRequests(changedFiles))
}

func reviewVerificationEvidenceRequests(changedFiles []string) []oneshot.AgentEvidenceRequest {
	if len(changedFiles) == 0 || reviewChangedFilesDocumentationOnly(changedFiles) {
		return nil
	}
	targets := reviewVerificationTargets(changedFiles)
	if len(targets) == 0 {
		return []oneshot.AgentEvidenceRequest{
			verificationEvidenceRequest("build", "auto", "."),
			verificationEvidenceRequest("test", "auto", "."),
		}
	}

	requests := make([]oneshot.AgentEvidenceRequest, 0, len(targets)*2)
	for _, target := range targets {
		language, path, ok := strings.Cut(target, ": ")
		if !ok {
			continue
		}
		if language == "go" {
			requests = append(requests, verificationEvidenceRequest("test", language, path))
			continue
		}
		requests = append(requests,
			verificationEvidenceRequest("build", language, path),
			verificationEvidenceRequest("test", language, path),
		)
	}
	return requests
}

func verificationEvidenceRequest(kind, language, path string) oneshot.AgentEvidenceRequest {
	return oneshot.AgentEvidenceRequest{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind":     kind,
			"language": language,
			"path":     path,
		},
	}
}

func hasNestedGoVerificationTarget(targets []string) bool {
	for _, target := range targets {
		if strings.HasPrefix(target, "go: ") && strings.TrimPrefix(target, "go: ") != "." {
			return true
		}
	}
	return false
}

func appendReviewVerificationConstraints(sb *strings.Builder, constraints reviewpolicy.VerificationConstraints) {
	if sb == nil || (!constraints.TestsRequireContainer && !constraints.ForbidHostRepoWideGo) {
		return
	}
	sb.WriteString("## Repository Verification Constraints\n\n")
	if constraints.TestsRequireContainer {
		sb.WriteString("- Project directives require tests in Docker or a dedicated container.\n")
		sb.WriteString("- The host `run_verification` tool cannot satisfy that requirement. Do not call it with `kind=test`.\n")
		sb.WriteString("- Use supplied immutable test evidence when present. Otherwise report Tests as UNAVAILABLE.\n")
		sb.WriteString("- Missing container evidence is a review limitation. It is not a product defect and cannot lower the grade below B.\n")
	}
	if constraints.ForbidHostRepoWideGo {
		sb.WriteString("- Project directives forbid repo-wide Go commands on the host. Never substitute `go test ./...` or `go build ./...`.\n")
	}
	sb.WriteString("\n")
}

func reviewVerificationTargets(changedFiles []string) []string {
	seen := make(map[string]struct{})
	for _, file := range changedFiles {
		file = normalizeReviewEvidencePath(file)
		language := reviewChangedFileLanguage(file)
		if file == "" || language == "" {
			continue
		}
		target := language + ": " + filepath.ToSlash(filepath.Dir(file))
		seen[target] = struct{}{}
	}
	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func validateReviewEvidenceCoverage(changedFiles []string, evidence []reviewCommandEvidenceDetails) error {
	required := make(map[string][]string)
	for _, file := range changedFiles {
		file = normalizeReviewEvidencePath(file)
		if file == "" {
			continue
		}
		if language := reviewChangedFileLanguage(file); language != "" {
			required[language] = append(required[language], file)
		}
	}

	if len(required) == 0 {
		byLanguage := make(map[string]map[string]bool)
		for _, item := range evidence {
			if item.Language == "" || item.Kind == "" || !reviewEvidenceCoversRepositoryRoot(item) {
				continue
			}
			if byLanguage[item.Language] == nil {
				byLanguage[item.Language] = make(map[string]bool)
			}
			byLanguage[item.Language][item.Kind] = true
			if item.Language == "go" && item.Kind == reviewEvidenceTest {
				byLanguage[item.Language][reviewEvidenceBuild] = true
			}
		}
		for _, kinds := range byLanguage {
			if kinds[reviewEvidenceBuild] && kinds[reviewEvidenceTest] {
				return nil
			}
		}
		return fmt.Errorf("approval without recognized changed source paths requires repo-root build and test evidence from one applicable toolchain")
	}

	var missing []string
	languages := make([]string, 0, len(required))
	for language := range required {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		files := required[language]
		languageSatisfied := false
		for _, evidenceLanguage := range []string{language, "*"} {
			candidateSatisfied := true
			for _, kind := range []string{reviewEvidenceBuild, reviewEvidenceTest} {
				for _, file := range files {
					if !reviewEvidenceCoversFile(evidence, evidenceLanguage, kind, file) {
						candidateSatisfied = false
					}
				}
			}
			if candidateSatisfied {
				languageSatisfied = true
				break
			}
		}
		if !languageSatisfied {
			var gaps []string
			for _, kind := range []string{reviewEvidenceBuild, reviewEvidenceTest} {
				var paths []string
				for _, file := range files {
					if !reviewEvidenceCoversFile(evidence, language, kind, file) {
						paths = append(paths, file)
					}
				}
				if len(paths) > 0 {
					gaps = append(gaps, kind+":"+strings.Join(paths, "+"))
				}
			}
			missing = append(missing, language+"("+strings.Join(gaps, ";")+")")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("approval evidence does not cover changed source paths: %s", strings.Join(missing, ", "))
	}
	return nil
}

func reviewEvidenceCoversRepositoryRoot(item reviewCommandEvidenceDetails) bool {
	for _, target := range item.Targets {
		if normalizeReviewEvidencePath(target.Path) == "." && target.Recursive {
			return true
		}
	}
	return false
}

func reviewEvidenceCoversFile(evidence []reviewCommandEvidenceDetails, language, kind, file string) bool {
	fileDir := filepath.ToSlash(filepath.Dir(file))
	for _, item := range evidence {
		kindMatches := item.Kind == kind ||
			(language == "go" && kind == reviewEvidenceBuild && item.Kind == reviewEvidenceTest)
		if !kindMatches || item.Language != language {
			continue
		}
		for _, target := range item.Targets {
			targetPath := normalizeReviewEvidencePath(target.Path)
			if targetPath == "" {
				continue
			}
			if target.ExactFile {
				if file == targetPath {
					return true
				}
				continue
			}
			if target.Recursive {
				if targetPath == "." || file == targetPath || strings.HasPrefix(file, targetPath+"/") {
					return true
				}
				continue
			}
			if fileDir == targetPath {
				return true
			}
		}
	}
	return false
}

func normalizeReviewEvidencePath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" || filepath.IsAbs(path) {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

func reviewChangedFileLanguage(path string) string {
	base := strings.ToLower(filepath.Base(path))
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py", ".pyi":
		return "python"
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return "node"
	}
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return "go"
	case "cargo.toml", "cargo.lock":
		return "rust"
	case "pyproject.toml", "setup.py", "setup.cfg", "pytest.ini", "requirements.txt":
		return "python"
	case "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml":
		return "node"
	default:
		return ""
	}
}

func reviewChangedFilesDocumentationOnly(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, raw := range paths {
		path := normalizeReviewEvidencePath(raw)
		if path == "" || !reviewDocumentationPath(path) {
			return false
		}
	}
	return true
}

func reviewDocumentationPath(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".md", ".markdown", ".mdx", ".rst", ".adoc", ".asciidoc":
		return true
	}

	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "authors", "changelog", "code_of_conduct", "contributing", "contributors", "license", "notice", "readme", "security", "support":
		return true
	default:
		return false
	}
}
