package builtin

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
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

func (w *workDirAware) readMutationFile(path string) ([]byte, error) {
	limit := w.mutationFileLimit()
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, mutationBudgetError(limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := &io.LimitedReader{R: file, N: limit}
	// Leave room for Buffer's EOF probe without growing a completed snapshot.
	extraCapacity := min(int64(bytes.MinRead), int64(math.MaxInt)-info.Size())
	buffer := bytes.NewBuffer(make([]byte, 0, int(info.Size()+extraCapacity)))
	_, err = buffer.ReadFrom(reader)
	if err != nil {
		return nil, err
	}
	// The file may grow after Stat. Peek beyond the bounded snapshot without
	// adding one to the policy limit, which can be MaxInt64.
	if reader.N == 0 {
		var extra [1]byte
		if n, err := file.Read(extra[:]); n > 0 {
			return nil, mutationBudgetError(limit)
		} else if err != nil && err != io.EOF {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func (w *workDirAware) checkReplacementGrowth(size, oldBytes, newBytes, count int) error {
	limit := w.mutationFileLimit()
	growth := int64(newBytes) - int64(oldBytes)
	if int64(size) > limit || (growth > 0 && int64(count) > (limit-int64(size))/growth) {
		return mutationBudgetError(limit)
	}
	return nil
}
