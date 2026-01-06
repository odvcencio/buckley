package tool

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"go.uber.org/mock/gomock"
)

func TestNewEmptyRegistry(t *testing.T) {
	r := NewEmptyRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if r.Count() != 0 {
		t.Errorf("expected empty registry, got %d tools", r.Count())
	}
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	// Should have built-in tools registered
	if r.Count() == 0 {
		t.Error("expected built-in tools to be registered")
	}
}

func TestNewRegistry_WithBuiltinFilter(t *testing.T) {
	// Filter to only include tools with "read" in the name
	filter := func(tool Tool) bool {
		return tool.Name() == "read_file"
	}

	r := NewRegistry(WithBuiltinFilter(filter))

	if r.Count() == 0 {
		t.Fatal("expected at least one tool")
	}

	// Should have read_file
	_, ok := r.Get("read_file")
	if !ok {
		t.Error("expected read_file tool to be registered")
	}

	// Should NOT have write_file
	_, ok = r.Get("write_file")
	if ok {
		t.Error("expected write_file tool to NOT be registered")
	}
}

func TestRegistry_Register_Get(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	r := NewEmptyRegistry()
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()

	r.Register(mockTool)

	tool, ok := r.Get("test_tool")
	if !ok {
		t.Fatal("expected to find registered tool")
	}
	if tool.Name() != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %s", tool.Name())
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewEmptyRegistry()

	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent tool")
	}
}

func TestRegistry_List(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	r := NewEmptyRegistry()

	mockTool1 := NewMockTool(ctrl)
	mockTool1.EXPECT().Name().Return("tool1").AnyTimes()
	mockTool2 := NewMockTool(ctrl)
	mockTool2.EXPECT().Name().Return("tool2").AnyTimes()

	r.Register(mockTool1)
	r.Register(mockTool2)

	tools := r.List()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestRegistry_Count(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	r := NewEmptyRegistry()
	if r.Count() != 0 {
		t.Errorf("expected count 0, got %d", r.Count())
	}

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test").AnyTimes()
	r.Register(mockTool)

	if r.Count() != 1 {
		t.Errorf("expected count 1, got %d", r.Count())
	}
}

func TestRegistry_Execute(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	r := NewEmptyRegistry()
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()
	mockTool.EXPECT().Execute(gomock.Any()).Return(&builtin.Result{
		Success: true,
		Data:    map[string]any{"output": "test result"},
	}, nil)

	r.Register(mockTool)

	result, err := r.Execute("test_tool", map[string]any{"param": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected successful result")
	}
	if result.Data["output"] != "test result" {
		t.Errorf("unexpected result data: %v", result.Data)
	}
}

func TestRegistry_Execute_NotFound(t *testing.T) {
	r := NewEmptyRegistry()

	_, err := r.Execute("nonexistent", nil)
	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

func TestRegistry_Execute_EmptyName(t *testing.T) {
	r := NewEmptyRegistry()

	_, err := r.Execute("", nil)
	if err == nil {
		t.Error("expected error for empty tool name")
	}
	if err.Error() != "tool name cannot be empty" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestRegistry_EnableContainers(t *testing.T) {
	r := NewEmptyRegistry()

	r.EnableContainers("/path/to/compose.yml", "/workdir")

	enabled, composePath, workDir := r.ContainerInfo()
	if !enabled {
		t.Error("expected containers to be enabled")
	}
	if composePath != "/path/to/compose.yml" {
		t.Errorf("expected compose path '/path/to/compose.yml', got %s", composePath)
	}
	if workDir != "/workdir" {
		t.Errorf("expected workDir '/workdir', got %s", workDir)
	}
}

func TestRegistry_DisableContainers(t *testing.T) {
	r := NewEmptyRegistry()

	r.EnableContainers("/path/to/compose.yml", "/workdir")
	r.DisableContainers()

	enabled, composePath, workDir := r.ContainerInfo()
	if enabled {
		t.Error("expected containers to be disabled")
	}
	if composePath != "" {
		t.Errorf("expected empty compose path, got %s", composePath)
	}
	if workDir != "" {
		t.Errorf("expected empty workDir, got %s", workDir)
	}
}

func TestRegistry_SetContainerContext(t *testing.T) {
	r := NewEmptyRegistry()

	r.SetContainerContext("/compose.yml", "/work")

	// Should set context but not enable execution
	enabled, composePath, workDir := r.ContainerInfo()
	if !enabled {
		t.Error("expected compose path to be set")
	}
	if composePath != "/compose.yml" {
		t.Errorf("expected compose path '/compose.yml', got %s", composePath)
	}
	if workDir != "/work" {
		t.Errorf("expected workDir '/work', got %s", workDir)
	}
}

func TestRegistry_EnableTelemetry(t *testing.T) {
	r := NewEmptyRegistry()
	hub := telemetry.NewHub()

	r.EnableTelemetry(hub, "session123")

	if r.telemetryHub == nil {
		t.Error("expected telemetry hub to be set")
	}
	if r.telemetrySession != "session123" {
		t.Errorf("expected session 'session123', got %s", r.telemetrySession)
	}
}

func TestRegistry_UpdateTelemetrySession(t *testing.T) {
	r := NewEmptyRegistry()
	hub := telemetry.NewHub()

	r.EnableTelemetry(hub, "session1")
	r.UpdateTelemetrySession("session2")

	if r.telemetrySession != "session2" {
		t.Errorf("expected session 'session2', got %s", r.telemetrySession)
	}
}

func TestRegistry_ToOpenAIFunctions(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	r := NewEmptyRegistry()
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()
	mockTool.EXPECT().Description().Return("Test tool").AnyTimes()
	mockTool.EXPECT().Parameters().Return(builtin.ParameterSchema{
		Type: "object",
	}).AnyTimes()

	r.Register(mockTool)

	functions := r.ToOpenAIFunctions()
	if len(functions) != 1 {
		t.Errorf("expected 1 function, got %d", len(functions))
	}
}

func TestSanitizeShellCommand(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{
			name:   "nil params",
			params: nil,
			want:   "",
		},
		{
			name:   "empty params",
			params: map[string]any{},
			want:   "",
		},
		{
			name:   "command present",
			params: map[string]any{"command": "ls -la"},
			want:   "ls -la",
		},
		{
			name:   "command with whitespace",
			params: map[string]any{"command": "  go test  "},
			want:   "go test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeShellCommand(tt.params)
			if got != tt.want {
				t.Errorf("sanitizeShellCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateForTelemetry(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short string",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "exact limit",
			input: string(make([]byte, 512)),
			want:  string(make([]byte, 512)),
		},
		{
			name:  "over limit",
			input: string(make([]byte, 600)),
			want:  string(make([]byte, 512)) + "...",
		},
		{
			name:  "whitespace trimmed",
			input: "  test  ",
			want:  "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForTelemetry(tt.input)
			if len(got) > 515 { // 512 + "..."
				t.Errorf("truncateForTelemetry() returned string too long: %d", len(got))
			}
			if tt.name != "over limit" && got != tt.want {
				t.Errorf("truncateForTelemetry() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegistry_ContainerInfo_EmptyWhenNotSet(t *testing.T) {
	r := NewEmptyRegistry()

	enabled, composePath, workDir := r.ContainerInfo()
	if enabled {
		t.Error("expected containers to not be enabled initially")
	}
	if composePath != "" {
		t.Error("expected empty compose path initially")
	}
	if workDir != "" {
		t.Error("expected empty workDir initially")
	}
}

func TestRegistry_BuiltinsIncludeExpectedTools(t *testing.T) {
	r := NewRegistry()

	expectedTools := []string{
		"read_file",
		"write_file",
		"list_directory",
		"git_status",
		"run_shell",
	}

	for _, name := range expectedTools {
		if _, ok := r.Get(name); !ok {
			t.Errorf("expected built-in tool %q to be registered", name)
		}
	}
}
