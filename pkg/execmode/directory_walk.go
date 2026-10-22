package execmode

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// Keep each directory's pending inventory bounded while preserving WalkDir's
// depth-first, lexical ordering. Large offsets continue after the last name
// instead of retaining the whole directory or creating a persistent index.
func recursiveDirectoryPage(ctx context.Context, root *os.Root, dir string, cursor int) ([]string, bool, error) {
	var names []string
	seen := 0
	var walk func(string) error
	walk = func(dir string) error {
		after := ""
		for {
			keep := maxListEntries + 1 + min(max(cursor-seen, 0), maxDirectoryPrefix-maxListEntries-1)
			entries, _, err := selectDirectoryNames(ctx, root, dir, after, keep, false)
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
				if entry.directory && skipDirName(entry.name) {
					continue
				}
				path := filepath.Join(dir, entry.name)
				if seen < cursor {
					seen++
				} else {
					if len(names) == maxListEntries {
						return fs.SkipAll
					}
					name := path
					if entry.directory {
						name += "/"
					}
					names = append(names, name)
				}
				if entry.directory {
					if err := walk(path); err != nil {
						return err
					}
				}
			}
			if len(entries) < keep {
				return nil
			}
		}
	}
	err := walk(dir)
	if err == fs.SkipAll {
		return names, true, nil
	}
	return names, false, err
}
