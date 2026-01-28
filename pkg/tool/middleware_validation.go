package tool

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// Validator checks a parameter value.
type Validator func(value any) error

// ValidationRule defines a validation rule for a tool parameter.
type ValidationRule struct {
	Tool     string
	Param    string
	Validate Validator
}

// ValidationConfig collects validation rules.
type ValidationConfig struct {
	Rules []ValidationRule
}

// Validation applies configured validation rules before executing tools.
func Validation(cfg ValidationConfig, onError func(tool, param, msg string)) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if ctx == nil || len(cfg.Rules) == 0 {
				return next(ctx)
			}
			toolName := strings.TrimSpace(ctx.ToolName)
			params := ctx.Params
			for _, rule := range cfg.Rules {
				if rule.Validate == nil {
					continue
				}
				if !validationRuleApplies(rule.Tool, toolName) {
					continue
				}
				param := strings.TrimSpace(rule.Param)
				if param == "" || params == nil {
					continue
				}
				value, ok := params[param]
				if !ok {
					continue
				}
				if err := rule.Validate(value); err != nil {
					msg := strings.TrimSpace(err.Error())
					if msg == "" {
						msg = "validation failed"
					}
					if onError != nil {
						onError(toolName, param, msg)
					}
					if ctx.Metadata == nil {
						ctx.Metadata = map[string]any{}
					}
					ctx.Metadata["validation_error"] = map[string]any{
						"tool":    toolName,
						"param":   param,
						"message": msg,
					}
					result := &builtin.Result{Success: false, Error: msg}
					return result, fmt.Errorf("validation failed: %s", msg)
				}
			}
			return next(ctx)
		}
	}
}

// ValidateNonEmpty ensures a parameter is non-empty.
func ValidateNonEmpty() Validator {
	return func(value any) error {
		switch v := value.(type) {
		case nil:
			return fmt.Errorf("value required")
		case string:
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("value required")
			}
		case []string:
			if len(v) == 0 {
				return fmt.Errorf("value required")
			}
		case []any:
			if len(v) == 0 {
				return fmt.Errorf("value required")
			}
		}
		return nil
	}
}

// ValidatePath ensures a path is non-empty and within baseDir (when provided).
func ValidatePath(baseDir string) Validator {
	base := strings.TrimSpace(baseDir)
	if base != "" {
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	return func(value any) error {
		raw, ok := value.(string)
		if !ok {
			return fmt.Errorf("path must be a string")
		}
		pathValue := strings.TrimSpace(raw)
		if pathValue == "" {
			return fmt.Errorf("path required")
		}
		if strings.Contains(pathValue, "\x00") {
			return fmt.Errorf("path contains null byte")
		}
		clean := filepath.Clean(pathValue)
		if base == "" {
			if strings.HasPrefix(clean, "..") {
				return fmt.Errorf("path escapes base directory")
			}
			return nil
		}
		abs := clean
		if !filepath.IsAbs(clean) {
			abs = filepath.Join(base, clean)
		}
		abs, err := filepath.Abs(abs)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}
		rel, err := filepath.Rel(base, abs)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}
		if strings.HasPrefix(rel, "..") {
			return fmt.Errorf("path outside base directory")
		}
		return nil
	}
}

func validationRuleApplies(ruleTool, toolName string) bool {
	ruleTool = strings.TrimSpace(ruleTool)
	if ruleTool == "" || ruleTool == "*" {
		return true
	}
	return strings.EqualFold(ruleTool, toolName)
}
