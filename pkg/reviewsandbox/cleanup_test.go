package reviewsandbox

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestCleanupSnapshot_FailureIsWarning(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	CleanupSnapshot([]string{writeFakeWrapper(t, "echo cleanup-error >&2\nexit 1\n")}, t.TempDir())
	if got := output.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "cleanup-error") {
		t.Fatalf("missing warning: %s", got)
	}
}
