// Package execmode is Buckley's code-execution surface (code-mode design,
// spore 2026-08-02): the model writes an ordinary Go program against a
// small set of typed capabilities, and safety sits below the program. The
// broker serves only read-only, workspace-jailed capabilities over a
// per-run unix socket with a per-run bearer token; the program's process
// gets a scrubbed environment; and every capability call must be audited
// before it answers — a run that cannot be recorded does not run.
package execmode

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/netutil"
)

const (
	// maxReadBytes bounds one file read through the broker.
	maxReadBytes = 256 * 1024
	// maxSearchFileBytes bounds searched file data, including symlink targets.
	maxSearchFileBytes = 2 * 1024 * 1024
	// maxSearchResults bounds one text search.
	maxSearchResults = 200
	// Match locations remain complete up to maxSearchResults, but line excerpts
	// cannot retain whole minified files or dominate the broker response.
	maxSearchMatchTextBytes = 2 * 1024
	maxSearchTextBytes      = 64 * 1024
	// maxListEntries bounds one files.list response. The generated client
	// transparently follows next_cursor, so programs still receive complete
	// listings without one oversized broker response.
	maxListEntries = 500
	// DefaultCapabilityCallLimit bounds the total broker operations one
	// exec_program can attempt internally, including rejected requests.
	// One terminal rejection is audited before the broker closes admission.
	DefaultCapabilityCallLimit = 32
	// Leave room for the terminal rejection when every admitted call has its
	// own keep-alive connection. Queued sockets stay outside the HTTP server.
	maxBrokerConnections = DefaultCapabilityCallLimit + 1
	maxBrokerHeaderBytes = 8 * 1024
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
	CapFilesRead   = "files.read"
	CapFilesList   = "files.list"
	CapSearchText  = "search.text"
	CapCodeOutline = "code.outline"
)

// CapabilitySet is a named grant. ReadOnlySet is everything the surface
// currently offers; MinimalSet keeps file access without search or structural
// parsing. Personas and phases pick a set; the broker enforces it below the program.
var (
	ReadOnlySet = []string{CapFilesRead, CapFilesList, CapSearchText, CapCodeOutline}
	MinimalSet  = []string{CapFilesRead, CapFilesList}
)

// DefaultTokenTTL bounds how long a run's capability token is accepted.
// The socket already dies with the broker; expiry is defense in depth for
// a token that outlives its run in a log, an env dump, or a core file.
const DefaultTokenTTL = 30 * time.Minute

// Broker serves the capability surface for one run.
type Broker struct {
	root        string
	token       string
	audit       AuditSink
	granted     map[string]bool
	expires     time.Time
	outline     OutlineFunc
	outlineSlot chan struct{}

	callMu            sync.Mutex
	capabilityCalls   int
	capabilityCallMax int
	budgetExhausted   bool

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
// budget, including rejected and malformed attempts. Non-positive values
// retain the safe default.
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
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("execmode: resolve workspace root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
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
		outlineSlot:       make(chan struct{}, 1),
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
	listener = netutil.LimitListener(listener, maxBrokerConnections)
	b.listener = listener
	b.socket = socketPath

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/files/read", b.handle(CapFilesRead, b.filesRead))
	mux.HandleFunc("/v1/files/list", b.handleContext(CapFilesList, b.filesList))
	mux.HandleFunc("/v1/search/text", b.handleContext(CapSearchText, b.searchText))
	mux.HandleFunc("/v1/code/outline", b.handleContext(CapCodeOutline, b.codeOutline))
	b.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    maxBrokerHeaderBytes,
	}
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
	return b.handleContext(method, func(_ context.Context, params map[string]any) (any, error) {
		return capability(params)
	})
}

func (b *Broker) handleContext(method string, capability func(context.Context, map[string]any) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, terminal := b.consumeCapabilityCall()
		if !allowed {
			if !terminal {
				// Already accepted connections get no further unaudited responses.
				panic(http.ErrAbortHandler)
			}
			if b.listener != nil {
				_ = b.listener.Close()
			}
			w.Header().Set("Connection", "close")
			detail := fmt.Sprintf("capability call budget exhausted (%d per program)", b.capabilityCallMax)
			b.deny(w, method, detail, detail, http.StatusTooManyRequests)
			return
		}
		if r.Method != http.MethodPost {
			b.deny(w, method, "POST only", "POST only", http.StatusMethodNotAllowed)
			return
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(b.token)) != 1 {
			b.deny(w, method, "bad token", "unauthorized", http.StatusUnauthorized)
			return
		}
		if !b.expires.IsZero() && time.Now().After(b.expires) {
			b.deny(w, method, "token expired", "capability token expired", http.StatusUnauthorized)
			return
		}
		// Grant check sits above the capability itself: a token proves who
		// is calling, the grant decides what they may call.
		if !b.granted[method] {
			b.deny(w, method, "capability not granted to this run", "capability "+method+" is not granted to this run", http.StatusForbidden)
			return
		}

		var params map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&params); err != nil {
			b.deny(w, method, "invalid params", "invalid params", http.StatusBadRequest)
			return
		}
		paramsJSON, _ := json.Marshal(params)

		result, err := capability(r.Context(), params)
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

func (b *Broker) deny(w http.ResponseWriter, method, detail, message string, status int) {
	err := b.audit.Record(AuditRecord{Method: method, Outcome: "denied", Detail: detail, Timestamp: time.Now().UTC()})
	if err != nil {
		http.Error(w, "capability call could not be audited: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Error(w, message, status)
}

func (b *Broker) consumeCapabilityCall() (allowed, terminal bool) {
	b.callMu.Lock()
	defer b.callMu.Unlock()
	if b.capabilityCalls < b.capabilityCallMax {
		b.capabilityCalls++
		return true, false
	}
	if !b.budgetExhausted {
		b.budgetExhausted = true
		return false, true
	}
	return false, false
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
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(b.root, full)
	if err != nil {
		return nil, err
	}
	f, err := root.OpenFile(rel, brokerReadFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("files.read requires a regular file")
	}
	var offset int64
	// Decimal strings preserve int64 offsets through JSON without rounding.
	raw, chunk := params["offset"]
	if chunk {
		s, isStr := raw.(string)
		if !isStr {
			return nil, fmt.Errorf("files.read offset must be a decimal string, got %T", raw)
		}
		off, parseErr := strconv.ParseInt(s, 10, 64)
		if parseErr != nil || off < 0 {
			return nil, fmt.Errorf("files.read invalid offset %q: want a non-negative decimal integer", s)
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek to offset %d: %w", off, err)
		}
		offset = off
	}
	// Read at most one byte past the cap so an exactly maxReadBytes file
	// still reports truncated=false without pulling the whole file in.
	data, readErr := readBoundedFile(f, info.Size()-offset, maxReadBytes+1)
	if readErr != nil {
		return nil, readErr
	}
	truncated := len(data) > maxReadBytes
	if truncated {
		data = data[:maxReadBytes]
	}
	if chunk {
		return map[string]any{"data": data, "truncated": truncated}, nil
	}
	return map[string]any{"content": string(data), "truncated": truncated}, nil
}

// filesList returns one bounded page of a directory listing. Recursive listing
// lets the generated client fetch a whole tree without model round-trips; the
// client follows next_cursor transparently and returns workspace-relative paths.
func (b *Broker) filesList(ctx context.Context, params map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(b.root, full)
	if err != nil {
		return nil, err
	}
	if recursive {
		names, hasMore, err = recursiveDirectoryPage(ctx, root, rel, cursor)
	} else {
		entries, total, err := flatDirectoryPage(ctx, root, rel, cursor)
		if err != nil {
			return nil, err
		}
		cursor = min(cursor, total)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := entry.name
			if entry.mode.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		hasMore = cursor+len(names) < total
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
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
func (b *Broker) searchText(ctx context.Context, params map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pattern, _ := params["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	needle := []byte(pattern)
	glob, _ := params["glob"].(string)
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return nil, fmt.Errorf("invalid glob %q: %w", glob, err)
		}
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	type match struct {
		File          string `json:"file"`
		Line          int    `json:"line"`
		Text          string `json:"text"`
		TextTruncated bool   `json:"text_truncated,omitempty"`
	}
	var matches []match
	textBytes := 0
	var data []byte
	err = walkDirectory(ctx, root, ".", glob, func() int { return maxDirectoryPrefix }, func(rel string, entry directoryName) error {
		if entry.mode.IsDir() {
			return nil
		}
		readName := rel
		if entry.mode&os.ModeSymlink != 0 {
			full, err := b.jail(rel)
			if err != nil {
				return nil
			}
			readName, err = filepath.Rel(b.root, full)
			if err != nil {
				return nil
			}
		}
		f, err := root.OpenFile(readName, brokerReadFlags, 0)
		if err != nil {
			return nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSearchFileBytes {
			return nil
		}
		data, err = readBoundedFileInto(f, info.Size(), maxSearchFileBytes+1, data)
		if err != nil || len(data) > maxSearchFileBytes || bytes.IndexByte(data[:min(len(data), 1024)], 0) >= 0 {
			return nil
		}
		lineNumber := 0
		for line := range bytes.SplitSeq(data, []byte{'\n'}) {
			if lineNumber%256 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			lineNumber++
			if index := bytes.Index(line, needle); index >= 0 {
				text, truncated := searchTextExcerpt(line, index, min(maxSearchMatchTextBytes, maxSearchTextBytes-textBytes))
				textBytes += len(text)
				matches = append(matches, match{File: rel, Line: lineNumber, Text: text, TextTruncated: truncated})
				if len(matches) >= maxSearchResults {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, err
	}
	return map[string]any{"matches": matches, "capped": len(matches) >= maxSearchResults}, nil
}

func searchTextExcerpt(line []byte, matchOffset, limit int) (string, bool) {
	text := bytes.TrimSpace(line)
	if len(text) <= limit && utf8.Valid(text) {
		return string(text), false
	}
	if limit == 0 {
		return "", true
	}
	matchOffset -= len(line) - len(bytes.TrimLeftFunc(line, unicode.IsSpace))
	start := max(0, matchOffset-min(80, limit/4))
	end := min(len(text), start+limit)
	for start < end && !utf8.RuneStart(text[start]) {
		start++
	}
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	excerpt := strings.ToValidUTF8(string(text[start:end]), "\uFFFD")
	if len(excerpt) > limit {
		end = limit
		for end > 0 && !utf8.RuneStart(excerpt[end]) {
			end--
		}
		excerpt = excerpt[:end]
	}
	return excerpt, true
}

// A file size is only an allocation hint: concurrent growth must still fit the
// byte budget, and a shrinking file must not return zero padding.
func readBoundedFile(r io.Reader, sizeHint int64, limit int) ([]byte, error) {
	data, err := readBoundedFileInto(r, sizeHint, limit, nil)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// The returned bytes borrow data's storage. Retained results must copy their
// text before another file reuses the buffer; its capacity never exceeds limit.
func readBoundedFileInto(r io.Reader, sizeHint int64, limit int, data []byte) ([]byte, error) {
	data = data[:0:min(cap(data), limit)]
	size := int(min(max(sizeHint, 0), int64(limit-1))) + 1
	if cap(data) < size {
		data = make([]byte, size, min(limit, max(size, 2*cap(data))))
	} else {
		data = data[:size]
	}
	filled := 0
	for {
		n, err := io.ReadFull(r, data[filled:])
		filled += n
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return data[:filled], nil
		}
		if err != nil || filled == limit {
			return data[:filled], err
		}
		next := min(limit, max(512, 2*len(data)))
		if next <= cap(data) {
			data = data[:next]
		} else {
			grown := make([]byte, next)
			copy(grown, data)
			data = grown
		}
	}
}
