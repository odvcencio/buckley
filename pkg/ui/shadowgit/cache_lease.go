package shadowgit

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"m31labs.dev/buckley/internal/filelock"
)

const cacheOwnerFile = "buckley-owner-v1"

type cacheLease struct {
	file *os.File
	ref  string
}

func (l *cacheLease) close() {
	if l != nil && l.file != nil {
		filelock.Unlock(l.file)
		_ = l.file.Close()
		l.file = nil
	}
}

func newCacheLease(dir, ref string) (*cacheLease, error) {
	file, err := openCacheLease(filepath.Join(dir, cacheOwnerFile), true)
	if err != nil {
		return nil, err
	}
	locked, err := filelock.TryLock(file)
	if err != nil || !locked {
		_ = file.Close()
		_ = os.Remove(file.Name())
		if err != nil {
			slog.Warn("Automatic undo cache recovery unavailable", "error", err)
		}
		return nil, nil // Filesystems without locks keep the existing unleased behavior.
	}
	lease := &cacheLease{file: file, ref: ref}
	if _, err := fmt.Fprintf(file, "buckley-undo-v1\n%s\n%s\n", runtime.GOOS, ref); err != nil {
		lease.close()
		return nil, err
	}
	return lease, nil
}

func abandonedLease(dir string) (*cacheLease, error) {
	name := filepath.Join(dir, cacheOwnerFile)
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) || err == nil && (!info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1024) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := openCacheLease(name, false)
	if cacheLeaseBusy(err) || errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lease := &cacheLease{file: file}
	keep := false
	defer func() {
		if !keep {
			lease.close()
		}
	}()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() > 1024 {
		return nil, err
	}
	locked, err := filelock.TryLock(file)
	if err != nil || !locked {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(body), "\n")
	if len(parts) != 4 || parts[0] != "buckley-undo-v1" || parts[1] != runtime.GOOS || parts[3] != "" {
		return nil, nil
	}
	ref := strings.TrimPrefix(parts[2], "refs/worktree/buckley/undo-live/")
	components := strings.Split(ref, "/")
	if ref == parts[2] || len(components) != 2 || SanitizeRefComponent(components[0]) != components[0] || SanitizeRefComponent(components[1]) != components[1] {
		return nil, nil
	}
	lease.ref, keep = parts[2], true
	return lease, nil
}

func sweepLock(parent string, wait bool) (*os.File, error) {
	name := filepath.Join(parent, ".reap.lock")
	if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("undo cleanup lock is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		locked, err := filelock.TryLock(file)
		if locked {
			return file, nil
		}
		if err != nil || !wait {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reapAbandonedStorage(parent string) error {
	lock, err := sweepLock(parent, false)
	if err != nil || lock == nil {
		return err
	}
	defer lock.Close()
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	var failure error
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "handle-") {
				continue
			}
			lease, err := abandonedLease(filepath.Join(parent, entry.Name()))
			if err != nil {
				if failure == nil {
					failure = err
				}
				continue
			}
			if lease == nil {
				continue
			}
			err = removeOwnedStorage(root, entry.Name(), lease.ref, lease)
			lease.close()
			if err != nil {
				if failure == nil {
					failure = err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return failure
		}
	}
}

func (s *Store) closeStorage() error {
	defer s.lease.close()
	if s.lease != nil {
		lock, err := sweepLock(filepath.Dir(s.gitDir), true)
		if err != nil {
			return err
		}
		defer lock.Close()
		if s.lease.file == nil {
			lease, err := abandonedLease(s.gitDir)
			if err != nil {
				return err
			}
			if lease == nil {
				if _, err := os.Stat(s.gitDir); errors.Is(err, os.ErrNotExist) {
					s.removed = true
					return nil
				}
				return fmt.Errorf("shadowgit: cache is not eligible for cleanup")
			}
			s.lease = lease
			defer lease.close()
		}
	}
	root, err := os.OpenRoot(filepath.Dir(s.gitDir))
	if err != nil {
		return err
	}
	defer root.Close()
	if err := removeOwnedStorage(root, filepath.Base(s.gitDir), s.ref, s.lease); err != nil {
		return err
	}
	s.removed = true
	clear(s.pending)
	s.pending = nil
	return nil
}
