package shadowgit

import (
	"context"
	"errors"
	"fmt"
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
	private, err := os.MkdirTemp(parent, "handle-")
	if err != nil {
		return fmt.Errorf("shadowgit: create storage: %w", err)
	}
	if _, err := s.run(ctx, nil, "init", "--bare", "--template=", "--object-format="+format, "--quiet", private); err != nil {
		return fmt.Errorf("shadowgit: initialize storage: %w", err)
	}
	if err := os.WriteFile(filepath.Join(private, "HEAD"), []byte("ref: refs/worktree/buckley/unborn\n"), 0600); err != nil {
		return fmt.Errorf("shadowgit: initialize private HEAD: %w", err)
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
func (s *Store) collect(ctx context.Context, extra ...string) error {
	args := []string{"prune", "--expire=now"}
	args = append(args, s.pending...)
	args = append(args, extra...)
	_, err := s.run(ctx, []string{"GIT_COMMON_DIR=" + s.gitDir, "GIT_INDEX_FILE=" + filepath.Join(s.gitDir, "index")}, args...)
	if err != nil {
		return fmt.Errorf("shadowgit: collect private objects: %w", err)
	}
	return nil
}

// removeStorage removes only known private metadata and empty directories.
// Unexpected files are preserved; the user's Git directory is never pruned here.
func (s *Store) removeStorage() error {
	entries, err := os.ReadDir(s.gitDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "objects", "refs", "info", "description", "HEAD", "config":
		default:
			return fmt.Errorf("shadowgit: preserving unexpected private file %q", entry.Name())
		}
	}
	remove := func(name string) error {
		err := os.Remove(filepath.Join(s.gitDir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for name := filepath.Dir(s.ref); name != "." && name != "refs"; name = filepath.Dir(name) {
		if err := remove(name); err != nil {
			return fmt.Errorf("shadowgit: remove private ref directory: %w", err)
		}
	}
	entries, err = os.ReadDir(filepath.Join(s.gitDir, "objects"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := remove(filepath.Join("objects", entry.Name())); err != nil {
			return fmt.Errorf("shadowgit: remove private object directory: %w", err)
		}
	}
	for _, name := range []string{"objects", "refs/heads", "refs/tags", "refs", "info/exclude", "info", "description", "HEAD", "config"} {
		if err := remove(name); err != nil {
			return fmt.Errorf("shadowgit: remove private metadata: %w", err)
		}
	}
	if err := os.Remove(s.gitDir); err != nil {
		return fmt.Errorf("shadowgit: remove storage: %w", err)
	}
	s.removed = true
	return nil
}
