package execmode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// OutlineFunc supplies a bounded live outline for a jailed workspace-relative file.
// The provider must honor cancellation and preserve structural coverage warnings.
type OutlineFunc func(context.Context, string, int, string) (map[string]any, error)

// WithOutlineProvider enables structural navigation without coupling the runner
// to a parser or CLI. The broker still enforces paths, grants and audit records.
func WithOutlineProvider(outline OutlineFunc) RunnerOption {
	return func(r *Runner) { r.outline = outline }
}

func (b *Broker) codeOutline(ctx context.Context, params map[string]any) (any, error) {
	path, ok := params["path"].(string)
	if !ok || len(path) == 0 || len(path) > 4096 {
		return nil, fmt.Errorf("code.outline path must be a workspace-relative file path of at most 4096 bytes")
	}
	full, err := b.jail(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("code.outline requires a regular source file")
	}
	limit := 40
	if raw, present := params["limit"]; present {
		n, ok := raw.(float64)
		if !ok || n != math.Trunc(n) || n < 0 || n > 100 {
			return nil, fmt.Errorf("code.outline limit must be an integer from 0 to 100 (0 selects 40)")
		}
		if n > 0 {
			limit = int(n)
		}
	}
	cursor := ""
	if raw, present := params["cursor"]; present {
		var ok bool
		cursor, ok = raw.(string)
		if !ok || len(cursor) > 128 {
			return nil, fmt.Errorf("code.outline cursor must be the returned string of at most 128 bytes")
		}
	}
	if b.outline == nil {
		return nil, fmt.Errorf("code.outline is unavailable; use SearchTextGlob and ReadFile instead")
	}
	rel, err := filepath.Rel(b.root, full)
	if err != nil {
		return nil, err
	}
	// A program can fan out calls, but only one parser process runs at a time.
	select {
	case b.outlineSlot <- struct{}{}:
		defer func() { <-b.outlineSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, err := b.outline(ctx, rel, limit, cursor)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode code.outline: %w", err)
	}
	if len(encoded) > 16<<10 {
		return nil, fmt.Errorf("code.outline exceeded the 16 KiB response budget; reduce limit")
	}
	return json.RawMessage(encoded), nil
}
