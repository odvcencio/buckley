package config

import (
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

const maxLaunchOperatorConfigBytes = 1 << 20

// LoadLaunchOperatorConfig loads launch admission policy from defaults, a
// bounded stable operator file, and the ordinary process environment. It
// never reads workspace project configuration or config.env.
func LoadLaunchOperatorConfig(workspaceRoot string) (*LaunchOperatorConfig, error) {
	current, err := user.Current()
	if err != nil {
		return nil, errors.New("launch operator account is unavailable")
	}
	return loadLaunchOperatorConfigForUser(workspaceRoot, current)
}

func loadLaunchOperatorConfigForUser(workspaceRoot string, current *user.User) (*LaunchOperatorConfig, error) {
	workspace, err := canonicalLaunchConfigPath(workspaceRoot)
	if err != nil {
		return nil, errors.New("launch operator workspace is invalid")
	}
	if current == nil || current.HomeDir == "" {
		return nil, errors.New("launch operator account is unavailable")
	}
	uid, uidErr := strconv.ParseUint(current.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(current.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return nil, errors.New("launch operator account is unavailable")
	}
	home, err := canonicalLaunchConfigPath(current.HomeDir)
	if err != nil {
		return nil, errors.New("launch operator home is unavailable")
	}
	operatorDir := filepath.Join(home, ".buckley")
	if pathContains(workspace, operatorDir) {
		return nil, errors.New("launch operator source overlaps workspace")
	}

	cfg := DefaultConfig()
	data, found, err := readLaunchOperatorConfig(home)
	if err != nil {
		return nil, errors.New("launch operator config is unavailable")
	}
	if found {
		if err := mergeConfigData(cfg, data, "launch operator config", false); err != nil {
			return nil, errors.New("launch operator config is invalid")
		}
	}
	applyEnvOverrides(cfg, nil)
	artifactDir, err := validateLaunchArtifactDirectory(cfg.Launch.WorkerImage.ArtifactDir, workspace, operatorDir, uid)
	if err != nil {
		return nil, errors.New("launch operator artifact source is unavailable")
	}
	cfg.Launch.WorkerImage.ArtifactDir = artifactDir
	return &LaunchOperatorConfig{WorkerImage: cfg.Launch.WorkerImage, Diagnostics: cfg.Diagnostics}, nil
}

func canonicalLaunchConfigPath(raw string) (string, error) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !hasParentPrefix(rel)
}

func hasParentPrefix(path string) bool {
	return len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}

func readLaunchOperatorConfig(home string) ([]byte, bool, error) {
	return readLaunchOperatorConfigWithHook(home, nil)
}

func readLaunchOperatorConfigWithHook(home string, afterReadHook func()) ([]byte, bool, error) {
	dir, file, dirBefore, before, found, err := openLaunchOperatorSource(home)
	if err != nil || !found {
		return nil, found, err
	}
	defer dir.Close()
	if !dirBefore.IsDir() || !before.Mode().IsRegular() || launchOperatorFileHasMultipleLinks(before) || before.Size() < 0 || before.Size() > maxLaunchOperatorConfigBytes {
		_ = file.Close()
		return nil, false, errors.New("operator config file is unsafe")
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, maxLaunchOperatorConfigBytes+1))
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	if afterReadHook != nil {
		afterReadHook()
	}
	after, lstatErr := os.Lstat(filepath.Join(home, ".buckley", "config.yaml"))
	dirAfter, dirErr := os.Lstat(filepath.Join(home, ".buckley"))
	dirCloseErr := dir.Close()
	if statErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil || dirErr != nil || dirCloseErr != nil || len(data) > maxLaunchOperatorConfigBytes {
		return nil, false, errors.New("operator config stable read failed")
	}
	if !os.SameFile(dirBefore, dirAfter) || dirAfter.Mode()&os.ModeSymlink != 0 || !dirAfter.IsDir() {
		return nil, false, errors.New("operator config directory changed")
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || launchOperatorFileHasMultipleLinks(current) || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
			return nil, false, errors.New("operator config changed during read")
		}
	}
	return data, true, nil
}
