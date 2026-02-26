package tool

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestValidationMiddleware_AllowsValid(t *testing.T) {
	called := false
	mw := Validation(ValidationConfig{
		Rules: []ValidationRule{
			{Tool: "echo", Param: "path", Validate: ValidateNonEmpty()},
		},
	}, nil)

	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	res, err := exec(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "echo",
		Params:   map[string]any{"path": "ok.txt"},
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("expected success result, got %#v", res)
	}
	if !called {
		t.Fatal("expected base executor to be called")
	}
}

func TestValidationMiddleware_BlocksInvalid(t *testing.T) {
	called := false
	var gotTool, gotParam, gotMsg string
	mw := Validation(ValidationConfig{
		Rules: []ValidationRule{
			{Tool: "write_file", Param: "path", Validate: ValidateNonEmpty()},
		},
	}, func(tool, param, msg string) {
		gotTool = tool
		gotParam = param
		gotMsg = msg
	})

	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	res, err := exec(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "write_file",
		Params:   map[string]any{"path": ""},
		Metadata: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("expected base executor to be skipped")
	}
	if res == nil || res.Success {
		t.Fatalf("expected failure result, got %#v", res)
	}
	if gotTool != "write_file" || gotParam != "path" || gotMsg == "" {
		t.Fatalf("unexpected callback values: tool=%q param=%q msg=%q", gotTool, gotParam, gotMsg)
	}
}

func TestValidateNonEmpty(t *testing.T) {
	validator := ValidateNonEmpty()
	if err := validator(""); err == nil {
		t.Fatal("expected error for empty string")
	}
	if err := validator("ok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validator([]string{}); err == nil {
		t.Fatal("expected error for empty slice")
	}
}

func TestValidatePath(t *testing.T) {
	baseDir := t.TempDir()
	validator := ValidatePath(baseDir)

	tests := []struct {
		name    string
		value   any
		wantErr bool
	}{
		{name: "relative", value: "file.txt", wantErr: false},
		{name: "absolute within", value: filepath.Join(baseDir, "nested", "file.txt"), wantErr: false},
		{name: "relative escape", value: "../outside.txt", wantErr: true},
		{name: "absolute outside", value: filepath.Dir(baseDir), wantErr: true},
		{name: "empty", value: "", wantErr: true},
		{name: "non-string", value: 42, wantErr: true},
	}

	for _, tt := range tests {
		err := validator(tt.value)
		if tt.wantErr && err == nil {
			t.Fatalf("%s: expected error", tt.name)
		}
		if !tt.wantErr && err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.name, err)
		}
	}
}
