package commands

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func appendReviewVerificationTargets(sb *strings.Builder, changedFiles []string) {
	targets := reviewVerificationTargets(changedFiles)
	if sb == nil || len(targets) == 0 {
		return
	}
	sb.WriteString("## Required Local Verification Targets\n\n")
	sb.WriteString("For an approval, call `run_verification` for both `build` and `test` on every target below. Use the exact language and repository-relative path shown; do not substitute only remote CI.\n\n")
	for _, target := range targets {
		sb.WriteString("- ")
		sb.WriteString(target)
		sb.WriteString("\n")
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
			missing = append(missing, language+":"+strings.Join(files, "+"))
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
		if item.Kind != kind || item.Language != language {
			continue
		}
		for _, target := range item.Targets {
			targetPath := normalizeReviewEvidencePath(target.Path)
			if targetPath == "" {
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
