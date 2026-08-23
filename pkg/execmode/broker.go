// Package execmode is Buckley's code-execution surface (code-mode design,
// spore 2026-08-02): the model writes an ordinary Go program against a
// small set of typed capabilities, and safety sits below the program. The
// broker serves only read-only, workspace-jailed capabilities over a
// per-run unix socket with a per-run bearer token; the program's process
// gets a scrubbed environment; and every capability call must be audited
// before it answers — a run that cannot be recorded does not run.
package execmode

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// maxReadBytes bounds one file read through the broker.
	maxReadBytes = 256 * 1024
	// maxSearchResults bounds one text search.
	maxSearchResults = 200
	// maxListEntries bounds one files.list response. The generated client
	// transparently follows next_cursor, so programs still receive complete
	// listings without one oversized broker response.
	maxListEntries = 500
	// DefaultCapabilityCallLimit bounds the total broker operations one
	// exec_program can fan out internally. Program-level composition remains
	// useful, but it cannot turn one model tool call into unbounded crawling.
	DefaultCapabilityCallLimit = 32
)

// AuditRecord is one capability call's durable trace: what was asked,
// what happened, when. The full truth of a run is its ordered records.
type AuditRecord struct {
	Method    string    `json:"method"`
	Params    string    `json:"params"`
	Outcome   string    `json:"outcome"` // ok | denied | error
	Detail    string    `json:"detail,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// AuditSink receives every capability call before its response is sent.
// A sink error fails the call: unrecorded capability use is not allowed.
type AuditSink interface {
	Record(record AuditRecord) error
}

// AuditSinkFunc adapts a function to AuditSink.
type AuditSinkFunc func(record AuditRecord) error

// Record implements AuditSink.
func (f AuditSinkFunc) Record(record AuditRecord) error { return f(record) }

// Capability names. A broker serves only the capabilities its grant
// allows; anything else is denied and audited, even with a valid token.
const (
	CapFilesRead  = "files.read"
	CapFilesList  = "files.list"
	CapSearchText = "search.text"
)

// CapabilitySet is a named grant. ReadOnlySet is everything the surface
// currently offers; MinimalSet drops whole-tree search, which is the
// expensive capability on a large repository. Personas and phases pick a
// set; the broker enforces it below the program.
var (
	ReadOnlySet = []string{CapFilesRead, CapFilesList, CapSearchText}
	MinimalSet  = []string{CapFilesRead, CapFilesList}
)

// DefaultTokenTTL bounds how long a run's capability token is accepted.
// The socket already dies with the broker; expiry is defense in depth for
// a token that outlives its run in a log, an env dump, or a core file.
const DefaultTokenTTL = 30 * time.Minute

// Broker serves the capability surface for one run.
type Broker struct {
	root    string
	token   string
	audit   AuditSink
	granted map[string]bool
	expires time.Time

	callMu            sync.Mutex
	capabilityCalls   int
	capabilityCallMax int

	server   *http.Server
	socket   string
	listener net.Listener
}

// BrokerOption configures NewBroker.
type BrokerOption func(*Broker)

// WithCapabilities restricts the broker to a named capability set. The
// default is ReadOnlySet. An empty grant serves nothing.
func WithCapabilities(capabilities ...string) BrokerOption {
	return func(b *Broker) {
		b.granted = make(map[string]bool, len(capabilities))
		for _, capability := range capabilities {
			b.granted[capability] = true
		}
	}
}

// WithTokenTTL overrides how long the run's token is accepted.
func WithTokenTTL(ttl time.Duration) BrokerOption {
	return func(b *Broker) {
		if ttl > 0 {
			b.expires = time.Now().Add(ttl)
		}
	}
}

// WithCapabilityCallLimit overrides the per-program capability operation
// budget. Non-positive values retain the safe default.
func WithCapabilityCallLimit(limit int) BrokerOption {
	return func(b *Broker) {
		if limit > 0 {
			b.capabilityCallMax = limit
		}
	}
}

// NewBroker jails capabilities to workspaceRoot and wires the audit sink.
// Both are required; the token is generated per broker and expires.
func NewBroker(workspaceRoot string, audit AuditSink, opts ...BrokerOption) (*Broker, error) {
	if audit == nil {
		return nil, fmt.Errorf("execmode: an audit sink is required; unaudited capability use is not allowed")
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("execmode: resolve workspace root: %w", err)
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("execmode: generate token: %w", err)
	}
	broker := &Broker{
		root:              root,
		token:             hex.EncodeToString(buf),
		audit:             audit,
		expires:           time.Now().Add(DefaultTokenTTL),
		capabilityCallMax: DefaultCapabilityCallLimit,
	}
	WithCapabilities(ReadOnlySet...)(broker)
	for _, opt := range opts {
		opt(broker)
	}
	return broker, nil
}

// Granted reports whether the broker serves a capability.
func (b *Broker) Granted(capability string) bool { return b.granted[capability] }

// Token returns the per-run bearer token.
func (b *Broker) Token() string { return b.token }

// SocketPath returns the listening socket path once Start has run.
func (b *Broker) SocketPath() string { return b.socket }

// Start listens on a unix socket at socketPath and serves until Close.
func (b *Broker) Start(socketPath string) error {
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("execmode: listen on %s: %w", socketPath, err)
	}
	b.listener = listener
	b.socket = socketPath

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/files/read", b.handle(CapFilesRead, b.filesRead))
	mux.HandleFunc("/v1/files/list", b.handle(CapFilesList, b.filesList))
	mux.HandleFunc("/v1/search/text", b.handle(CapSearchText, b.searchText))
	b.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.server.Serve(listener) }()
	return nil
}

// Close stops the broker and removes the socket.
func (b *Broker) Close() {
	if b.server != nil {
		_ = b.server.Close()
	}
	if b.socket != "" {
		_ = os.Remove(b.socket)
	}
}

type capabilityFunc func(params map[string]any) (any, error)

// handle wraps one capability: auth, decode, audit, execute, respond.
// The audit record is written BEFORE the response; a sink failure turns
// into a call failure.
func (b *Broker) handle(method string, capability capabilityFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(b.token)) != 1 {
			_ = b.audit.Record(AuditRecord{Method: method, Outcome: "denied", Detail: "bad token", Timestamp: time.Now().UTC()})
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !b.expires.IsZero() && time.Now().After(b.expires) {
			_ = b.audit.Record(AuditRecord{Method: method, Outcome: "denied", Detail: "token expired", Timestamp: time.Now().UTC()})
			http.Error(w, "capability token expired", http.StatusUnauthorized)
			return
		}
		// Grant check sits above the capability itself: a token proves who
		// is calling, the grant decides what they may call.
		if !b.granted[method] {
			_ = b.audit.Record(AuditRecord{Method: method, Outcome: "denied", Detail: "capability not granted to this run", Timestamp: time.Now().UTC()})
			http.Error(w, "capability "+method+" is not granted to this run", http.StatusForbidden)
			return
		}
		if !b.consumeCapabilityCall() {
			detail := fmt.Sprintf("capability call budget exhausted (%d per program)", b.capabilityCallMax)
			_ = b.audit.Record(AuditRecord{Method: method, Outcome: "denied", Detail: detail, Timestamp: time.Now().UTC()})
			http.Error(w, detail, http.StatusTooManyRequests)
			return
		}

		var params map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&params); err != nil {
			http.Error(w, "invalid params", http.StatusBadRequest)
			return
		}
		paramsJSON, _ := json.Marshal(params)

		result, err := capability(params)
		record := AuditRecord{Method: method, Params: string(paramsJSON), Outcome: "ok", Timestamp: time.Now().UTC()}
		if err != nil {
			record.Outcome = "error"
			record.Detail = err.Error()
		}
		if auditErr := b.audit.Record(record); auditErr != nil {
			http.Error(w, "capability call could not be audited: "+auditErr.Error(), http.StatusInternalServerError)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

func (b *Broker) consumeCapabilityCall() bool {
	b.callMu.Lock()
	defer b.callMu.Unlock()
	if b.capabilityCalls >= b.capabilityCallMax {
		return false
	}
	b.capabilityCalls++
	return true
}

// jail resolves a workspace-relative path and rejects anything that
// escapes the root, including through symlinks.
func (b *Broker) jail(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be workspace-relative")
	}
	full := filepath.Join(b.root, rel)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rel, err)
	}
	if resolved != b.root && !strings.HasPrefix(resolved, b.root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes the workspace", rel)
	}
	return resolved, nil
}

func (b *Broker) filesRead(params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	full, err := b.jail(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(data) > maxReadBytes {
		data = data[:maxReadBytes]
		truncated = true
	}
	return map[string]any{"content": string(data), "truncated": truncated}, nil
}

// filesList returns one bounded page of a directory listing. Recursive listing
// lets the generated client fetch a whole tree without model round-trips; the
// client follows next_cursor transparently and returns workspace-relative paths.
func (b *Broker) filesList(params map[string]any) (any, error) {
	dir, _ := params["dir"].(string)
	if dir == "" {
		dir = "."
	}
	recursive, _ := params["recursive"].(bool)
	cursor, err := listCursor(params["cursor"])
	if err != nil {
		return nil, err
	}
	full, err := b.jail(dir)
	if err != nil {
		return nil, err
	}
	// Guidance over bare failure: a program that lists a file has made a
	// recoverable mistake, and saying so costs the model nothing to fix.
	if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("%s is a file, not a directory; use ReadFile for file contents", dir)
	}

	var (
		names   []string
		hasMore bool
	)
	if recursive {
		seen := 0
		err = filepath.WalkDir(full, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == full {
				return nil
			}
			if entry.IsDir() && skipDirName(entry.Name()) {
				return filepath.SkipDir
			}
			if seen < cursor {
				seen++
				return nil
			}
			if len(names) >= maxListEntries {
				hasMore = true
				return fs.SkipAll
			}
			rel, relErr := filepath.Rel(b.root, path)
			if relErr != nil {
				return nil
			}
			if entry.IsDir() {
				rel += "/"
			}
			names = append(names, rel)
			seen++
			return nil
		})
	} else {
		var entries []os.DirEntry
		entries, err = os.ReadDir(full)
		if cursor > len(entries) {
			cursor = len(entries)
		}
		end := cursor + maxListEntries
		if end > len(entries) {
			end = len(entries)
		}
		for _, entry := range entries[cursor:end] {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		hasMore = end < len(entries)
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return map[string]any{
		"entries":     names,
		"capped":      hasMore,
		"next_cursor": cursor + len(names),
	}, nil
}

func listCursor(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	switch cursor := value.(type) {
	case int:
		if cursor >= 0 {
			return cursor, nil
		}
	case float64:
		asInt := int(cursor)
		if cursor >= 0 && float64(asInt) == cursor {
			return asInt, nil
		}
	}
	return 0, fmt.Errorf("cursor must be a non-negative integer")
}

func skipDirName(name string) bool {
	return name == ".git" || name == ".worktrees" || name == "node_modules" || name == "vendor"
}

// searchText finds literal matches, optionally restricted to files whose
// base name matches a glob ("*.go"). The glob filter turns "search then
// filter in the program" into one call, and keeps the result set inside
// the cap for large repositories.
func (b *Broker) searchText(params map[string]any) (any, error) {
	pattern, _ := params["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	glob, _ := params["glob"].(string)
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return nil, fmt.Errorf("invalid glob %q: %w", glob, err)
		}
	}
	type match struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var matches []match
	err := filepath.WalkDir(b.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || len(matches) >= maxSearchResults {
			return walkErr
		}
		if entry.IsDir() {
			if skipDirName(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, entry.Name()); !ok {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2*1024*1024 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data[:min(len(data), 1024)]), "\x00") {
			return nil
		}
		rel, _ := filepath.Rel(b.root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, pattern) {
				matches = append(matches, match{File: rel, Line: i + 1, Text: strings.TrimSpace(line)})
				if len(matches) >= maxSearchResults {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"matches": matches, "capped": len(matches) >= maxSearchResults}, nil
}
