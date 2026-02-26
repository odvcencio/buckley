package tool

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestHookRegistryOrderAndCopy(t *testing.T) {
	registry := &HookRegistry{}
	var preOrder []string
	var postOrder []string

	preGlobal := func(ctx *ExecutionContext) HookResult {
		preOrder = append(preOrder, "global")
		return HookResult{}
	}
	preTool := func(ctx *ExecutionContext) HookResult {
		preOrder = append(preOrder, "tool")
		return HookResult{}
	}
	postGlobal := func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		postOrder = append(postOrder, "global")
		return result, err
	}
	postTool := func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		postOrder = append(postOrder, "tool")
		return result, err
	}

	registry.RegisterPreHook("*", preGlobal)
	registry.RegisterPreHook("write_file", preTool)
	registry.RegisterPostHook("*", postGlobal)
	registry.RegisterPostHook("write_file", postTool)

	for _, hook := range registry.PreHooks("write_file") {
		if hook == nil {
			t.Fatal("unexpected nil pre-hook")
		}
		hook(&ExecutionContext{})
	}
	if got, want := preOrder, []string{"global", "tool"}; !sameStringSlice(got, want) {
		t.Fatalf("pre-order = %#v, want %#v", got, want)
	}

	for _, hook := range registry.PostHooks("write_file") {
		if hook == nil {
			t.Fatal("unexpected nil post-hook")
		}
		_, _ = hook(&ExecutionContext{}, &builtin.Result{Success: true}, nil)
	}
	if got, want := postOrder, []string{"global", "tool"}; !sameStringSlice(got, want) {
		t.Fatalf("post-order = %#v, want %#v", got, want)
	}

	// Ensure slices are copies.
	preHooks := registry.PreHooks("write_file")
	preHooks[0] = nil
	for _, hook := range registry.PreHooks("write_file") {
		if hook == nil {
			t.Fatal("expected pre-hooks to be returned as a copy")
		}
	}
}

func TestHookRegistryUnregisterHook(t *testing.T) {
	registry := &HookRegistry{}
	var order []string

	preOne := func(ctx *ExecutionContext) HookResult {
		order = append(order, "one")
		return HookResult{}
	}
	preTwo := func(ctx *ExecutionContext) HookResult {
		order = append(order, "two")
		return HookResult{}
	}
	postOne := func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		order = append(order, "post-one")
		return result, err
	}
	postTwo := func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		order = append(order, "post-two")
		return result, err
	}

	registry.RegisterPreHook("read_file", preOne)
	registry.RegisterPreHook("read_file", preTwo)
	registry.RegisterPostHook("read_file", postOne)
	registry.RegisterPostHook("read_file", postTwo)

	registry.UnregisterHook("read_file", preOne)
	registry.UnregisterHook("read_file", postOne)

	for _, hook := range registry.PreHooks("read_file") {
		hook(&ExecutionContext{})
	}
	for _, hook := range registry.PostHooks("read_file") {
		_, _ = hook(&ExecutionContext{}, &builtin.Result{Success: true}, nil)
	}

	if got, want := order, []string{"two", "post-two"}; !sameStringSlice(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
}

func sameStringSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
