package shadowgit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// retentionPack stages replacement objects outside Git's searchable pack
// directory and reuses complete packs needed by the proposed retention root.
type retentionPack struct {
	store *Store
	dir   string
	ids   map[string]bool
	kept  map[string]bool
}

func (s *Store) hasPacks() (bool, error) {
	dir, err := os.Open(filepath.Join(s.gitDir, "objects", "pack"))
	if err != nil {
		return false, err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".pack") {
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
	}
}

func (s *Store) preparePack(ctx context.Context, roots []string, all bool) (_ *retentionPack, err error) {
	kept, err := s.reusablePacks(ctx, roots, all)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Join(s.gitDir, "objects", "pack"), "tmp_pack_")
	if err != nil {
		return nil, fmt.Errorf("shadowgit: stage retained pack: %w", err)
	}
	pack := &retentionPack{store: s, dir: dir, kept: kept}
	defer func() {
		if err != nil {
			err = errors.Join(err, pack.close())
		}
	}()
	// Split replacement packs so large blobs can outlive changing retention
	// metadata without being copied again. Keep Git's bounded streaming path.
	args := []string{"-c", "pack.writeReverseIndex=false", "pack-objects", "--revs", "--non-empty", "--quiet", "--window=0", "--threads=1", "--max-pack-size=1m", "--index-version=2", "--no-write-bitmap-index"}
	if all {
		args = append(args, "--all")
	}
	for name := range kept {
		args = append(args, "--keep-pack="+name)
	}
	args = append(args, filepath.Join(dir, "pack"))
	out, err := s.runBytes(ctx, []string{"GIT_COMMON_DIR=" + s.gitDir}, strings.NewReader(strings.Join(roots, "\n")+"\n"), args...)
	if err != nil {
		return nil, fmt.Errorf("shadowgit: pack retained objects: %w", err)
	}
	pack.ids = make(map[string]bool)
	for _, id := range strings.Fields(string(out)) {
		if (len(id) != 40 && len(id) != 64) || !cacheHex(id) {
			return nil, fmt.Errorf("shadowgit: invalid retained pack ID %q", id)
		}
		pack.ids[id] = true
	}
	return pack, nil
}

func (p *retentionPack) bytes() (int64, error) {
	var total int64
	for id := range p.ids {
		for _, ext := range []string{".pack", ".idx"} {
			name := "pack-" + id + ext
			info, err := os.Lstat(filepath.Join(p.dir, name))
			if err != nil {
				return 0, err
			}
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("shadowgit: retained pack is not a regular file")
			}
			used := allocatedBytes(info)
			existing, err := os.Lstat(filepath.Join(p.store.gitDir, "objects", "pack", name))
			if err == nil {
				if !existing.Mode().IsRegular() {
					return 0, fmt.Errorf("shadowgit: existing retained pack is not a regular file")
				}
				used = max(used, allocatedBytes(existing))
			} else if !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
			total += used
		}
	}
	for name := range p.kept {
		base := strings.TrimSuffix(name, ".pack")
		for _, ext := range []string{".pack", ".idx"} {
			info, err := os.Lstat(filepath.Join(p.store.gitDir, "objects", "pack", base+ext))
			if err != nil {
				return 0, err
			}
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("shadowgit: reusable pack is not a regular file")
			}
			total += allocatedBytes(info)
		}
	}
	return total, nil
}

func (p *retentionPack) close() error {
	root, err := os.OpenRoot(p.store.gitDir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Join("objects", "pack", filepath.Base(p.dir))
	if err := walkCache(root, p.store.ref, filepath.ToSlash(name), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // Native prune can remove the empty staging directory.
		}
		return err
	}
	if err := walkCache(root, p.store.ref, filepath.ToSlash(name), true); err != nil {
		return err
	}
	return root.Remove(name)
}

// collectPack installs new objects before removing packs outside the retained set.
// The caller holds the store lock and includes every published/pending root.
func (s *Store) collectPack(ctx context.Context, pack *retentionPack, extra ...string) error {
	root, err := os.OpenRoot(s.gitDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := walkCache(root, s.ref, "objects/pack", false); err != nil {
		return err
	}
	for id := range pack.ids {
		for _, ext := range []string{".pack", ".idx"} {
			name := "pack-" + id + ext
			target := filepath.Join("objects", "pack", name)
			if _, err := root.Lstat(target); err == nil {
				// Content-addressed packs are immutable. Windows also refuses
				// replacing the read-only files Git has already installed.
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := root.Rename(filepath.Join("objects", "pack", filepath.Base(pack.dir), name), target); err != nil {
				return fmt.Errorf("shadowgit: install retained pack: %w", err)
			}
		}
	}
	dir, err := root.Open("objects/pack")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			base, ext, _ := strings.Cut(name, ".")
			if entry.IsDir() || !strings.HasPrefix(name, "pack-") || (ext == "pack" || ext == "idx") && (pack.ids[strings.TrimPrefix(base, "pack-")] || pack.kept[base+".pack"]) {
				continue
			}
			if err := root.Remove(filepath.Join("objects", "pack", entry.Name())); err != nil {
				return fmt.Errorf("shadowgit: remove expired pack: %w", err)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return s.prune(ctx, extra...)
}
