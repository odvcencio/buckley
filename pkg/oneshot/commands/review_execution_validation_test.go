package commands

import "testing"

func TestValidateReviewEvidenceCoverageRequiresSameToolchainAndChangedPaths(t *testing.T) {
	changed := []string{"pkg/a/a.go", "pkg/b/b.go"}
	target := func(path string, recursive bool) []reviewCoverageTarget {
		return []reviewCoverageTarget{{Path: path, Recursive: recursive}}
	}

	t.Run("disjoint package evidence", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg/a", false)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg/b", false)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err == nil {
			t.Fatal("disjoint build/test targets satisfied changed-file coverage")
		}
	})

	t.Run("mixed toolchains", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target(".", true)},
			{Kind: reviewEvidenceTest, Language: "python", Targets: target(".", true)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err == nil {
			t.Fatal("mixed build/test toolchains satisfied approval")
		}
	})

	t.Run("recursive Go scope", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg", true)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg", true)},
		}
		if err := validateReviewEvidenceCoverage(changed, evidence); err != nil {
			t.Fatalf("recursive applicable evidence rejected: %v", err)
		}
	})

	t.Run("repo verification preset", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "*", Targets: target(".", true)},
			{Kind: reviewEvidenceTest, Language: "*", Targets: target(".", true)},
		}
		if err := validateReviewEvidenceCoverage(append(changed, "web/app.ts"), evidence); err != nil {
			t.Fatalf("paired repo-wide build/test evidence rejected: %v", err)
		}
	})

	t.Run("unrecognized configuration still requires repository coverage", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "go", Targets: target("pkg/a", false)},
			{Kind: reviewEvidenceTest, Language: "go", Targets: target("pkg/a", false)},
		}
		if err := validateReviewEvidenceCoverage([]string{"release.yaml"}, evidence); err == nil {
			t.Fatal("scoped unrelated evidence approved an unrecognized configuration change")
		}
	})

	t.Run("Cargo default members do not imply workspace coverage", func(t *testing.T) {
		evidence := []reviewCommandEvidenceDetails{
			{Kind: reviewEvidenceBuild, Language: "rust", Targets: target(".", false)},
			{Kind: reviewEvidenceTest, Language: "rust", Targets: target(".", false)},
		}
		if err := validateReviewEvidenceCoverage([]string{"crates/changed/src/lib.rs"}, evidence); err == nil {
			t.Fatal("plain Cargo default-member evidence covered an arbitrary changed crate")
		}
	})
}

func TestReviewChangedFilesDocumentationOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		paths []string
		want  bool
	}{
		"markdown":              {paths: []string{"README.md", "docs/release.mdx"}, want: true},
		"other doc formats":     {paths: []string{"guide.rst", "docs/design.adoc"}, want: true},
		"extensionless license": {paths: []string{"LICENSE"}, want: true},
		"empty":                 {paths: nil, want: false},
		"mixed source":          {paths: []string{"README.md", "main.go"}, want: false},
		"mixed configuration":   {paths: []string{"README.md", "release.yaml"}, want: false},
		"unsafe path":           {paths: []string{"../README.md"}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reviewChangedFilesDocumentationOnly(tc.paths); got != tc.want {
				t.Fatalf("reviewChangedFilesDocumentationOnly(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}
