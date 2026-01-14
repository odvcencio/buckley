package tool

import (
	"reflect"
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// HookResult describes how a pre-hook wants to adjust execution.
type HookResult struct {
	Abort          bool
	ModifiedParams map[string]any
	AbortReason    string
	AbortResult    *builtin.Result
}

// PreHook runs before a tool executes.
type PreHook func(ctx *ExecutionContext) HookResult

// PostHook runs after a tool executes.
type PostHook func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error)

// HookRegistry stores pre/post hooks per tool name.
type HookRegistry struct {
	mu        sync.RWMutex
	preHooks  map[string][]PreHook
	postHooks map[string][]PostHook
}

// RegisterPreHook registers a pre-hook for a tool name or "*".
func (h *HookRegistry) RegisterPreHook(toolName string, hook PreHook) {
	if h == nil || hook == nil {
		return
	}
	name := normalizeHookName(toolName)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.preHooks == nil {
		h.preHooks = map[string][]PreHook{}
	}
	h.preHooks[name] = append(h.preHooks[name], hook)
}

// RegisterPostHook registers a post-hook for a tool name or "*".
func (h *HookRegistry) RegisterPostHook(toolName string, hook PostHook) {
	if h == nil || hook == nil {
		return
	}
	name := normalizeHookName(toolName)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.postHooks == nil {
		h.postHooks = map[string][]PostHook{}
	}
	h.postHooks[name] = append(h.postHooks[name], hook)
}

// PreHooks returns hooks in order: "*" then tool-specific.
func (h *HookRegistry) PreHooks(toolName string) []PreHook {
	if h == nil {
		return nil
	}
	name := strings.TrimSpace(toolName)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.preHooks) == 0 {
		return nil
	}
	var merged []PreHook
	if hooks, ok := h.preHooks["*"]; ok {
		merged = append(merged, hooks...)
	}
	if name != "" {
		if hooks, ok := h.preHooks[name]; ok {
			merged = append(merged, hooks...)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := make([]PreHook, len(merged))
	copy(out, merged)
	return out
}

// PostHooks returns hooks in order: "*" then tool-specific.
func (h *HookRegistry) PostHooks(toolName string) []PostHook {
	if h == nil {
		return nil
	}
	name := strings.TrimSpace(toolName)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.postHooks) == 0 {
		return nil
	}
	var merged []PostHook
	if hooks, ok := h.postHooks["*"]; ok {
		merged = append(merged, hooks...)
	}
	if name != "" {
		if hooks, ok := h.postHooks[name]; ok {
			merged = append(merged, hooks...)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := make([]PostHook, len(merged))
	copy(out, merged)
	return out
}

// UnregisterHook removes a previously registered hook for a tool name or "*".
func (h *HookRegistry) UnregisterHook(toolName string, hook any) {
	if h == nil || hook == nil {
		return
	}
	name := normalizeHookName(toolName)
	h.mu.Lock()
	defer h.mu.Unlock()

	switch typed := hook.(type) {
	case PreHook:
		if len(h.preHooks) == 0 {
			return
		}
		h.preHooks[name] = removePreHook(h.preHooks[name], typed)
		if len(h.preHooks[name]) == 0 {
			delete(h.preHooks, name)
		}
	case func(*ExecutionContext) HookResult:
		if len(h.preHooks) == 0 {
			return
		}
		typedHook := PreHook(typed)
		h.preHooks[name] = removePreHook(h.preHooks[name], typedHook)
		if len(h.preHooks[name]) == 0 {
			delete(h.preHooks, name)
		}
	case PostHook:
		if len(h.postHooks) == 0 {
			return
		}
		h.postHooks[name] = removePostHook(h.postHooks[name], typed)
		if len(h.postHooks[name]) == 0 {
			delete(h.postHooks, name)
		}
	case func(*ExecutionContext, *builtin.Result, error) (*builtin.Result, error):
		if len(h.postHooks) == 0 {
			return
		}
		typedHook := PostHook(typed)
		h.postHooks[name] = removePostHook(h.postHooks[name], typedHook)
		if len(h.postHooks[name]) == 0 {
			delete(h.postHooks, name)
		}
	}
}

func normalizeHookName(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return "*"
	}
	return name
}

func removePreHook(hooks []PreHook, target PreHook) []PreHook {
	if len(hooks) == 0 {
		return hooks
	}
	out := hooks[:0]
	for _, hook := range hooks {
		if !sameHook(hook, target) {
			out = append(out, hook)
		}
	}
	return out
}

func removePostHook(hooks []PostHook, target PostHook) []PostHook {
	if len(hooks) == 0 {
		return hooks
	}
	out := hooks[:0]
	for _, hook := range hooks {
		if !sameHook(hook, target) {
			out = append(out, hook)
		}
	}
	return out
}

func sameHook(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
