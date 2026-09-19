package execmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCapsAPICard_TeachesBoundedSearchFirstComposition(t *testing.T) {
	for _, want := range []string{"at most 32 broker operations", "SearchTextGlob", ".worktrees"} {
		if !strings.Contains(CapsAPICard, want) {
			t.Fatalf("CapsAPICard missing %q", want)
		}
	}
	if strings.Contains(CapsAPICard, `caps.WalkDir(".")`) {
		t.Fatal("CapsAPICard still teaches whole-repository traversal")
	}
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
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
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
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
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

// TestRunner_SandboxBlocksEscapes locks slice 2's enforcement, from
// inside a sandboxed program: the host filesystem outside the mount
// plan is invisible, the network namespace has no route out, system
// directories are read-only — and the caps socket still works.
func TestRunner_SandboxBlocksEscapes(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	runner, err := NewRunner(workspace, sink, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if runner.Isolation() != IsolationBwrap {
		t.Fatalf("isolation = %q, want bwrap", runner.Isolation())
	}

	const program = `package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"execprogram/caps"
)

func main() {
	checks := 0
	if _, err := os.ReadFile("/etc/passwd"); err != nil {
		checks++ // host /etc is not mounted
	}
	if _, err := os.ReadDir(os.Getenv("HOST_WORKSPACE")); err != nil {
		checks++ // the workspace itself is not mounted; caps is the only window
	}
	if err := os.WriteFile("/usr/escape-probe", []byte("x"), 0o644); err != nil {
		checks++ // system dirs are read-only
	}
	if _, err := net.DialTimeout("tcp", "1.1.1.1:80", 2*time.Second); err != nil {
		checks++ // no network namespace route
	}
	content, _, err := caps.ReadFile("greeting.txt")
	if err == nil && content != "" {
		checks++ // the brokered window still works
	}
	fmt.Printf("blocked=%d\n", checks)
}
`
	// The host workspace path rides in via the program source, not env,
	// so the scrub test elsewhere stays meaningful.
	result, err := runner.Run(context.Background(), strings.Replace(program,
		`os.Getenv("HOST_WORKSPACE")`, fmt.Sprintf("%q", workspace), 1))
	if err != nil {
		t.Fatalf("Run: %v\nstderr:\n%s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "blocked=5") {
		t.Fatalf("stdout = %q, want all five checks to hold\nstderr:\n%s", result.Stdout, result.Stderr)
	}
}

// TestBroker_GuidanceAndRicherCapabilities locks the efficiency
// affordances: listing a file returns actionable guidance rather than a
// bare error, WalkDir returns the whole tree in one call, and the glob
// filter narrows a search to matching file names.
func TestBroker_GuidanceAndRicherCapabilities(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}

	_, err = broker.filesList(map[string]any{"dir": "greeting.txt"})
	if err == nil || !strings.Contains(err.Error(), "use ReadFile") {
		t.Fatalf("listing a file returned %v, want ReadFile guidance", err)
	}

	out, err := broker.filesList(map[string]any{"dir": ".", "recursive": true})
	if err != nil {
		t.Fatalf("recursive list: %v", err)
	}
	entries := out.(map[string]any)["entries"].([]string)
	var sawNested bool
	for _, entry := range entries {
		if entry == "sub/notes.md" {
			sawNested = true
		}
	}
	if !sawNested {
		t.Fatalf("recursive list = %v, want the nested file", entries)
	}

	if got := globSearchCount(t, broker, "needle", "*.md"); got != 1 {
		t.Fatalf("glob search matches = %d, want 1", got)
	}
	if got := globSearchCount(t, broker, "needle", "*.go"); got != 0 {
		t.Fatalf("non-matching glob returned %d matches", got)
	}
}

func TestBroker_FilesListPaginatesWithoutSilentTruncation(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < maxListEntries+17; i++ {
		path := filepath.Join(workspace, fmt.Sprintf("file-%04d.go", i))
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}

	first, err := broker.filesList(map[string]any{"dir": ".", "recursive": true})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	firstPage := first.(map[string]any)
	if got := len(firstPage["entries"].([]string)); got != maxListEntries {
		t.Fatalf("first page entries = %d, want %d", got, maxListEntries)
	}
	if capped, _ := firstPage["capped"].(bool); !capped {
		t.Fatal("first page capped = false, want another page")
	}
	cursor, _ := firstPage["next_cursor"].(int)
	if cursor != maxListEntries {
		t.Fatalf("first page next_cursor = %d, want %d", cursor, maxListEntries)
	}

	second, err := broker.filesList(map[string]any{
		"dir":       ".",
		"recursive": true,
		"cursor":    cursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	secondPage := second.(map[string]any)
	if got := len(secondPage["entries"].([]string)); got != 17 {
		t.Fatalf("second page entries = %d, want 17", got)
	}
	if capped, _ := secondPage["capped"].(bool); capped {
		t.Fatal("second page capped = true, want complete listing")
	}
}

func TestRunner_WalkDirPaginatesCompleteTree(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap unavailable")
	}
	workspace := t.TempDir()
	bulk := filepath.Join(workspace, "bulk")
	if err := os.MkdirAll(bulk, 0o755); err != nil {
		t.Fatal(err)
	}
	want := maxListEntries + 17
	for i := 0; i < want; i++ {
		path := filepath.Join(bulk, fmt.Sprintf("file-%04d.go", i))
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := NewRunner(workspace, &recordingSink{}, DefaultTimeout)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	result, err := runner.Run(context.Background(), `package main

import (
	"fmt"

	"execprogram/caps"
)

func main() {
	files, err := caps.WalkDir("bulk")
	if err != nil {
		panic(err)
	}
	fmt.Println(len(files))
}
`)
	if err != nil {
		t.Fatalf("Run: %v\nstderr:\n%s", err, result.Stderr)
	}
	if got := strings.TrimSpace(result.Stdout); got != fmt.Sprint(want) {
		t.Fatalf("stdout = %q, want %d complete entries", got, want)
	}
}

// globSearchCount counts matches through a JSON round-trip so the test
// does not depend on the broker's anonymous result struct.
func globSearchCount(t *testing.T, broker *Broker, pattern, glob string) int {
	t.Helper()
	out, err := broker.searchText(map[string]any{"pattern": pattern, "glob": glob})
	if err != nil {
		t.Fatalf("search %q glob %q: %v", pattern, glob, err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Matches []struct {
			File string `json:"file"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return len(decoded.Matches)
}

// TestBroker_CapabilityGrantEnforced locks per-run capability scoping: a
// valid token does not imply every capability. A run granted MinimalSet
// can read and list but is refused search, and the refusal is audited as
// a denial rather than an error.
func TestBroker_CapabilityGrantEnforced(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(workspace, sink, WithCapabilities(MinimalSet...))
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if !broker.Granted(CapFilesRead) || broker.Granted(CapSearchText) {
		t.Fatalf("grant = read:%v search:%v, want read granted and search withheld",
			broker.Granted(CapFilesRead), broker.Granted(CapSearchText))
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	ok, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", `{"path":"greeting.txt"}`)
	if err != nil || ok.status != 200 {
		t.Fatalf("granted capability failed: status=%d err=%v", ok.status, err)
	}
	denied, err := capsHTTPCall(t, socket, broker.Token(), "/v1/search/text", `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("search call: %v", err)
	}
	if denied.status != 403 || !strings.Contains(denied.body, "not granted") {
		t.Fatalf("ungranted capability = %d %q, want 403", denied.status, denied.body)
	}
	records := sink.byMethod(CapSearchText)
	if len(records) != 1 || records[0].Outcome != "denied" {
		t.Fatalf("denial audit = %+v, want one denied record", records)
	}
}

func TestBroker_CapabilityCallBudgetBoundsProgramFanout(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(workspace, sink, WithCapabilityCallLimit(2))
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	for call := 1; call <= 3; call++ {
		resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", `{"path":"greeting.txt"}`)
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		if call <= 2 && resp.status != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", call, resp.status)
		}
		if call == 3 && (resp.status != http.StatusTooManyRequests || !strings.Contains(resp.body, "budget exhausted")) {
			t.Fatalf("call %d = %d %q, want bounded denial", call, resp.status, resp.body)
		}
	}
	records := sink.byMethod(CapFilesRead)
	if len(records) != 3 || records[2].Outcome != "denied" || !strings.Contains(records[2].Detail, "2 per program") {
		t.Fatalf("audit records = %+v, want two calls and one bounded denial", records)
	}
}

func TestBroker_RecursiveDiscoverySkipsManagedWorktrees(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(workspace, ".worktrees", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".worktrees", "other", "hidden.go"), []byte("package hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	result, err := broker.filesList(map[string]any{"dir": ".", "recursive": true, "cursor": 0})
	if err != nil {
		t.Fatalf("filesList: %v", err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), ".worktrees") || !strings.Contains(string(encoded), "greeting.txt") {
		t.Fatalf("recursive listing = %s, want workspace files without managed worktrees", encoded)
	}
}

// TestBroker_ExpiredTokenRejected locks token expiry: a token past its
// TTL is refused even on the right socket, and the refusal is audited.
func TestBroker_ExpiredTokenRejected(t *testing.T) {
	t.Parallel()
	workspace := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(workspace, sink)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	broker.expires = time.Now().Add(-time.Second)
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer broker.Close()

	resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", `{"path":"greeting.txt"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.status != 401 || !strings.Contains(resp.body, "expired") {
		t.Fatalf("expired token = %d %q, want 401 expired", resp.status, resp.body)
	}
	records := sink.byMethod(CapFilesRead)
	if len(records) != 1 || records[0].Detail != "token expired" {
		t.Fatalf("expiry audit = %+v", records)
	}
}
