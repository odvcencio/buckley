package reviewsandbox

import (
	"context"
	"log/slog"
	"time"
)

// CleanupSnapshot releases a wrapper's copy after the snapshot owner has
// joined all verification commands. A canceled review must still run cleanup.
func CleanupSnapshot(argv []string, snapshotRoot string) {
	if len(argv) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := append(append([]string(nil), argv[1:]...), snapshotRoot)
	output, err := runCommand(ctx, commandInvocation{Name: argv[0], Args: args}, 4096)
	if err != nil {
		slog.Warn("Review snapshot cleanup failed", "snapshot", snapshotRoot, "error", err, "stderr", output.Stderr)
	}
}
