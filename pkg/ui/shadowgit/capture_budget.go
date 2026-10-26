package shadowgit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrCaptureLimit means the snapshot's raw inputs exceed its configured budget.
// The worktree and published retention root are unchanged by the refusal.
var ErrCaptureLimit = errors.New("shadowgit: capture input budget exceeded")

// Option configures a private snapshot store.
type Option func(*Store)

// WithCaptureLimit caps raw file and pathname bytes before Git stages a snapshot.
// Zero disables the preflight limit. Filters and concurrent file growth can
// produce additional output; this is an input limit, not a filesystem quota.
func WithCaptureLimit(limit int64) Option {
	return func(store *Store) { store.captureLimit = limit }
}

func (s *Store) checkCaptureInputs(ctx context.Context, env []string) error {
	if s.captureLimit == 0 {
		return nil
	}
	root, err := os.OpenRoot(s.workTree)
	if err != nil {
		return fmt.Errorf("shadowgit: inspect capture inputs: %w", err)
	}
	defer root.Close()
	cmd := s.command(ctx, env, "ls-files", "--cached", "--others", "--exclude-standard", "--deduplicate", "--full-name", "-z", "--", ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("shadowgit: enumerate capture inputs: %w", err)
	}
	reader := bufio.NewReader(stdout)
	var used int64
	var inputErr error
	for {
		name, err := reader.ReadString(0)
		if errors.Is(err, io.EOF) && name == "" {
			break
		}
		if err != nil {
			inputErr = err
			break
		}
		name = strings.TrimSuffix(name, "\x00")
		info, err := root.Lstat(filepath.FromSlash(name))
		if errors.Is(err, os.ErrNotExist) {
			continue // Git will stage a file that disappeared as a deletion.
		}
		if err != nil {
			inputErr = err
			break
		}
		if info.IsDir() {
			continue // Embedded repositories are Git links, not file contents.
		}
		if info.Size() > s.captureLimit-used || int64(len(name)) > s.captureLimit-used-info.Size() {
			inputErr = fmt.Errorf("%w: %q has %d bytes, limit %d", ErrCaptureLimit, name, info.Size(), s.captureLimit)
			break
		}
		used += info.Size() + int64(len(name))
	}
	_ = stdout.Close()
	commandErr := cmd.Wait()
	if errors.Is(inputErr, ErrCaptureLimit) {
		return inputErr // Closing the pipe intentionally stops a large listing.
	}
	if err := errors.Join(inputErr, commandErr); err != nil {
		return fmt.Errorf("shadowgit: inspect capture inputs: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
