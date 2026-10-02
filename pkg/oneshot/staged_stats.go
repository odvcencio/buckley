package oneshot

// StagedDiffStats classifies the staged diff, optionally limited to paths,
// without asking a model. It applies the same generated-file rules as the
// context builder, including gitattributes.
func StagedDiffStats(paths []string) (DiffStats, error) {
	params := map[string]string{"staged": "true"}
	if len(paths) > 0 {
		params["paths"] = joinNUL(paths)
	}
	_, stats, err := gatherGitDiffStats(params, ContextOpts{})
	return stats, err
}

func joinNUL(paths []string) string {
	out := ""
	for i, p := range paths {
		if i > 0 {
			out += "\x00"
		}
		out += p
	}
	return out
}
