package utils

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileLines safely reads a file line by line, handling errors gracefully.
// Returns an error only if the file cannot be opened, not for individual read errors.
func ReadFileLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, nil
}

// ReadFile safely reads an entire file.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return data, nil
}

// FileExists checks if a file exists and is accessible.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// WalkGoFiles walks a directory tree and invokes the callback for each Go file.
// Skips hidden directories and vendor directories.
func WalkGoFiles(root string, callback func(path string, info os.FileInfo) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files/directories we can't access
		}

		if info.IsDir() {
			// Skip hidden directories and vendor
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only process Go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		return callback(path, info)
	})
}
