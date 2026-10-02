package commands

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
)

// GeneratedCommit builds the fixed message for a commit that changes generated
// files only: "update(<scope>): regenerate <artifact>". No model runs, so the
// text cannot echo removed names from minified output. It returns nil when the
// diff is not generated-only.
func GeneratedCommit(stats oneshot.DiffStats) *CommitResult {
	if !stats.GeneratedOnly() {
		return nil
	}
	paths := stats.GeneratedPaths
	cr := &CommitResult{
		Action: "update",
		Scope:  generatedScope(paths),
		Body:   StringList{"Rebuild generated files from their sources; no hand-written changes."},
	}
	cr.Subject = "regenerate " + generatedArtifact(paths, false)
	if len(cr.Header()) > commitmsg.HeaderLimit {
		cr.Subject = "regenerate " + generatedArtifact(paths, true)
	}
	return cr
}

// generatedScope is the first directory shared by every path, or "".
func generatedScope(paths []string) string {
	first := ""
	for i, p := range paths {
		top, _, found := strings.Cut(p, "/")
		if !found {
			return ""
		}
		if i == 0 {
			first = top
		} else if top != first {
			return ""
		}
	}
	return first
}

func generatedArtifact(paths []string, countOnly bool) string {
	seen := map[string]bool{}
	var names []string
	for _, p := range paths {
		if b := path.Base(p); !seen[b] {
			seen[b] = true
			names = append(names, b)
		}
	}
	sort.Strings(names)
	if !countOnly && len(names) <= 2 {
		return strings.Join(names, " and ")
	}
	return fmt.Sprintf("%d generated files", len(paths))
}
