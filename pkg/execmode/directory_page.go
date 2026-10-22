package execmode

import (
	"container/heap"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// Numeric cursors normally stay within the program's operation budget.
// Larger direct cursors use bounded rank selection instead of a larger heap.
const maxDirectoryPrefix = maxListEntries * DefaultCapabilityCallLimit

var errDirectoryChanged = errors.New("directory changed during listing; retry from the first page")

type directoryName struct {
	name      string
	directory bool
}

type directoryHeap struct {
	entries []directoryName
	largest bool
}

func (h directoryHeap) Len() int { return len(h.entries) }
func (h directoryHeap) Less(i, j int) bool {
	if h.largest {
		return h.entries[i].name < h.entries[j].name
	}
	return h.entries[i].name > h.entries[j].name
}
func (h directoryHeap) Swap(i, j int) { h.entries[i], h.entries[j] = h.entries[j], h.entries[i] }
func (h *directoryHeap) Push(value any) {
	h.entries = append(h.entries, value.(directoryName))
}
func (h *directoryHeap) Pop() any {
	last := h.entries[len(h.entries)-1]
	h.entries[len(h.entries)-1] = directoryName{}
	h.entries = h.entries[:len(h.entries)-1]
	return last
}

func scanDirectory(ctx context.Context, root *os.Root, path string, visit func(os.DirEntry)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			visit(entry)
		}
		if err == io.EOF {
			return nil
		}
	}
}

func selectDirectoryNames(ctx context.Context, root *os.Root, path, after string, keep int, largest bool) ([]directoryName, int, error) {
	h := &directoryHeap{largest: largest}
	total := 0
	err := scanDirectory(ctx, root, path, func(entry os.DirEntry) {
		total++
		name := entry.Name()
		if keep == 0 || name <= after {
			return
		}
		if h.Len() < keep {
			heap.Push(h, directoryName{name, entry.IsDir()})
			return
		}
		preferred := name < h.entries[0].name
		if largest {
			preferred = name > h.entries[0].name
		}
		if preferred {
			h.entries[0] = directoryName{name, entry.IsDir()}
			heap.Fix(h, 0)
		}
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(h.entries, func(i, j int) bool { return h.entries[i].name < h.entries[j].name })
	return h.entries, total, nil
}

// Select a byte-lexicographic rank with fixed bucket storage. This slow path
// supports arbitrary legacy offsets without retaining every preceding name.
func directoryNameAtRank(ctx context.Context, root *os.Root, path string, rank int) (string, error) {
	prefix := ""
	for {
		var counts [257]int
		err := scanDirectory(ctx, root, path, func(entry os.DirEntry) {
			name := entry.Name()
			if !strings.HasPrefix(name, prefix) {
				return
			}
			bucket := 0
			if len(name) > len(prefix) {
				bucket = int(name[len(prefix)]) + 1
			}
			counts[bucket]++
		})
		if err != nil {
			return "", err
		}
		bucket := -1
		for i, count := range counts {
			if rank < count {
				bucket = i
				break
			}
			rank -= count
		}
		if bucket < 0 {
			return "", errDirectoryChanged
		}
		if bucket == 0 {
			return prefix, nil
		}
		prefix += string([]byte{byte(bucket - 1)})
	}
}

func flatDirectoryPage(ctx context.Context, root *os.Root, path string, cursor int) ([]directoryName, int, error) {
	keep := 0
	if cursor <= maxDirectoryPrefix {
		keep = cursor + maxListEntries + 1
	}
	entries, total, err := selectDirectoryNames(ctx, root, path, "", keep, false)
	if err != nil || cursor >= total {
		return nil, total, err
	}
	if keep > 0 {
		return entries[cursor:min(cursor+maxListEntries, len(entries))], total, nil
	}
	remaining := total - cursor
	if remaining <= maxDirectoryPrefix {
		entries, count, err := selectDirectoryNames(ctx, root, path, "", remaining, true)
		if err != nil {
			return nil, 0, err
		}
		if count != total {
			return nil, 0, errDirectoryChanged
		}
		return entries[:min(maxListEntries, len(entries))], total, nil
	}
	after, err := directoryNameAtRank(ctx, root, path, cursor-1)
	if err != nil {
		return nil, 0, err
	}
	entries, count, err := selectDirectoryNames(ctx, root, path, after, maxListEntries+1, false)
	if err != nil {
		return nil, 0, err
	}
	if count != total || len(entries) == 0 {
		return nil, 0, errDirectoryChanged
	}
	return entries[:min(maxListEntries, len(entries))], total, nil
}
