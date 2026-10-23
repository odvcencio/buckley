// Package verifyreceipt records proof that a verification command ran, bound
// to the git tree it ran against, and loads that proof back for reviews.
//
// A receipt holds the repository, commit, tree hash, command, exit status,
// timing, environment and a digest of the combined log. The log itself is
// kept in the evidence store, so a loader can recompute the digest. Receipts
// are not signed: Buckley has no signing facility, so the tree hash and log
// digest guard against accidental reuse, not against a user who edits the
// ledger.
package verifyreceipt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
)

// Schema names the receipt format.
const Schema = "buckley.verify-receipt.v1"

// MaxLogBytes caps the log kept per receipt. A longer log is cut and flagged.
const MaxLogBytes = 16 << 20

// Status is the outcome of the recorded command.
type Status string

const (
	StatusPass Status = "PASS"
	StatusFail Status = "FAIL"
)

// Environment describes where the command ran.
type Environment struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Hostname    string `json:"hostname"`
	GoVersion   string `json:"go_version,omitempty"`
	NodeVersion string `json:"node_version,omitempty"`
}

// Receipt is the stored record of one verification run.
type Receipt struct {
	Schema       string      `json:"schema"`
	ID           string      `json:"-"`
	Name         string      `json:"name,omitempty"`
	Repo         string      `json:"repo"`
	Commit       string      `json:"commit"`
	Tree         string      `json:"tree"`
	Argv         []string    `json:"argv"`
	Dir          string      `json:"dir"`
	Paths        []string    `json:"paths"`
	ExitCode     int         `json:"exit_code"`
	Status       Status      `json:"status"`
	StartedAt    time.Time   `json:"started_at"`
	DurationMS   int64       `json:"duration_ms"`
	LogEvidence  string      `json:"log_evidence_id"`
	LogSHA256    string      `json:"log_sha256"`
	LogBytes     int64       `json:"log_bytes"`
	LogTruncated bool        `json:"log_truncated,omitempty"`
	Env          Environment `json:"environment"`
	Recorder     string      `json:"recorder"`
}

// Command renders the argv for display.
func (r Receipt) Command() string { return strings.Join(r.Argv, " ") }

// RecordOptions describes one run to record.
type RecordOptions struct {
	// Dir is any directory inside the repository's work tree.
	Dir string
	// Argv is the command to run, executed from Dir.
	Argv []string
	// Paths are the repository-relative globs the command covers.
	Paths []string
	// Name is an optional label from the declared verification list.
	Name string
	// Recorder is "run" or "record".
	Recorder string
	// Output receives the command's combined output as it is produced.
	Output io.Writer
	// Now overrides the clock in tests.
	Now func() time.Time
}

// ErrDirtyTree means the work tree has uncommitted changes, so a receipt could
// not be tied to one commit.
var ErrDirtyTree = errors.New("working tree is not clean (including changes hidden by index flags); commit or stash changes so the receipt matches one tree")

// Record runs opts.Argv in the real work tree and stores a receipt, with the
// log, in store. It refuses a dirty tree before the run and refuses to record
// if tracked files or HEAD changed during it. A command that exits non-zero
// still produces a (FAIL) receipt; the returned error is nil in that case.
func Record(ctx context.Context, store *evidence.SQLiteStore, opts RecordOptions) (Receipt, error) {
	if store == nil {
		return Receipt{}, errors.New("verify: evidence store is required")
	}
	if len(opts.Argv) == 0 {
		return Receipt{}, errors.New("verify: a command is required")
	}
	if len(opts.Paths) == 0 {
		return Receipt{}, errors.New("verify: the receipt needs at least one covered path glob")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	before, err := StateOf(ctx, opts.Dir)
	if err != nil {
		return Receipt{}, err
	}
	if before.Dirty {
		return Receipt{}, ErrDirtyTree
	}

	var log limitedBuffer
	log.max = MaxLogBytes
	var sink io.Writer = &log
	if opts.Output != nil {
		sink = io.MultiWriter(&log, opts.Output)
	}
	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Stdout, cmd.Stderr = sink, sink
	started := now()
	runErr := cmd.Run()
	duration := now().Sub(started)

	exit := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(runErr, &exitErr):
			exit = exitErr.ExitCode()
		default:
			return Receipt{}, fmt.Errorf("verify: start %q: %w", opts.Argv[0], runErr)
		}
	}

	after, err := StateOf(ctx, opts.Dir)
	if err != nil {
		return Receipt{}, err
	}
	if after.Commit != before.Commit || after.Tree != before.Tree || after.TrackedChanged {
		return Receipt{}, errors.New("verify: the command changed HEAD or tracked files, so the result cannot be tied to one tree; no receipt recorded")
	}

	logBody := log.Bytes()
	sum := sha256.Sum256(logBody)
	logObj, err := store.Put(ctx, evidence.Object{
		Kind:       evidence.KindTestOutput,
		MediaType:  "text/plain",
		InlineBody: logBody,
		Metadata:   map[string]any{"verification_log_for_tree": before.Tree},
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("verify: store log: %w", err)
	}
	status := StatusPass
	if exit != 0 {
		status = StatusFail
	}
	receipt := Receipt{
		Schema:       Schema,
		Name:         opts.Name,
		Repo:         before.Repo,
		Commit:       before.Commit,
		Tree:         before.Tree,
		Argv:         append([]string(nil), opts.Argv...),
		Dir:          before.RelDir,
		Paths:        append([]string(nil), opts.Paths...),
		ExitCode:     exit,
		Status:       status,
		StartedAt:    started.UTC(),
		DurationMS:   duration.Milliseconds(),
		LogEvidence:  logObj.ID,
		LogSHA256:    hex.EncodeToString(sum[:]),
		LogBytes:     int64(len(logBody)),
		LogTruncated: log.truncated,
		Env:          currentEnvironment(ctx, opts.Dir),
		Recorder:     opts.Recorder,
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	obj, err := store.Put(ctx, evidence.Object{
		Kind:       evidence.KindVerificationReceipt,
		MediaType:  "application/json",
		InlineBody: body,
		Metadata: map[string]any{
			"tree":   receipt.Tree,
			"commit": receipt.Commit,
			"status": string(receipt.Status),
			"repo":   receipt.Repo,
		},
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("verify: store receipt: %w", err)
	}
	if err := store.PinVerificationReceipt(ctx, obj.ID, logObj.ID); err != nil {
		return Receipt{}, fmt.Errorf("verify: retain receipt and log: %w", err)
	}
	receipt.ID = obj.ID
	return receipt, nil
}

// Loaded is the result of reading receipts for one tree.
type Loaded struct {
	Receipts []Receipt
	// Rejected explains each stored receipt for the tree that failed
	// verification, such as a log whose digest no longer matches.
	Rejected []string
}

// Load returns the receipts recorded for tree, newest first. A receipt is
// rejected when its recorded tree differs from the requested tree, its schema
// is unknown, or its log is missing or no longer matches the stored digest.
func Load(ctx context.Context, store *evidence.SQLiteStore, tree string) (Loaded, error) {
	var out Loaded
	tree = strings.TrimSpace(tree)
	if store == nil || tree == "" {
		return out, nil
	}
	summaries, err := store.Query(ctx, evidence.Query{Kinds: []evidence.Kind{evidence.KindVerificationReceipt}, Tree: tree, Limit: 5000})
	if err != nil {
		return out, err
	}
	for _, summary := range summaries {
		if got, _ := summary.Metadata["tree"].(string); got != tree {
			continue
		}
		obj, err := store.Get(ctx, summary.ID)
		if err != nil {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: unreadable: %v", summary.ID, err))
			continue
		}
		var receipt Receipt
		if err := json.Unmarshal(obj.InlineBody, &receipt); err != nil || receipt.Schema != Schema {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: unknown receipt format", summary.ID))
			continue
		}
		receipt.ID = summary.ID
		if receipt.Tree != tree {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: recorded tree %s does not match %s", summary.ID, short(receipt.Tree), short(tree)))
			continue
		}
		logObj, err := store.Get(ctx, receipt.LogEvidence)
		if err != nil {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: log %s unavailable: %v", summary.ID, receipt.LogEvidence, err))
			continue
		}
		sum := sha256.Sum256(logObj.InlineBody)
		if hex.EncodeToString(sum[:]) != receipt.LogSHA256 {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: log digest does not match the receipt", summary.ID))
			continue
		}
		out.Receipts = append(out.Receipts, receipt)
	}
	return out, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

func currentEnvironment(ctx context.Context, dir string) Environment {
	host, _ := os.Hostname()
	env := Environment{OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host}
	probe := func(name string, args ...string) string {
		path, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(c, path, args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	env.GoVersion = probe("go", "env", "GOVERSION")
	env.NodeVersion = probe("node", "--version")
	return env
}
