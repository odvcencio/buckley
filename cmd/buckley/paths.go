package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	envBuckleyDBPath          = "BUCKLEY_DB_PATH"
	envBuckleyDataDir         = "BUCKLEY_DATA_DIR"
	envBuckleyACPEventsDBPath = "BUCKLEY_ACP_EVENTS_DB_PATH"
	envBuckleyCoordEventsDBPath = "BUCKLEY_COORD_EVENTS_DB_PATH"
	envBuckleyRemoteAuthPath  = "BUCKLEY_REMOTE_AUTH_PATH"
)

func resolveDBPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envBuckleyDBPath)); path != "" {
		return expandHomePath(path)
	}

	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		dir, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "buckley.db"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".buckley", "buckley.db"), nil
}

func resolveACPEventsDBPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envBuckleyACPEventsDBPath)); path != "" {
		return expandHomePath(path)
	}

	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		dir, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "buckley-acp-events.db"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".buckley", "buckley-acp-events.db"), nil
}

func resolveCoordinationEventsDBPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envBuckleyCoordEventsDBPath)); path != "" {
		return expandHomePath(path)
	}
	return resolveACPEventsDBPath()
}

func expandHomePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}

	return path, nil
}
