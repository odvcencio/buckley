package reviewsandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeWrapper writes an executable shell script standing in for a
// remote verification wrapper (for example buildbox-run) satisfying the
// `<wrapper...> <snapshot-dir> <argv...>` contract, and returns its path.
func writeFakeWrapper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-wrapper.sh")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake wrapper: %v", err)
	}
	return path
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestExecutorVerifyWrapperRunsWrapperWithSnapshotDirThenArgv(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")

	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	wrapper := writeFakeWrapper(t, fmt.Sprintf("printf '%%s\\n' \"$@\" > %s\nexit 0\n", argvFile))

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
	})
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want PASS (error=%q stdout=%q stderr=%q)", result.Status, result.Error, result.Stdout, result.Stderr)
	}

	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	// The wrapper always receives the immutable snapshot ROOT (never a
	// package subdirectory -- see wrapperRemoteCommand), plus a portable
	// cd-then-exec shim that moves into the requested package before
	// running the real command.
	want := []string{snapshotRoot, "sh", "-c", `cd "$1" && shift && exec "$@"`, "sh", ".", "go", "test", "-count=1", "."}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapper argv = %v, want %v", lines, want)
	}
}

// TestExecutorVerifyWrapperSyncsSnapshotRootNotPackageSubdirectory is a
// regression test for a real production bug: an earlier version of this
// code passed the package subdirectory (not the snapshot root) as the
// wrapper's <local-dir>. A wrapper that syncs only files tracked under
// <local-dir> (as buildbox-run's `git ls-files -co` does) then never syncs
// go.mod, Cargo.toml, pyproject.toml, or package.json -- all of which live
// above the package directory -- so every remote command failed
// immediately with an error like "go.mod file not found in current
// directory or any parent directory", and a trusted exit code let that
// false failure through as CONFIRMED_FAIL. This test uses a real shell
// wrapper that mimics buildbox-run's sync-then-cd contract closely enough
// to catch that regression: it copies only the directory it is given, then
// runs the received command inside it.
func TestExecutorVerifyWrapperSyncsSnapshotRootNotPackageSubdirectory(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	mustWriteFile(t, filepath.Join(snapshotRoot, "pkg", "foo", "foo.go"), "package foo\n")

	// A minimal stand-in for buildbox-run: it "syncs" (copies) only the
	// directory it receives as $1, then execs the remaining argv inside
	// that copy. If Verify still handed it the package subdirectory
	// instead of the snapshot root, the copy would never contain go.mod.
	wrapper := writeFakeWrapper(t, `
sync_dir=$(mktemp -d)
cp -a "$1"/. "$sync_dir"/
shift
cd "$sync_dir"
exec "$@"
`)

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindCheck,
		Language:     LanguageGo,
		Path:         "pkg/foo",
	})
	if result.Status != StatusUnavailable && strings.Contains(result.Error+result.Stderr, "go.mod file not found") {
		t.Fatalf("wrapper synced the package subdirectory instead of the snapshot root: %+v", result)
	}
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want PASS (error=%q stdout=%q stderr=%q)", result.Status, result.Error, result.Stdout, result.Stderr)
	}
}

func TestExecutorVerifyWrapperTrustsExitOne(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	wrapper := writeFakeWrapper(t, "echo '--- FAIL: TestBad (0.00s)'\nexit 1\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
	})
	if result.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL for a real test failure exit code", result.Status)
	}
	if result.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", result.ExitCode)
	}
}

// TestExecutorVerifyWrapperTrustsRustExit101ForFailure is a regression test
// for a real production finding: cargo build/cargo test report a compile
// error or a failing test with exit code 101 (Rust's default panic code),
// not 1. A trusted-exit-code check that only accepted 0/1 misgraded a real
// Rust failure as an untrusted wrapper/transport result (UNAVAILABLE)
// instead of a confirmed failure.
func TestExecutorVerifyWrapperTrustsRustExit101ForFailure(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "Cargo.toml"), "[package]\nname = \"fake\"\nversion = \"0.1.0\"\n")
	wrapper := writeFakeWrapper(t, "echo 'test result: FAILED'\nexit 101\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageRust,
		Path:         ".",
	})
	if result.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL for a real Cargo failure exit code (error=%q)", result.Status, result.Error)
	}
	if result.ExitCode != 101 {
		t.Fatalf("exit code = %d, want 101", result.ExitCode)
	}
}

// TestExecutorVerifyWrapperRustExit1IsUntrustedNotFail guards the other
// direction: exit 1 is not a code cargo build/cargo test use for a real
// build or test failure, so it must still grade UNAVAILABLE for Rust, not
// FAIL.
func TestExecutorVerifyWrapperRustExit1IsUntrustedNotFail(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "Cargo.toml"), "[package]\nname = \"fake\"\nversion = \"0.1.0\"\n")
	wrapper := writeFakeWrapper(t, "exit 1\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageRust,
		Path:         ".",
	})
	if result.Status != StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE: exit 1 is not a code cargo uses for a real failure", result.Status)
	}
}

// TestExecutorVerifyWrapperGoExit101IsUntrustedNotFail is the Go-side
// counterpart: exit 101 is Rust's panic code, not Go's, so it must stay
// untrusted (UNAVAILABLE) for a Go verification command.
func TestExecutorVerifyWrapperGoExit101IsUntrustedNotFail(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	wrapper := writeFakeWrapper(t, "exit 101\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
	})
	if result.Status != StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE: exit 101 is not a code go test uses for a real failure", result.Status)
	}
}

// TestExecutorVerifyWrapperExit255GradesUnavailableNotFail is the harness
// safety rule: ssh connection failures, rsync errors, and other
// non-test wrapper exit codes (255 is ssh's) must never be reported as a
// confirmed product failure.
func TestExecutorVerifyWrapperExit255GradesUnavailableNotFail(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	wrapper := writeFakeWrapper(t, "echo 'ssh: connect to host buildbox port 22: Connection refused' >&2\nexit 255\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
	})
	if result.Status != StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE for wrapper exit 255", result.Status)
	}
	if result.Status == StatusFail {
		t.Fatal("a transport failure must never grade CONFIRMED_FAIL")
	}
	if !strings.Contains(result.Error, "not a recognized test result") {
		t.Fatalf("error = %q, want it to explain the exit code is untrusted", result.Error)
	}
}

func TestExecutorVerifyWrapperTimeoutGradesUnavailable(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	wrapper := writeFakeWrapper(t, "sleep 5\nexit 0\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
		Timeout:      50 * time.Millisecond,
	})
	if result.Status != StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE for a wrapper timeout", result.Status)
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Fatalf("error = %q, want a timeout message", result.Error)
	}
}

func TestExecutorVerifyWrapperLaunchFailureGradesUnavailable(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{missing})
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: snapshotRoot,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         ".",
	})
	if result.Status != StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE when the wrapper itself cannot launch", result.Status)
	}
	if !strings.Contains(result.Error, "failed to launch") {
		t.Fatalf("error = %q, want a launch-failure message", result.Error)
	}
}

const cannedGoTestJSONStream = `{"Action":"run","Package":"example.com/fake/pkgpass","Test":"TestOK"}
{"Action":"output","Package":"example.com/fake/pkgpass","Test":"TestOK","Output":"=== RUN   TestOK\n"}
{"Action":"pass","Package":"example.com/fake/pkgpass","Test":"TestOK","Elapsed":0}
{"Action":"output","Package":"example.com/fake/pkgpass","Output":"PASS\n"}
{"Action":"output","Package":"example.com/fake/pkgpass","Output":"ok  \texample.com/fake/pkgpass\t0.010s\n"}
{"Action":"pass","Package":"example.com/fake/pkgpass","Elapsed":0.01}
{"Action":"run","Package":"example.com/fake/pkgfail","Test":"TestBad"}
{"Action":"output","Package":"example.com/fake/pkgfail","Test":"TestBad","Output":"=== RUN   TestBad\n"}
{"Action":"output","Package":"example.com/fake/pkgfail","Test":"TestBad","Output":"    fake_test.go:10: boom\n"}
{"Action":"fail","Package":"example.com/fake/pkgfail","Test":"TestBad","Elapsed":0}
{"Action":"output","Package":"example.com/fake/pkgfail","Output":"FAIL\n"}
{"Action":"output","Package":"example.com/fake/pkgfail","Output":"FAIL\texample.com/fake/pkgfail\t0.005s\n"}
{"Action":"fail","Package":"example.com/fake/pkgfail","Elapsed":0.005}
{"Action":"output","Package":"example.com/fake/pkgskip","Output":"?   \texample.com/fake/pkgskip\t[no test files]\n"}
{"Action":"skip","Package":"example.com/fake/pkgskip","Elapsed":0}
`

// TestExecutorVerifyGoTestBatchParsesPerPackagePassFail is the harness's
// primary TDD contract: a fake wrapper script records the argv it received
// and replays canned `go test -json` output, and VerifyGoTestBatch parses
// per-package PASS/FAIL (and no-test-files skip) from the event stream
// correctly, in one wrapper invocation covering every requested package.
func TestExecutorVerifyGoTestBatchParsesPerPackagePassFail(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")

	fixture := filepath.Join(t.TempDir(), "events.jsonl")
	mustWriteFile(t, fixture, cannedGoTestJSONStream)
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	wrapper := writeFakeWrapper(t, fmt.Sprintf(
		"printf '%%s\\n' \"$@\" > %s\ncat %s\nexit 1\n", argvFile, fixture))

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	results, err := executor.VerifyGoTestBatch(context.Background(), snapshotRoot, []BatchTarget{
		{Path: "pkgpass"}, {Path: "pkgfail"}, {Path: "pkgskip"},
	}, 0, 0)
	if err != nil {
		t.Fatalf("VerifyGoTestBatch: %v", err)
	}

	if got := results["pkgpass"]; got.Status != StatusPass {
		t.Fatalf("pkgpass status = %s, want PASS (error=%q)", got.Status, got.Error)
	}
	if got := results["pkgfail"]; got.Status != StatusFail {
		t.Fatalf("pkgfail status = %s, want FAIL (stdout=%q)", got.Status, got.Stdout)
	} else if !strings.Contains(got.Stdout, "boom") {
		t.Fatalf("pkgfail stdout = %q, want captured failure output", got.Stdout)
	}
	if got := results["pkgskip"]; got.Status != StatusPass || !got.NoTestFiles {
		t.Fatalf("pkgskip = %+v, want PASS with NoTestFiles", got)
	}

	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	want := []string{snapshotRoot, "go", "test", "-json", "-count=1", "./pkgpass", "./pkgfail", "./pkgskip"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("batch wrapper argv = %v, want %v", argv, want)
	}
}

// TestExecutorVerifyGoTestBatchWrapperExit255GradesAllUnavailable is the
// batch analogue of the single-call transport-failure rule: an ssh/rsync
// style failure grades every package in the batch UNAVAILABLE, never
// CONFIRMED_FAIL for any of them.
func TestExecutorVerifyGoTestBatchWrapperExit255GradesAllUnavailable(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	wrapper := writeFakeWrapper(t, "exit 255\n")

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	results, err := executor.VerifyGoTestBatch(context.Background(), snapshotRoot, []BatchTarget{
		{Path: "pkgpass"}, {Path: "pkgfail"},
	}, 0, 0)
	if err != nil {
		t.Fatalf("VerifyGoTestBatch: %v", err)
	}
	for path, result := range results {
		if result.Status != StatusUnavailable {
			t.Fatalf("%s status = %s, want UNAVAILABLE for a batch-wide wrapper failure", path, result.Status)
		}
	}
}

// TestExecutorVerifyGoTestBatchLargeEarlyPackageDoesNotTruncateLaterTerminalEvent
// is a regression test for a real production bug: a batch's raw JSON
// stream is one shared buffer for every package in the chunk, and a large
// package's own verbose "output" events (one per "=== RUN"/"--- PASS"
// line) can push a later package's terminal pass/fail/skip event past a
// too-small buffer cap, silently grading that later package UNAVAILABLE
// even though it passed. This synthesizes exactly that shape: over 1MiB of
// interleaved "output" noise from an early package, followed by a later
// package's terminal "pass" event, and asserts the later package is still
// captured (proving batchDefaultMaxOutput is large enough for the case
// that motivated raising it, not just for small canned fixtures).
func TestExecutorVerifyGoTestBatchLargeEarlyPackageDoesNotTruncateLaterTerminalEvent(t *testing.T) {
	module := "example.com/fake"
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module "+module+"\n\ngo 1.26\n")

	var stream strings.Builder
	noisyLine := strings.Repeat("x", 200)
	// Over 1MiB of "output" events from a large early package, mimicking
	// hundreds of verbose per-test lines from a big package like
	// cmd/buckley -- exactly what a too-small buffer truncated in
	// production.
	for written := 0; written < 1_200_000; {
		event := fmt.Sprintf(`{"Action":"output","Package":"%s/pkgbig","Output":"=== RUN TestNoise_%s\n"}`+"\n", module, noisyLine)
		stream.WriteString(event)
		written += len(event)
	}
	stream.WriteString(fmt.Sprintf(`{"Action":"pass","Package":"%s/pkgbig","Elapsed":1.0}`+"\n", module))
	stream.WriteString(fmt.Sprintf(`{"Action":"pass","Package":"%s/pkgsmall","Elapsed":0.01}`+"\n", module))

	fixture := filepath.Join(t.TempDir(), "events.jsonl")
	mustWriteFile(t, fixture, stream.String())
	wrapper := writeFakeWrapper(t, fmt.Sprintf("cat %s\nexit 0\n", fixture))

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	results, err := executor.VerifyGoTestBatch(context.Background(), snapshotRoot, []BatchTarget{
		{Path: "pkgbig"}, {Path: "pkgsmall"},
	}, 0, 0)
	if err != nil {
		t.Fatalf("VerifyGoTestBatch: %v", err)
	}
	if got := results["pkgbig"]; got.Status != StatusPass {
		t.Fatalf("pkgbig status = %s, want PASS (error=%q)", got.Status, got.Error)
	}
	if got := results["pkgsmall"]; got.Status != StatusPass {
		t.Fatalf("pkgsmall status = %s, want PASS -- a large early package's output must not truncate a later package's terminal event out of the batch buffer (error=%q)", got.Status, got.Error)
	}
}

// TestExecutorVerifyGoTestBatchPartialStreamGradesMissingPackageUnavailable
// covers a mid-run connection drop: some packages completed and reported a
// terminal pass/fail/skip event before the stream cut off, but a requested
// package never did. That package must grade UNAVAILABLE individually
// rather than being silently reported as passing.
func TestExecutorVerifyGoTestBatchPartialStreamGradesMissingPackageUnavailable(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	partial := `{"Action":"run","Package":"example.com/fake/pkgpass","Test":"TestOK"}
{"Action":"pass","Package":"example.com/fake/pkgpass","Test":"TestOK","Elapsed":0}
{"Action":"pass","Package":"example.com/fake/pkgpass","Elapsed":0.01}
`
	fixture := filepath.Join(t.TempDir(), "partial.jsonl")
	mustWriteFile(t, fixture, partial)
	wrapper := writeFakeWrapper(t, fmt.Sprintf("cat %s\nexit 0\n", fixture))

	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{wrapper})
	results, err := executor.VerifyGoTestBatch(context.Background(), snapshotRoot, []BatchTarget{
		{Path: "pkgpass"}, {Path: "pkgnever"},
	}, 0, 0)
	if err != nil {
		t.Fatalf("VerifyGoTestBatch: %v", err)
	}
	if got := results["pkgpass"]; got.Status != StatusPass {
		t.Fatalf("pkgpass status = %s, want PASS", got.Status)
	}
	if got := results["pkgnever"]; got.Status != StatusUnavailable {
		t.Fatalf("pkgnever status = %s, want UNAVAILABLE for a package missing from the stream", got.Status)
	}
}

func TestVerifyGoTestBatchRequiresConfiguredWrapper(t *testing.T) {
	snapshotRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(snapshotRoot, "go.mod"), "module example.com/fake\n\ngo 1.26\n")
	executor := NewExecutorWithCodexCommand("")
	if _, err := executor.VerifyGoTestBatch(context.Background(), snapshotRoot, []BatchTarget{{Path: "."}}, 0, 0); err == nil {
		t.Fatal("expected an error when batching without a configured wrapper")
	}
}

// TestDefaultLocalParallelismStaysWithinBounds is the parallelism-cap
// contract: even with no wrapper configured, local verification never
// exceeds 4 concurrent commands, and never drops below 1.
func TestDefaultLocalParallelismStaysWithinBounds(t *testing.T) {
	got := DefaultLocalParallelism()
	if got < 1 || got > 4 {
		t.Fatalf("DefaultLocalParallelism() = %d, want a value in [1,4]", got)
	}
	want := runtime.NumCPU() / 4
	if want < 1 {
		want = 1
	}
	if want > 4 {
		want = 4
	}
	if got != want {
		t.Fatalf("DefaultLocalParallelism() = %d, want min(4, max(1, NumCPU/4)) = %d", got, want)
	}
}

func TestExecutorVerify_LowDiskIsInfrastructureFailure(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module example.test/disk\n\ngo 1.26\n")
	executor := NewExecutorWithCodexCommand("")
	executor.SetWrapper([]string{writeFakeWrapper(t, "exit 91\n")})
	single := executor.Verify(context.Background(), Request{SnapshotRoot: root, Kind: KindTest, Language: LanguageGo, Path: "."})
	batch, err := executor.VerifyGoTestBatch(context.Background(), root, []BatchTarget{{Path: "."}}, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []Result{single, batch["."]} {
		if result.Status != StatusUnavailable || result.ExitCode != -1 || !strings.Contains(result.Error, "remote build host low on disk") {
			t.Fatalf("low disk classified as product check: %+v", result)
		}
	}
}
