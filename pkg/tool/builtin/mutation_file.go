package builtin

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// DefaultMutationFileBytes bounds whole-file mutations when no file policy is set.
const DefaultMutationFileBytes = 8 << 20

func (w *workDirAware) mutationFileLimit() int64 {
	if w.maxFileSizeBytes > 0 {
		return min(w.maxFileSizeBytes, int64(math.MaxInt))
	}
	return DefaultMutationFileBytes
}

func mutationBudgetError(limit int64) error {
	return fmt.Errorf("file too large to edit: input or replacement exceeds %d bytes", limit)
}

func mutationLineOffset(content string, line int) int {
	offset := 0
	for i := 1; i < line; i++ {
		end := strings.IndexByte(content[offset:], '\n')
		if end < 0 {
			return len(content)
		}
		offset += end + 1
	}
	return offset
}

func writeMutationFile(path, content string, perm os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (w *workDirAware) readMutationFile(path string) (string, error) {
	limit := w.mutationFileLimit()
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return "", mutationBudgetError(limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	content, exceeded, err := readSnapshotString(file, info.Size(), limit)
	if exceeded {
		return "", mutationBudgetError(limit)
	}
	return content, err
}

func (w *workDirAware) checkReplacementGrowth(size, oldBytes, newBytes, count int) error {
	limit := w.mutationFileLimit()
	growth := int64(newBytes) - int64(oldBytes)
	if int64(size) > limit || (growth > 0 && int64(count) > (limit-int64(size))/growth) {
		return mutationBudgetError(limit)
	}
	return nil
}
