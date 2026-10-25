package shadowgit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrStorageLimit leaves the published undo root unchanged. The caller may
// discard older records and retry with a smaller set of trees.
var ErrStorageLimit = errors.New("shadowgit: undo storage budget exceeded")

// retainedBytes charges private metadata and the unique objects reachable from
// root. Unreachable staging objects are removed only after the new ref commits.
func (s *Store) retainedBytes(ctx context.Context, root string) (int64, error) {
	objects := filepath.Join(s.gitDir, "objects")
	var total int64
	err := filepath.WalkDir(s.gitDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(name, objects+string(os.PathSeparator)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += allocatedBytes(info)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("shadowgit: account metadata: %w", err)
	}
	// A new ref may not exist yet. Its object ID fits in the same allocation
	// unit as the private HEAD file; existing ref storage is conservatively kept.
	head, err := os.Stat(filepath.Join(s.gitDir, "HEAD"))
	if err != nil {
		return 0, fmt.Errorf("shadowgit: account ref: %w", err)
	}
	total += allocatedBytes(head)
	cmd := s.command(ctx, []string{"GIT_COMMON_DIR=" + s.gitDir}, "rev-list", "--objects", "--no-object-names", root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("shadowgit: enumerate retained objects: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	var accountErr error
	for scanner.Scan() {
		id := scanner.Text()
		if len(id) != 40 && len(id) != 64 {
			accountErr = fmt.Errorf("invalid retained object ID %q", id)
			break
		}
		info, err := os.Lstat(filepath.Join(objects, id[:2], id[2:]))
		if err != nil {
			accountErr = err
			break
		}
		if !info.Mode().IsRegular() {
			accountErr = fmt.Errorf("retained object %s is not a regular loose object", id)
			break
		}
		total += allocatedBytes(info)
	}
	// Closing the pipe before Wait also unblocks a producer after accounting fails.
	_ = stdout.Close()
	err = errors.Join(accountErr, scanner.Err(), cmd.Wait())
	if err != nil {
		return 0, fmt.Errorf("shadowgit: account retained objects: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return total, nil
}
