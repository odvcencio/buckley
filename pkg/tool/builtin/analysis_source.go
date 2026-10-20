package builtin

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DefaultAnalysisEntries = 10000

// sourceScan enforces one aggregate input and traversal budget for analysis tools.
type sourceScan struct {
	ctx            context.Context
	remaining      int64
	entries, files int
}

func (a *sourceScan) scan(path string, depth int, include func(string, bool) (bool, error), visit func(string, string) error) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if depth > 64 {
		return fmt.Errorf("directory depth exceeds 64; narrow the path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: analysis requires a regular file", path)
		}
		selected, err := include(path, depth == 0)
		if err != nil {
			return err
		}
		if !selected {
			return nil
		}
		source, _, exceeded, err := readFileSnapshot(path, a.remaining)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if exceeded {
			return fmt.Errorf("source byte budget exceeded at %s; narrow the path", path)
		}
		a.remaining -= int64(len(source))
		a.files++
		return visit(path, source)
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(directoryReadBatch)
		for _, entry := range entries {
			a.entries++
			if a.entries > DefaultAnalysisEntries {
				return fmt.Errorf("traversal exceeds %d entries; narrow the path", DefaultAnalysisEntries)
			}
			if err := a.ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__":
					continue
				}
			}
			if err := a.scan(filepath.Join(path, entry.Name()), depth+1, include, visit); err != nil {
				return err
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
