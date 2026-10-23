package main

import (
	"os"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
)

// installReviewGeneratedResolver makes review diffs treat configured globs
// (buckbot.generated_globs) as generated. With useRepoAttributes, files marked
// linguist-generated or -diff in the current repository's .gitattributes count
// too; a PR review of another repository passes false so the current
// directory's attributes are not applied to that repository's paths.
func installReviewGeneratedResolver(cfg *config.Config, useRepoAttributes bool) {
	var globs []string
	if cfg != nil {
		globs = cfg.Buckbot.GeneratedGlobs
	}
	workDir := ""
	if useRepoAttributes {
		workDir, _ = os.Getwd()
	}
	diffsignal.SetGeneratedResolver(diffsignal.NewGeneratedResolver(workDir, globs))
}
