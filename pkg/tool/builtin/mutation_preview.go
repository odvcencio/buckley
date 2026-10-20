package builtin

import (
	"crypto/sha256"
	"fmt"
	"os"

	"m31labs.dev/buckley/internal/textdiff"
)

// Preview returns the validated effect without writing or running diagnostics.
func (t *EditFileTool) Preview(params map[string]any) (*Result, error) {
	copy := *t
	copy.previewMutation = true
	return copy.Execute(params)
}

func (t *WriteFileTool) Preview(params map[string]any) (*Result, error) {
	copy := *t
	copy.previewMutation = true
	return copy.Execute(params)
}

func (t *InsertTextTool) Preview(params map[string]any) (*Result, error) {
	copy := *t
	copy.previewMutation = true
	return copy.Execute(params)
}

func (t *DeleteLinesTool) Preview(params map[string]any) (*Result, error) {
	copy := *t
	copy.previewMutation = true
	return copy.Execute(params)
}

func (t *SearchReplaceTool) Preview(params map[string]any) (*Result, error) {
	copy := *t
	copy.previewMutation = true
	return copy.Execute(params)
}

func (w *workDirAware) mutationPreview(path, before, after string, exists bool) (*Result, error) {
	// Capture only compact source evidence while approval is parked.
	digest := mutationDigest(before)
	target := evalSymlinksFallbackForTarget(path)
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return &Result{Error: fmt.Sprintf("failed to preview file: %v", err)}, nil
	}
	if (err == nil) != exists {
		return &Result{Error: "file changed while preparing approval; retry"}, nil
	}
	// A dangling final symlink is not a new file.
	if !exists {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return &Result{Error: "cannot preview unresolved file target"}, nil
		}
	}
	return &Result{
		Success: true, NeedsApproval: true,
		DiffPreview: &DiffInfo{FilePath: path, IsNew: !exists, UnifiedDiff: textdiff.Unified(path, before, after)},
		ValidateApproval: func() error {
			resolved, err := w.ResolvePath(path)
			if err != nil {
				return err
			}
			if resolved != path || evalSymlinksFallbackForTarget(path) != target {
				return fmt.Errorf("file target changed since approval; retry")
			}
			current, err := os.Stat(path)
			if !exists {
				if !os.IsNotExist(err) {
					return fmt.Errorf("file appeared since approval; retry")
				}
				if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
					return fmt.Errorf("file target changed since approval; retry")
				}
				return nil
			}
			if err != nil {
				return fmt.Errorf("approved file unavailable: %w", err)
			}
			if !os.SameFile(info, current) {
				return fmt.Errorf("file replaced since approval; retry")
			}
			content, err := w.readMutationFile(path)
			if err != nil {
				return fmt.Errorf("recheck approved file: %w", err)
			}
			if mutationDigest(content) != digest {
				return fmt.Errorf("file changed since approval; retry")
			}
			return nil
		},
	}, nil
}

func mutationDigest(content string) [sha256.Size]byte {
	hash := sha256.New()
	scratch := make([]byte, min(len(content), 4096))
	for len(content) > 0 {
		n := copy(scratch, content)
		hash.Write(scratch[:n])
		content = content[n:]
	}
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest
}
