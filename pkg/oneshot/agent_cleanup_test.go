package oneshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

func TestCollectAgentEvidence_CleanupAfterAllCommands(t *testing.T) {
	for _, tc := range []struct {
		name, kind, mode string
		timeout          time.Duration
		cleanupExit      int
	}{
		{"parallel", "build", "pass", time.Second, 0},
		{"batches", "test", "pass", time.Second, 0},
		{"mixed", "mixed", "pass", time.Second, 0},
		{"failure", "build", "fail", time.Second, 0},
		{"timeout", "build", "wait", 100 * time.Millisecond, 0},
		{"cancellation", "build", "wait", time.Second, 0},
		{"cleanup failure", "build", "pass", time.Second, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initGoEvidenceRepo(t, "example.test/cleanup", "a", "b", "c")
			snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			log := filepath.Join(dir, "events")
			wrapper := filepath.Join(dir, "wrapper")
			body := `#!/bin/sh
log=$1
mode=$2
root=$3
printf 'start %s %s\n' "$$" "$root" >> "$log"
if [ "$mode" = wait ]; then exec sleep 10; fi
sleep 0.1
printf 'end %s %s\n' "$$" "$root" >> "$log"
if [ "$mode" = fail ]; then exit 1; fi
printf '%s\n' '{"Action":"pass","Package":"example.test/cleanup/a"}' '{"Action":"pass","Package":"example.test/cleanup/b"}' '{"Action":"pass","Package":"example.test/cleanup/c"}'
`
			if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			cleanup := filepath.Join(dir, "cleanup")
			body = fmt.Sprintf(`#!/bin/sh
log=$1
root=$2
[ -d "$root" ] || exit 2
while read -r event pid path; do
 if [ "$event" = start ] && kill -0 "$pid" 2>/dev/null; then exit 3; fi
done < "$log"
printf 'cleanup %%s\n' "$root" >> "$log"
exit %d
`, tc.cleanupExit)
			if err := os.WriteFile(cleanup, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			requests := []AgentEvidenceRequest{}
			for _, path := range []string{"a", "b", "c"} {
				kind := tc.kind
				if kind == "mixed" {
					kind = "build"
					if path == "a" {
						kind = "test"
					}
				}
				requests = append(requests, AgentEvidenceRequest{Tool: "run_verification", Parameters: map[string]any{"kind": kind, "language": "go", "path": path}})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.name == "cancellation" {
				done := make(chan struct{})
				defer close(done)
				go func() {
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-done:
							return
						case <-ticker.C:
							data, _ := os.ReadFile(log)
							if strings.Count(string(data), "start ") == 3 {
								cancel()
								return
							}
						}
					}
				}()
			}
			calls, err := (&AgentRunner{}).CollectAgentEvidence(ctx, requests, AgentExecutionOpts{
				ReviewSnapshot: snapshot, VerificationWrapper: []string{wrapper, log, tc.mode},
				VerificationCleanup: []string{cleanup, log}, VerificationParallelism: 3, VerificationTimeout: tc.timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 3 {
				t.Fatalf("calls = %d", len(calls))
			}
			wantStatus := "PASS"
			if tc.mode == "fail" {
				wantStatus = "FAIL"
			}
			if tc.mode == "wait" {
				wantStatus = "UNAVAILABLE"
			}
			for _, call := range calls {
				if call.Data["status"] != wantStatus {
					t.Fatalf("status: %+v", call)
				}
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if strings.Count(string(data), "start ") != 3 || strings.Count(string(data), "cleanup ") != 1 || !strings.HasPrefix(lines[len(lines)-1], "cleanup ") {
				t.Fatalf("cleanup must follow all commands once: %s", data)
			}
			if tc.mode != "wait" && strings.Count(string(data), "end ") != 3 {
				t.Fatalf("commands still running: %s", data)
			}
			root := strings.TrimPrefix(lines[len(lines)-1], "cleanup ")
			if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
				t.Fatalf("snapshot remains: %s: %v", root, err)
			}
		})
	}
}
