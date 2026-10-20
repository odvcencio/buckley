package oneshot

import "strings"

// StagedDiffStats classifies the staged diff, optionally limited to paths,
// without asking a model. It applies the same generated-file rules as the
// context builder, including gitattributes.
func StagedDiffStats(paths []string) (DiffStats, error) {
	params := map[string]string{"staged": "true"}
	if len(paths) > 0 {
		params["paths"] = strings.Join(paths, "\x00")
		params["literal_paths"] = "true"
	}
	_, stats, err := gatherGitDiffStats(params, ContextOpts{})
	return stats, err
}
