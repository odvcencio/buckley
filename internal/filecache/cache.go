// Package filecache stores bounded, disposable cache files in a shared directory.
package filecache

import (
	"container/heap"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/internal/filelock"
)

var ErrBusy = errors.New("cache writer is busy")
var ErrEntryTooLarge = errors.New("cache entry exceeds byte budget")

type Limits struct {
	Bytes      int64
	Entries    int
	EntryBytes int64
}

type Cache struct {
	dir, prefix       string
	limits            Limits
	mu                sync.Mutex
	entries           map[string]cacheEntry
	bytes             int64
	generation        [16]byte
	indexed           bool
	directoryModified time.Time
}

func New(dir, prefix string, limits Limits) *Cache {
	cache := &Cache{dir: dir, prefix: prefix, limits: limits}
	if dir == "" {
		return cache
	}
	_ = os.MkdirAll(dir, 0755)
	if lock, err := cache.lock(); err == nil {
		if err := cache.prepare(); err == nil {
			_ = cache.finishIndex()
		}
		filelock.Unlock(lock)
		_ = lock.Close()
	}
	return cache
}

func validKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c != '-' && c != '_' && !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func (c *Cache) owns(name string) bool {
	return strings.HasPrefix(name, c.prefix) && strings.HasSuffix(name, ".json")
}

func (c *Cache) Get(key string) ([]byte, bool) {
	if c.dir == "" || !validKey(key) {
		return nil, false
	}
	path := filepath.Join(c.dir, c.prefix+key+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > c.limits.EntryBytes {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > c.limits.EntryBytes {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, c.limits.EntryBytes+1))
	if err != nil || int64(len(data)) > c.limits.EntryBytes {
		return nil, false
	}
	return data, true
}

func (c *Cache) lock() (*os.File, error) {
	path := filepath.Join(c.dir, "."+c.prefix+"lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("cache lock is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	locked, err := filelock.TryLock(file)
	if err != nil || !locked {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrBusy
	}
	return file, nil
}

// Set evicts the oldest writes first. Contention skips this optional cache write.
// Space for the single temporary file is reserved before it is created.
func (c *Cache) Set(key string, data []byte) error {
	if c.dir == "" {
		return nil
	}
	if !validKey(key) {
		return errors.New("invalid cache key")
	}
	if int64(len(data)) > c.limits.EntryBytes || int64(len(data)) > c.limits.Bytes {
		return ErrEntryTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lock, err := c.lock()
	if errors.Is(err, ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	defer filelock.Unlock(lock)
	if err := c.prepare(); err != nil {
		return err
	}
	if err := c.evict(int64(len(data)), 1); err != nil {
		return err
	}
	file, err := os.CreateTemp(c.dir, "."+c.prefix+"tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	name := c.prefix + key + ".json"
	if err := os.Rename(file.Name(), filepath.Join(c.dir, name)); err != nil {
		return err
	}
	c.bytes -= c.entries[name].bytes
	c.entries[name] = cacheEntry{name: name, bytes: int64(len(data)), modified: info.ModTime()}
	c.bytes += int64(len(data))
	return c.finishIndex()
}

func (c *Cache) Clear() error {
	if c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lock, err := c.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	defer filelock.Unlock(lock)
	if err := c.prepare(); err != nil {
		return err
	}
	err = c.walk(func(entry os.DirEntry) error {
		if !entry.IsDir() && (c.owns(entry.Name()) || strings.HasPrefix(entry.Name(), "."+c.prefix+"tmp-")) {
			return os.Remove(filepath.Join(c.dir, entry.Name()))
		}
		return nil
	})
	if err == nil {
		c.entries = make(map[string]cacheEntry)
		c.bytes = 0
		return c.finishIndex()
	}
	return err
}

// The generation is changed under the writer lock before modifying entries.
// Peers and crashed writers invalidate an index without rescanning on every Set.
func (c *Cache) prepare() error {
	info, err := os.Stat(c.dir)
	if err != nil {
		return err
	}
	var previous [16]byte
	path := filepath.Join(c.dir, "."+c.prefix+"generation")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("cache generation is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.Open(path)
	valid := false
	if err == nil {
		_, err = io.ReadFull(file, previous[:])
		_ = file.Close()
		valid = err == nil
	}
	rebuild := !c.indexed || !valid || previous != c.generation || !info.ModTime().Equal(c.directoryModified)
	c.indexed = false
	if _, err := rand.Read(c.generation[:]); err != nil {
		return err
	}
	if err := os.WriteFile(path, c.generation[:], 0600); err != nil {
		return err
	}
	if rebuild {
		return c.rebuild()
	}
	return nil
}

func (c *Cache) finishIndex() error {
	info, err := os.Stat(c.dir)
	if err != nil {
		return err
	}
	c.directoryModified = info.ModTime()
	c.indexed = true
	return nil
}

func (c *Cache) evict(reservedBytes int64, reservedEntries int) error {
	for len(c.entries)+reservedEntries > c.limits.Entries || c.bytes+reservedBytes > c.limits.Bytes {
		var oldest cacheEntry
		for _, entry := range c.entries {
			if oldest.name == "" || entry.modified.Before(oldest.modified) || entry.modified.Equal(oldest.modified) && entry.name < oldest.name {
				oldest = entry
			}
		}
		if err := os.Remove(filepath.Join(c.dir, oldest.name)); err != nil {
			return err
		}
		delete(c.entries, oldest.name)
		c.bytes -= oldest.bytes
	}
	return nil
}

func (c *Cache) walk(visit func(os.DirEntry) error) error {
	dir, err := os.Open(c.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(128)
		for _, entry := range entries {
			if e := visit(entry); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type cacheEntry struct {
	name     string
	bytes    int64
	modified time.Time
}
type oldestEntries []cacheEntry

func (h oldestEntries) Len() int { return len(h) }
func (h oldestEntries) Less(i, j int) bool {
	if h[i].modified.Equal(h[j].modified) {
		return h[i].name < h[j].name
	}
	return h[i].modified.Before(h[j].modified)
}
func (h oldestEntries) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *oldestEntries) Push(value any) { *h = append(*h, value.(cacheEntry)) }
func (h *oldestEntries) Pop() any {
	old := *h
	last := old[len(old)-1]
	old[len(old)-1] = cacheEntry{}
	*h = old[:len(old)-1]
	return last
}

func (c *Cache) rebuild() error {
	var retained oldestEntries
	var bytes int64
	err := c.walk(func(entry os.DirEntry) error {
		path := filepath.Join(c.dir, entry.Name())
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "."+c.prefix+"tmp-") {
			return os.Remove(path)
		}
		if !c.owns(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > c.limits.EntryBytes {
			return os.Remove(path)
		}
		bytes += info.Size()
		heap.Push(&retained, cacheEntry{name: entry.Name(), bytes: info.Size(), modified: info.ModTime()})
		for retained.Len() > c.limits.Entries || bytes > c.limits.Bytes {
			old := heap.Pop(&retained).(cacheEntry)
			if err := os.Remove(filepath.Join(c.dir, old.name)); err != nil {
				return err
			}
			bytes -= old.bytes
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.entries = make(map[string]cacheEntry, len(retained))
	for _, entry := range retained {
		c.entries[entry.name] = entry
	}
	c.bytes = bytes
	return nil
}
