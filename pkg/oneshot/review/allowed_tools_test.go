package review

import (
	"testing"

	"github.com/draco/buckley/pkg/tool"
	"github.com/stretchr/testify/require"
)

// TestReviewAllowedToolsExistInRegistry guards the review sub-agent's tool
// contract. Every name in branchReviewTools/prReviewTools must resolve to a
// real tool in the default registry; otherwise the sub-agent intersects the
// requested names with the registry, gets an empty set, and sends the request
// with ToolChoice:"none" — the model is offered no tools while the prompt tells
// it to "verify with tools", so it hallucinates or leaks raw tool-call text.
// This is the regression guard for exactly that bug (requested read/glob/grep/
// bash/write vs. registered read_file/find_files/search_text/run_shell/write_file).
func TestReviewAllowedToolsExistInRegistry(t *testing.T) {
	reg := tool.NewRegistry()

	cases := map[string][]string{
		"branchReviewTools": branchReviewTools,
		"fixFindingTools":   fixFindingTools,
		"prReviewTools":     prReviewTools,
	}
	for listName, names := range cases {
		require.NotEmpty(t, names, "%s must not be empty", listName)
		for _, name := range names {
			_, ok := reg.Get(name)
			require.Truef(t, ok,
				"%s requests tool %q, which is not registered in tool.NewRegistry(); "+
					"the review sub-agent would be offered ZERO tools (ToolChoice:none)", listName, name)
		}
	}
}
