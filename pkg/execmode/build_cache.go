package execmode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/internal/filelock"
)

const (
	// DefaultBuildCacheBytes bounds retained build artifacts when no programs use the cache.
	// Active compilation can temporarily exceed this limit.
	DefaultBuildCacheBytes   int64 = 256 << 20
	DefaultBuildCacheEntries       = 4096
)

type buildCacheLease struct {
	dir        string
	users      *os.File
	maxBytes   int64
	maxEntries int
}

func cacheLock(ctx context.Context, file *os.File, shared bool) error {
	try := filelock.TryLock
	if shared {
		try = filelock.TryReadLock
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := try(file)
		if err != nil || ok {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func openCacheLock(dir, suffix string) (*os.File, error) {
	return os.OpenFile(dir+suffix, os.O_CREATE|os.O_RDWR, 0600)
}

func acquireBuildCache(ctx context.Context, dir string, maxBytes int64, maxEntries int) (*buildCacheLease, error) {
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("unsafe build cache directory %s", path)
		}
	}
	admission, err := openCacheLock(dir, ".admission.lock")
	if err != nil {
		return nil, err
	}
	defer admission.Close()
	if err := cacheLock(ctx, admission, false); err != nil {
		return nil, err
	}
	defer filelock.Unlock(admission)
	users, err := openCacheLock(dir, ".users.lock")
	if err != nil {
		return nil, err
	}
	lease := &buildCacheLease{dir: dir, users: users, maxBytes: maxBytes, maxEntries: maxEntries}
	idle, err := filelock.TryLock(users)
	if err == nil && !idle {
		var over bool
		over, err = buildCacheOverBudget(dir, maxBytes, maxEntries)
		if err == nil && over {
			// Holding admission drains existing users without admitting another writer.
			err = cacheLock(ctx, users, false)
			idle = err == nil
		}
	}
	if err == nil && idle {
		err = trimBuildCache(dir, maxBytes, maxEntries)
		filelock.Unlock(users)
	}
	if err == nil {
		err = cacheLock(ctx, users, true)
	}
	if err != nil {
		users.Close()
		return nil, err
	}
	return lease, nil
}

func (l *buildCacheLease) Close() error {
	filelock.Unlock(l.users)
	defer l.users.Close()
	idle, err := filelock.TryLock(l.users)
	if err != nil || !idle {
		return err
	}
	defer filelock.Unlock(l.users)
	return trimBuildCache(l.dir, l.maxBytes, l.maxEntries)
}

type buildCacheEntry struct {
	name       string
	bytes      int64
	used       time.Time
	invalid    bool
	executable bool
}

func cacheHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// Inventory is bounded even when upgrading from a much larger cache. It only
// inspects Go's flat artifacts and one-file executable directories, never links.
func scanBuildCache(root *os.Root, limit int) ([]buildCacheEntry, bool, error) {
	var entries []buildCacheEntry
	add := func(name string, info os.FileInfo, artifact bool) error {
		entry := buildCacheEntry{name: name, used: info.ModTime(), bytes: info.Size()}
		base := filepath.Base(name)
		valid := artifact && len(base) == 66 && cacheHex(base[:64]) && (strings.HasSuffix(base, "-a") || strings.HasSuffix(base, "-d"))
		if info.IsDir() && valid && strings.HasSuffix(base, "-d") {
			entry.executable = true
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			children, err := f.ReadDir(2)
			f.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			valid = len(children) == 1 && children[0].Type().IsRegular()
			if valid {
				child, err := children[0].Info()
				if err != nil {
					return err
				}
				entry.bytes = child.Size()
			}
		} else if !info.Mode().IsRegular() {
			valid = false
		}
		if !artifact && info.Mode().IsRegular() && (base == "README" || base == "trim.txt") {
			valid = true
		}
		entry.invalid = !valid
		entries = append(entries, entry)
		return nil
	}
	read := func(dir string, shard bool) error {
		f, err := root.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			batch, err := f.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, d := range batch {
				name := filepath.Join(dir, d.Name())
				info, e := root.Lstat(name)
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					return e
				}
				if !shard && info.IsDir() && len(d.Name()) == 2 && cacheHex(d.Name()) {
					continue
				}
				if e := add(name, info, shard); e != nil {
					if errors.Is(e, os.ErrNotExist) {
						continue
					}
					return e
				}
				if len(entries) >= limit {
					return io.EOF
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	if err := read(".", false); err != nil {
		return entries, errors.Is(err, io.EOF), nilIfEOF(err)
	}
	for i := 0; i < 256; i++ {
		dir := fmt.Sprintf("%02x", i)
		info, err := root.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if !info.IsDir() {
			continue
		}
		if err := read(dir, true); err != nil {
			return entries, errors.Is(err, io.EOF), nilIfEOF(err)
		}
	}
	return entries, false, nil
}

func nilIfEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func buildCacheOverBudget(dir string, maxBytes int64, maxEntries int) (bool, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	entries, more, err := scanBuildCache(root, maxEntries+1)
	if err != nil {
		return false, err
	}
	var size int64
	for _, entry := range entries {
		if entry.invalid {
			return true, nil
		}
		size += entry.bytes
	}
	return more || len(entries) > maxEntries || size > maxBytes, nil
}

func trimBuildCache(dir string, maxBytes int64, maxEntries int) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for {
		entries, more, err := scanBuildCache(root, 2*maxEntries)
		if err != nil {
			return err
		}
		var size int64
		for _, entry := range entries {
			size += entry.bytes
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].invalid != entries[j].invalid {
				return entries[i].invalid
			}
			// Go only refreshes usage mtimes hourly. Prefer dropping linked
			// programs over repeatedly rebuilding hot standard-library archives.
			if entries[i].executable != entries[j].executable {
				return entries[i].executable
			}
			return entries[i].used.Before(entries[j].used)
		})
		remaining := len(entries)
		removeAtLeast := 0
		if more {
			removeAtLeast = len(entries) / 2
		}
		for i, entry := range entries {
			if !entry.invalid && i >= removeAtLeast && size <= maxBytes && remaining <= maxEntries {
				break
			}
			if err := root.RemoveAll(entry.name); err != nil {
				return err
			}
			size -= entry.bytes
			remaining--
		}
		if !more {
			return nil
		}
	}
}
