package shadowgit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const reusablePackMinBytes = 1 << 20
const maxReusablePacks = 64
const maxReusablePackObjects = 64

type reusablePack struct {
	name    string
	objects []string
	bytes   int64
}

// smallPackObjects reads bounded index tables without loading blob contents.
// Unsupported or larger indexes continue through ordinary repacking.
func smallPackObjects(root *os.Root, name string) ([]string, error) {
	pack, err := root.Open("objects/pack/" + name)
	if err != nil {
		return nil, err
	}
	var header [13]byte
	_, err = pack.ReadAt(header[:], 0)
	_ = pack.Close()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	version := binary.BigEndian.Uint32(header[4:8])
	kind := header[12] >> 4 & 7
	if string(header[:4]) != "PACK" || version != 2 && version != 3 || (binary.BigEndian.Uint32(header[8:12]) == 0 || binary.BigEndian.Uint32(header[8:12]) > maxReusablePackObjects) || kind < 1 || kind > 4 {
		return nil, nil
	}
	index, err := root.Open("objects/pack/" + strings.TrimSuffix(name, ".pack") + ".idx")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer index.Close()
	var prefix [8]byte
	if _, err := index.ReadAt(prefix[:], 0); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	// V1 interleaves offsets and names; V2 has a header followed by the
	// fanout table and contiguous names. Both use the repository hash width.
	countOffset, objectOffset := int64(255*4), int64(256*4)
	width := (len(name) - len("pack-") - len(".pack")) / 2
	stride, offset := width+4, 4
	if binary.BigEndian.Uint32(prefix[:4]) == 0xff744f63 {
		if binary.BigEndian.Uint32(prefix[4:]) != 2 {
			return nil, nil
		}
		countOffset, objectOffset = 8+255*4, 8+256*4
		stride, offset = width, 0
	}
	var count [4]byte
	if _, err := index.ReadAt(count[:], countOffset); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if binary.BigEndian.Uint32(count[:]) != binary.BigEndian.Uint32(header[8:12]) {
		return nil, nil
	}
	ids := make([]byte, int(binary.BigEndian.Uint32(count[:]))*stride)
	if _, err := index.ReadAt(ids, objectOffset); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	objects := make([]string, len(ids)/stride)
	for i := range objects {
		objects[i] = hex.EncodeToString(ids[i*stride+offset : i*stride+offset+width])
	}
	return objects, nil
}

// reusablePacks keeps only wholly reachable packs. Fixed candidate/object caps
// bound memory and command-line size; every other object is repacked normally.
func (s *Store) reusablePacks(ctx context.Context, roots []string, all bool) (map[string]bool, error) {
	root, err := os.OpenRoot(s.gitDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open("objects/pack")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	candidates := make([]reusablePack, 0, maxReusablePacks)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".pack") || !ownedCacheEntry("objects/pack/"+entry.Name(), s.ref, false) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || allocatedBytes(info) < reusablePackMinBytes {
				continue
			}
			objects, err := smallPackObjects(root, entry.Name())
			if err != nil {
				return nil, fmt.Errorf("shadowgit: inspect reusable pack: %w", err)
			}
			if len(objects) == 0 {
				continue
			}
			candidate := reusablePack{entry.Name(), objects, allocatedBytes(info)}
			if len(candidates) < maxReusablePacks {
				candidates = append(candidates, candidate)
			} else {
				smallest := 0
				for i := range candidates {
					if candidates[i].bytes < candidates[smallest].bytes {
						smallest = i
					}
				}
				if candidate.bytes > candidates[smallest].bytes {
					candidates[smallest] = candidate
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	byObject := make(map[string][]int)
	for i, candidate := range candidates {
		for _, object := range candidate.objects {
			byObject[object] = append(byObject[object], i)
		}
	}
	args := []string{"rev-list", "--objects", "--no-object-names", "--stdin"}
	if all {
		args = append(args, "--all")
	}
	cmd := s.command(ctx, []string{"GIT_COMMON_DIR=" + s.gitDir}, args...)
	cmd.Stdin = strings.NewReader(strings.Join(roots, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	hits := make([]int, len(candidates))
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		for _, i := range byObject[scanner.Text()] {
			hits[i]++
		}
	}
	_ = stdout.Close()
	if err := errors.Join(scanner.Err(), cmd.Wait()); err != nil {
		return nil, fmt.Errorf("shadowgit: find reusable packs: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	kept := make(map[string]bool, len(candidates))
	used := make(map[string]bool)
	for i, candidate := range candidates {
		if hits[i] != len(candidate.objects) {
			continue
		}
		overlap := false
		for _, object := range candidate.objects {
			overlap = overlap || used[object]
		}
		if overlap {
			continue
		}
		kept[candidate.name] = true
		for _, object := range candidate.objects {
			used[object] = true
		}
	}
	return kept, nil
}
