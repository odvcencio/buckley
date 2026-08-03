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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// maxReadBytes bounds one file read through the broker.
	maxReadBytes = 256 * 1024
	// maxSearchResults bounds one text search.
	maxSearchResults = 200
	// maxListEntries bounds one directory listing.
	maxListEntries = 500
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

// Broker serves the capability surface for one run.
type Broker struct {
	root  string
	token string
	audit AuditSink

	server   *http.Server
	socket   string
	listener net.Listener
}

// NewBroker jails capabilities to workspaceRoot and wires the audit sink.
// Both are required; the token is generated per broker.
func NewBroker(workspaceRoot string, audit AuditSink) (*Broker, error) {
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
	return &Broker{root: root, token: hex.EncodeToString(buf), audit: audit}, nil
}

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
	mux.HandleFunc("/v1/files/read", b.handle("files.read", b.filesRead))
	mux.HandleFunc("/v1/files/list", b.handle("files.list", b.filesList))
	mux.HandleFunc("/v1/search/text", b.handle("search.text", b.searchText))
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

// filesList lists a directory, optionally recursively. Recursive listing
// exists so a program does not pay a model round-trip per subdirectory:
// one call returns the whole tree, workspace-relative.
func (b *Broker) filesList(params map[string]any) (any, error) {
	dir, _ := params["dir"].(string)
	if dir == "" {
		dir = "."
	}
	recursive, _ := params["recursive"].(bool)
	full, err := b.jail(dir)
	if err != nil {
		return nil, err
	}
	// Guidance over bare failure: a program that lists a file has made a
	// recoverable mistake, and saying so costs the model nothing to fix.
	if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("%s is a file, not a directory; use ReadFile for file contents", dir)
	}

	var names []string
	if recursive {
		err = filepath.WalkDir(full, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || len(names) >= maxListEntries {
				return walkErr
			}
			if path == full {
				return nil
			}
			if entry.IsDir() && skipDirName(entry.Name()) {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(b.root, path)
			if relErr != nil {
				return nil
			}
			if entry.IsDir() {
				rel += "/"
			}
			names = append(names, rel)
			return nil
		})
	} else {
		var entries []os.DirEntry
		entries, err = os.ReadDir(full)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
			if len(names) >= maxListEntries {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return map[string]any{"entries": names, "capped": len(names) >= maxListEntries}, nil
}

func skipDirName(name string) bool {
	return name == ".git" || name == "node_modules" || name == "vendor"
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
