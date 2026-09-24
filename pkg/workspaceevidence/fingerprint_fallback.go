package workspaceevidence

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fingerprintTimeout scales with the Git index entry count. Worktrees have a
// gitdir file instead of a .git directory. Missing indexes use the base limit.
func fingerprintTimeout(root string) time.Duration {
	gitDir := filepath.Join(root, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.Mode().IsRegular() {
		if data, err := os.ReadFile(gitDir); err == nil && strings.HasPrefix(string(data), "gitdir: ") {
			gitDir = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(root, gitDir)
			}
		}
	}
	index, err := os.Open(filepath.Join(gitDir, "index"))
	if err != nil {
		return defaultFingerprintTimeout
	}
	defer index.Close()
	var header [12]byte
	if _, err := io.ReadFull(index, header[:]); err != nil || string(header[:4]) != "DIRC" {
		return defaultFingerprintTimeout
	}
	entries := binary.BigEndian.Uint32(header[8:])
	return min(5*time.Minute, defaultFingerprintTimeout+time.Duration(entries/1000)*time.Second)
}

type StateFingerprint struct {
	Digest  string
	Warning string
}

// GitStateFingerprintWithFallback is for best-effort execution observation.
// Admission and review gates can keep using the strict fingerprint API.
func GitStateFingerprintWithFallback(ctx context.Context, root string) (StateFingerprint, error) {
	digest, primaryErr := GitStateFingerprint(ctx, root)
	if primaryErr == nil {
		return StateFingerprint{Digest: digest}, nil
	}
	state := StateFingerprint{Warning: primaryErr.Error()}
	if ctx.Err() != nil {
		return state, primaryErr
	}
	fallbackCtx, cancel := context.WithTimeout(ctx, fingerprintTimeout(root))
	defer cancel()
	digest, err := gitStatusFingerprint(fallbackCtx, root)
	if err != nil {
		return state, errors.Join(primaryErr, fmt.Errorf("status fallback: %w", err))
	}
	state.Digest = digest
	state.Warning += "; used git status and changed-file hashes"
	return state, nil
}

func gitStatusFingerprint(ctx context.Context, root string) (string, error) {
	top, err := gitOutput(ctx, root, maxFingerprintPathBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(string(top))
	fs, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer fs.Close()
	first, err := gitStatusManifest(ctx, root)
	if err != nil {
		return "", err
	}
	digest, err := hashGitManifest(ctx, fs, first)
	if err != nil {
		return "", err
	}
	second, err := gitStatusManifest(ctx, root)
	if err != nil {
		return "", err
	}
	again, err := hashGitManifest(ctx, fs, second)
	if err != nil {
		return "", err
	}
	final, err := gitStatusManifest(ctx, root)
	if err != nil {
		return "", err
	}
	if !first.equal(second) || !second.equal(final) || digest != again {
		return "", fmt.Errorf("workspace changed during status observation")
	}
	return "status:" + digest, nil
}

func gitStatusManifest(ctx context.Context, root string) (gitManifest, error) {
	head, unborn, err := gitFingerprintHead(ctx, root)
	if err != nil {
		return gitManifest{}, err
	}
	raw, err := gitOutput(ctx, root, maxFingerprintPathBytes, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return gitManifest{}, err
	}
	index, err := gitOutput(ctx, root, maxFingerprintPathBytes, "ls-files", "--stage", "-z")
	if err != nil {
		return gitManifest{}, err
	}
	manifest := gitManifest{head: head, unbornHeadRef: unborn, index: append(raw, index...)}
	for _, entry := range splitNULPaths(raw) {
		if len(entry) < 4 || entry[2] != ' ' {
			return gitManifest{}, fmt.Errorf("invalid porcelain status entry")
		}
		if entry[:2] == "??" {
			manifest.untrackedPaths = append(manifest.untrackedPaths, entry[3:])
		} else {
			manifest.trackedPaths = append(manifest.trackedPaths, entry[3:])
		}
	}
	manifest.trackedPaths = sortedUniquePaths(manifest.trackedPaths)
	manifest.untrackedPaths = sortedUniquePaths(manifest.untrackedPaths)
	return manifest, nil
}
