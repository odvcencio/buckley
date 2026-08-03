package execmode

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu      sync.Mutex
	records []AuditRecord
	fail    bool
}

func (s *recordingSink) Record(record AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("audit store unavailable")
	}
	s.records = append(s.records, record)
	return nil
}

func (s *recordingSink) byMethod(method string) []AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AuditRecord
	for _, r := range s.records {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

type capsResponse struct {
	status int
	body   string
}

// capsHTTPCall speaks to a broker socket the way the generated caps
// client does: HTTP over unix with a bearer token.
func capsHTTPCall(t *testing.T, socket, token, path, body string) (capsResponse, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequest(http.MethodPost, "http://caps"+path, strings.NewReader(body))
	if err != nil {
		return capsResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return capsResponse{}, err
	}
	defer resp.Body.Close()
	out := new(strings.Builder)
	_, _ = io.Copy(out, resp.Body)
	return capsResponse{status: resp.StatusCode, body: out.String()}, nil
}

func newWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "greeting.txt"), []byte("hello from the workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "notes.md"), []byte("alpha\nneedle here\nomega\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBroker_JailRejectsEscapes locks the jail: absolute paths, dot-dot
// traversal, and symlink escapes are all refused and audited as errors.
func TestBroker_JailRejectsEscapes(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatal(err)
	}

	sink := &recordingSink{}
	broker, err := NewBroker(workspace, sink)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}

	for _, path := range []string{"../outside.txt", "/etc/passwd", "link.txt"} {
		if _, err := broker.filesRead(map[string]any{"path": path}); err == nil {
			t.Fatalf("jail allowed %q", path)
		}
	}
	if _, err := broker.filesRead(map[string]any{"path": "greeting.txt"}); err != nil {
		t.Fatalf("jail rejected a legitimate read: %v", err)
	}
}

// TestBroker_AuditFailureFailsTheCall locks the full-truth rule: when the
// sink cannot record, the capability answers with an error, never with
// unaudited data.
func TestBroker_AuditFailureFailsTheCall(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	sink := &recordingSink{fail: true}
	broker, err := NewBroker(workspace, sink)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", `{"path":"greeting.txt"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.status == 200 {
		t.Fatalf("unaudited call succeeded: %+v", resp)
	}
	if !strings.Contains(resp.body, "could not be audited") {
		t.Fatalf("body = %q, want audit failure", resp.body)
	}
}

// TestBroker_RequiresToken locks socket auth: even a caller on the unix
// socket needs the run token.
func TestBroker_RequiresToken(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(workspace, sink)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	resp, err := capsHTTPCall(t, socket, "wrong-token", "/v1/files/read", `{"path":"greeting.txt"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.status != 401 {
		t.Fatalf("status = %d, want 401", resp.status)
	}
	if denied := sink.byMethod("files.read"); len(denied) != 1 || denied[0].Outcome != "denied" {
		t.Fatalf("denied call not audited: %+v", sink.records)
	}
}

// TestRunner_EndToEndProgram locks the whole surface with a real
// program: it lists the workspace, reads a file, searches for a needle,
// prints a composed result — and every capability call lands in the
// audit trail. The environment scrub is asserted from inside the
// program: a sentinel secret in Buckley's environment must be invisible.
func TestRunner_EndToEndProgram(t *testing.T) {
	workspace := newWorkspace(t)
	t.Setenv("BUCKLEY_TEST_SENTINEL_SECRET", "leak-me-if-you-can")

	sink := &recordingSink{}
	runner, err := NewRunner(workspace, sink, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	const program = `package main

import (
	"fmt"
	"os"
	"strings"

	"execprogram/caps"
)

func main() {
	if os.Getenv("BUCKLEY_TEST_SENTINEL_SECRET") != "" {
		fmt.Println("ENV LEAK")
		os.Exit(1)
	}
	entries, err := caps.ListDir(".")
	if err != nil {
		panic(err)
	}
	content, _, err := caps.ReadFile("greeting.txt")
	if err != nil {
		panic(err)
	}
	matches, _, err := caps.SearchText("needle")
	if err != nil {
		panic(err)
	}
	fmt.Printf("entries=%d greeting=%q needles=%d at=%s\n",
		len(entries), strings.TrimSpace(content), len(matches), matches[0].File)
}
`
	result, err := runner.Run(context.Background(), program)
	if err != nil {
		t.Fatalf("Run: %v\nstderr:\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	want := `entries=2 greeting="hello from the workspace" needles=1 at=sub/notes.md`
	if !strings.Contains(result.Stdout, want) {
		t.Fatalf("stdout = %q, want %q", result.Stdout, want)
	}

	for _, method := range []string{"files.list", "files.read", "search.text"} {
		records := sink.byMethod(method)
		if len(records) != 1 || records[0].Outcome != "ok" {
			t.Fatalf("audit for %s = %+v, want one ok record", method, records)
		}
	}
}

// TestRunner_TimeoutKillsProgram locks the bound: an infinite loop dies
// at the timeout with a clear error.
func TestRunner_TimeoutKillsProgram(t *testing.T) {
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	runner, err := NewRunner(workspace, sink, 15*time.Second)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	_, err = runner.Run(context.Background(), "package main\n\nfunc main() { for {} }\n")
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want timeout", err)
	}
}

// TestRunner_RejectsNonMainSource locks the contract: fragments are
// refused before any scaffold work.
func TestRunner_RejectsNonMainSource(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	runner, err := NewRunner(t.TempDir(), sink, time.Minute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.Run(context.Background(), `fmt.Println("fragment")`); err == nil {
		t.Fatal("fragment accepted")
	}
}
