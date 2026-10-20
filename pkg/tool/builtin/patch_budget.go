package builtin

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

type patchFileState struct {
	size         int64
	exists       bool
	unterminated bool
}

type patchCandidate struct {
	name, path string
	state      patchFileState
	newDirs    int
}

func (t *PatchFileTool) checkPatchFileBudget(files []unifiedPatchFile, strip int, env []string) error {
	limit := t.maxFileSizeBytes
	if limit <= 0 {
		return nil
	}
	posix := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "POSIXLY_CORRECT=") {
			posix = true
		}
	}
	states := make(map[string]patchFileState)
	createdDirs := make(map[string]bool)
	for _, file := range files {
		if !file.hasHunk {
			continue
		}
		names := []string{file.oldPath, file.newPath}
		if posix && file.indexPath != "" {
			names = append(names, file.indexPath)
		}
		candidates := make([]patchCandidate, 0, len(names))
		for _, name := range names {
			if name == "/dev/null" {
				continue
			}
			name, ok := patchPathAfterStrip(name, strip)
			if !ok || name != pathpkg.Clean(name) {
				return fmt.Errorf("file size policy requires canonical relative patch paths")
			}
			path, err := resolvePath(t.workDir, filepath.FromSlash(name))
			if err != nil {
				return fmt.Errorf("resolve patch target: %w", err)
			}
			state, ok := states[path]
			if !ok {
				info, err := os.Lstat(path)
				if err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("inspect patch target: %w", err)
				}
				if err == nil {
					if !info.Mode().IsRegular() {
						return fmt.Errorf("file size policy requires regular patch targets: %s", name)
					}
					state = patchFileState{size: info.Size(), exists: true}
				}
				if state.size > 0 {
					input, err := os.Open(path)
					if err != nil {
						return fmt.Errorf("open patch target: %w", err)
					}
					var last [1]byte
					_, readErr := input.ReadAt(last[:], state.size-1)
					closeErr := input.Close()
					if readErr != nil {
						return fmt.Errorf("read patch input ending: %w", readErr)
					}
					if closeErr != nil {
						return fmt.Errorf("close patch input: %w", closeErr)
					}
					state.unterminated = last[0] != '\n'
				}
				states[path] = state
			}
			candidate := patchCandidate{name: name, path: path, state: state}
			if !state.exists && file.creates {
				for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
					if createdDirs[dir] {
						break
					}
					info, err := os.Stat(dir)
					if err == nil && info.IsDir() {
						break
					}
					if err == nil {
						return fmt.Errorf("patch parent is not a directory: %s", dir)
					}
					if !os.IsNotExist(err) {
						return fmt.Errorf("inspect patch parent directory: %w", err)
					}
					candidate.newDirs++
				}
			}
			candidates = append(candidates, candidate)
		}

		// GNU patch prefers existing names, then the fewest new directories
		// for creation. POSIX mode takes the first existing header instead.
		selected := -1
		for i, candidate := range candidates {
			if !candidate.state.exists && (!file.creates || posix) {
				continue
			}
			if selected < 0 || betterPatchCandidate(candidate, candidates[selected]) {
				selected = i
			}
			if posix && candidate.state.exists {
				selected = i
				break
			}
		}
		if selected < 0 {
			return fmt.Errorf("cannot determine bounded patch input; use an existing file or a unified file creation patch")
		}
		target := candidates[selected]
		if target.state.size > limit {
			return mutationBudgetError(limit)
		}
		remaining := target.state.size - file.removedBytes
		if remaining < 0 {
			return fmt.Errorf("patch removes more bytes than its input contains")
		}
		// An added line without LF may need a separator before retained input.
		// Credit its missing byte only when it provably ends the output.
		added := file.addedBytes
		if file.lastNewAddedUnterminated && (remaining == 0 || file.removesUnterminated) {
			added--
		}
		if added > limit-remaining {
			return mutationBudgetError(limit)
		}
		size := remaining + added
		unterminated := target.state.unterminated && !file.removesUnterminated
		// GNU patch can supply a separator when appending after a retained
		// unterminated input line, including context ignored through fuzz.
		// Reserve that byte rather than reading or staging the whole input.
		if unterminated && file.addedBytes > 0 {
			if size == limit {
				return fmt.Errorf("file too large to patch safely: leave one byte of headroom for an unterminated input line (max %d bytes)", limit)
			}
			size++
		}
		states[target.path] = patchFileState{size: size, exists: posix || file.newPath != "/dev/null" || size != 0, unterminated: size > 0 && (unterminated || file.addsUnterminated)}
		dir := filepath.Dir(target.path)
		for range target.newDirs {
			createdDirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}
	return nil
}

func betterPatchCandidate(a, b patchCandidate) bool {
	if a.state.exists != b.state.exists {
		return a.state.exists
	}
	if !a.state.exists && a.newDirs != b.newDirs {
		return a.newDirs < b.newDirs
	}
	if aDirs, bDirs := strings.Count(a.name, "/"), strings.Count(b.name, "/"); aDirs != bDirs {
		return aDirs < bDirs
	}
	if aBase, bBase := len(pathpkg.Base(a.name)), len(pathpkg.Base(b.name)); aBase != bBase {
		return aBase < bBase
	}
	return len(a.name) < len(b.name)
}
