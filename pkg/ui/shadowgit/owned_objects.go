package shadowgit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// StorageDir is this handle's private Git directory. It contains no borrowed
// objects and can be inspected without changing the user's Git configuration.
func (s *Store) StorageDir() string {
	return s.gitDir
}

func (s *Store) initStorage(ctx context.Context) error {
	gitDir, err := s.run(ctx, nil, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return ErrNotAGitRepo
	}
	commonDir, err := s.run(ctx, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	workTree, err := s.run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	format, err := s.run(ctx, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	parent := filepath.Join(gitDir, "buckley", "undo")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("shadowgit: create storage parent: %w", err)
	}
	if err := reapAbandonedStorage(parent); err != nil {
		slog.WarnContext(ctx, "Could not collect abandoned undo caches", "error", err)
	}
	private, err := os.MkdirTemp(parent, "handle-")
	if err != nil {
		return fmt.Errorf("shadowgit: create storage: %w", err)
	}
	s.lease, err = newCacheLease(private, s.ref)
	if err != nil {
		return fmt.Errorf("shadowgit: lease storage: %w", err)
	}
	if _, err := s.run(ctx, nil, "-c", "core.hooksPath="+filepath.Join(private, "hooks"), "init", "--bare", "--template=", "--object-format="+format, "--quiet", private); err != nil {
		return fmt.Errorf("shadowgit: initialize storage: %w", err)
	}
	if err := os.WriteFile(filepath.Join(private, "HEAD"), []byte("ref: refs/worktree/buckley/unborn\n"), 0600); err != nil {
		return fmt.Errorf("shadowgit: initialize private HEAD: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(private, s.ref)), 0700); err != nil {
		return fmt.Errorf("shadowgit: initialize private ref directory: %w", err)
	}
	s.gitDir, s.commonDir, s.workTree = private, commonDir, workTree
	return nil
}

func (s *Store) environment(extra []string) []string {
	// Inherited repository selectors must not redirect private collection or
	// namespaced refs into another repository. Keep ordinary user configuration.
	env := make([]string, 0, len(os.Environ())+len(extra)+10)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_NAMESPACE", "GIT_QUARANTINE_PATH":
			continue
		}
		env = append(env, entry)
	}
	if s.gitDir != "" {
		env = append(env,
			"GIT_DIR="+s.gitDir,
			"GIT_COMMON_DIR="+s.commonDir,
			"GIT_WORK_TREE="+s.workTree,
			"GIT_OBJECT_DIRECTORY="+filepath.Join(s.gitDir, "objects"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
			"GIT_SHALLOW_FILE="+filepath.Join(s.gitDir, "shallow"),
			"GIT_GRAFT_FILE="+filepath.Join(s.gitDir, "grafts"),
			"GIT_NAMESPACE=",
		)
	}
	return append(env, extra...)
}

// collect holds the store lock, so no snapshot or restore can race collection.
// Capture shares user attributes/configuration; collection switches the common
// directory back to the owned bare repository, including its refs and shallow file.
func (s *Store) collect(ctx context.Context, extra ...string) (err error) {
	packed, err := s.hasPacks()
	if err != nil {
		return err
	}
	if !packed {
		return s.prune(ctx, extra...)
	}
	roots := append(append([]string(nil), s.pending...), extra...)
	pack, err := s.preparePack(ctx, roots, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pack.close()) }()
	return s.collectPack(ctx, pack, extra...)
}

func (s *Store) prune(ctx context.Context, extra ...string) error {
	args := []string{"prune", "--expire=now"}
	args = append(args, s.pending...)
	args = append(args, extra...)
	_, err := s.run(ctx, []string{"GIT_COMMON_DIR=" + s.gitDir, "GIT_INDEX_FILE=" + filepath.Join(s.gitDir, "index")}, args...)
	if err != nil {
		return fmt.Errorf("shadowgit: collect private objects: %w", err)
	}
	return nil
}
