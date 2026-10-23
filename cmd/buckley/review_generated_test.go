package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
)

func TestInstallReviewGeneratedResolverAppliesConfiguredGlobs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.GeneratedGlobs = []string{"client/js/bootstrap*.js"}
	installReviewGeneratedResolver(cfg, false)
	t.Cleanup(func() { diffsignal.SetGeneratedResolver(nil) })
	raw := "diff --git a/client/js/bootstrap-a.js b/client/js/bootstrap-a.js\nnew file mode 100644\n--- /dev/null\n+++ b/client/js/bootstrap-a.js\n@@ -0,0 +1 @@\n+var shipped = 1;\n"
	if res := diffsignal.Prioritize(raw, 10_000); strings.Contains(res.Context, "var shipped") {
		t.Fatalf("configured glob did not summarize the file:\n%s", res.Context)
	}
}
