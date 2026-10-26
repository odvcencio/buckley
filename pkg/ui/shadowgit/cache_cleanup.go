package shadowgit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

func cacheHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return value != ""
}

func ownedCacheEntry(name, ref string, dir bool) bool {
	if dir {
		switch name {
		case ".", "objects", "objects/info", "objects/pack", "refs", "refs/heads", "refs/tags", "info":
			return true
		}
		if strings.HasPrefix(ref, name+"/") {
			return true
		}
		return strings.HasPrefix(name, "objects/") && len(strings.TrimPrefix(name, "objects/")) == 2 && cacheHex(strings.TrimPrefix(name, "objects/"))
	}
	switch name {
	case cacheOwnerFile, "HEAD", "HEAD.lock", "config", "config.lock", "description", "info/exclude":
		return true
	}
	if name == ref || name == ref+".lock" {
		return true
	}
	if strings.HasPrefix(name, "index-") && !strings.Contains(name, "/") {
		nonce := strings.TrimSuffix(strings.TrimPrefix(name, "index-"), ".lock")
		if nonce == "" || len(nonce) > 10 {
			return false
		}
		for _, c := range nonce {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[0] != "objects" {
		return false
	}
	if parts[1] == "pack" {
		if base, ok := strings.CutPrefix(parts[2], "pack-"); ok {
			id, ext, _ := strings.Cut(base, ".")
			if (len(id) == 40 || len(id) == 64) && cacheHex(id) {
				switch ext {
				case "pack", "idx", "rev", "bitmap", "keep", "promisor":
					return true
				}
			}
		}
		return false
	}
	if len(parts[1]) != 2 || !cacheHex(parts[1]) {
		return false
	}
	if (len(parts[2]) == 38 || len(parts[2]) == 62) && cacheHex(parts[2]) {
		return true
	}
	if suffix, ok := strings.CutPrefix(parts[2], "tmp_obj_"); ok && len(suffix) > 0 && len(suffix) <= 32 {
		for _, c := range suffix {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				return false
			}
		}
		return true
	}
	return false
}

// walkCache validates before deleting, uses bounded directory batches, and never
// follows links. Only known private Git artifacts and indexes are eligible.
func walkCache(root *os.Root, ref, dir string, remove bool) error {
	file, err := root.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		entries, err := file.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			name := path.Join(dir, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() && !entry.Type().IsRegular() || !ownedCacheEntry(name, ref, entry.IsDir()) {
				return fmt.Errorf("shadowgit: preserving unexpected private entry %q", name)
			}
			if entry.IsDir() {
				if err := walkCache(root, ref, name, remove); err != nil {
					return err
				}
			}
			if remove && name != cacheOwnerFile {
				if err := root.Remove(name); err != nil {
					return err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func removeOwnedStorage(parent *os.Root, name, ref string, lease *cacheLease) error {
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("shadowgit: cache is not a directory")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, actual) {
		return fmt.Errorf("shadowgit: cache directory changed during cleanup")
	}
	if lease != nil {
		owner, err := root.Lstat(cacheOwnerFile)
		if err != nil {
			return err
		}
		locked, err := lease.file.Stat()
		if err != nil {
			return err
		}
		if !owner.Mode().IsRegular() || !os.SameFile(owner, locked) {
			return fmt.Errorf("shadowgit: cache lease changed during cleanup")
		}
	} else if _, err := root.Lstat(cacheOwnerFile); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shadowgit: preserving unexpected ownership marker")
	}
	if err := walkCache(root, ref, ".", false); err != nil {
		return err
	}
	if err := walkCache(root, ref, ".", true); err != nil {
		return err
	}
	// Parent sweep coordination protects the close/unlink window on Windows.
	// Keep the marker until every other known artifact was removed so an
	// interrupted cleanup can be retried even without a valid bare repository.
	if lease != nil {
		lease.close()
		if err := root.Remove(cacheOwnerFile); err != nil {
			return err
		}
	}
	return parent.Remove(name)
}
