package execmode

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

func recursiveDirectoryPage(ctx context.Context, root *os.Root, dir string, cursor int) ([]string, bool, error) {
	var names []string
	seen := 0
	window := func() int {
		return maxListEntries + 1 + min(max(cursor-seen, 0), maxDirectoryPrefix-maxListEntries-1)
	}
	err := walkDirectory(ctx, root, dir, "", window, func(path string, entry directoryName) error {
		if seen < cursor {
			seen++
			return nil
		}
		if len(names) == maxListEntries {
			return fs.SkipAll
		}
		if entry.mode.IsDir() {
			path += "/"
		}
		names = append(names, path)
		return nil
	})
	if err == fs.SkipAll {
		return names, true, nil
	}
	return names, false, err
}

// Bound each directory's pending inventory while preserving depth-first,
// lexical ordering. Glob filtering happens before selection, so unrelated
// files do not occupy the window. Directories are always traversed unless
// excluded by the shared managed-directory policy.
func walkDirectory(ctx context.Context, root *os.Root, dir, glob string, window func() int, visit func(string, directoryName) error) error {
	after := ""
	for {
		keep := window()
		entries, _, err := selectDirectoryNames(ctx, root, dir, after, keep, false, glob)
		if err != nil {
			return err
		}
		for i := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			entry := entries[i]
			entries[i] = directoryName{}
			after = entry.name
			if entry.mode.IsDir() && skipDirName(entry.name) {
				continue
			}
			path := filepath.Join(dir, entry.name)
			if err := visit(path, entry); err != nil {
				return err
			}
			if entry.mode.IsDir() {
				if err := walkDirectory(ctx, root, path, glob, window, visit); err != nil {
					return err
				}
			}
		}
		if len(entries) < keep {
			return nil
		}
	}
}
