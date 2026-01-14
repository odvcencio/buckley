package tool

import (
	"encoding/json"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ResultSizeLimit truncates oversized tool results.
func ResultSizeLimit(maxBytes int, suffix string) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			res, err := next(ctx)
			if res == nil || maxBytes <= 0 {
				return res, err
			}
			if hasNativeTruncation(res) {
				return res, err
			}
			if sizeFits(res, maxBytes) {
				return res, err
			}

			setTruncationMetadata(ctx)
			truncateResultStrings(res, maxBytes, suffix)
			if sizeFits(res, maxBytes) {
				return res, err
			}

			res.DisplayData = map[string]any{
				"message": strings.TrimSpace("output truncated" + suffix),
			}
			res.Data = map[string]any{
				"truncated": true,
			}
			setTruncationMetadata(ctx)
			return res, err
		}
	}
}

func sizeFits(res *builtin.Result, maxBytes int) bool {
	if res == nil {
		return true
	}
	data, err := json.Marshal(res)
	if err != nil {
		return false
	}
	return len(data) <= maxBytes
}

func truncateResultStrings(res *builtin.Result, maxBytes int, suffix string) {
	if res == nil {
		return
	}
	target := maxBytes / 2
	if target <= 0 {
		target = maxBytes
	}
	res.Data = truncateMapStrings(res.Data, target, suffix)
	res.DisplayData = truncateMapStrings(res.DisplayData, target, suffix)
	if res.Error != "" && len(res.Error) > target {
		res.Error = truncateString(res.Error, target, suffix)
	}
}

func truncateMapStrings(data map[string]any, max int, suffix string) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, val := range data {
		switch v := val.(type) {
		case string:
			out[key] = truncateString(v, max, suffix)
		default:
			out[key] = val
		}
	}
	return out
}

func truncateString(value string, max int, suffix string) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= len(suffix) {
		return value[:max]
	}
	return value[:max-len(suffix)] + suffix
}

func hasNativeTruncation(res *builtin.Result) bool {
	if res == nil {
		return false
	}
	if res.ShouldAbridge {
		return true
	}
	return hasTruncationFlag(res.Data) || hasTruncationFlag(res.DisplayData)
}

func hasTruncationFlag(data map[string]any) bool {
	if data == nil {
		return false
	}
	for key, value := range data {
		if !strings.Contains(strings.ToLower(key), "truncated") {
			continue
		}
		if truncated, ok := value.(bool); ok && truncated {
			return true
		}
	}
	return false
}

func setTruncationMetadata(ctx *ExecutionContext) {
	if ctx == nil {
		return
	}
	if ctx.Metadata == nil {
		ctx.Metadata = map[string]any{}
	}
	ctx.Metadata["result_truncated"] = true
}
