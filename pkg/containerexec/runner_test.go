package containerexec

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"go.uber.org/mock/gomock"
)

func TestCanRunOnHost(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"read_file can run on host", "read_file", true},
		{"list_directory can run on host", "list_directory", true},
		{"git_status can run on host", "git_status", true},
		{"go_test cannot run on host", "go_test", false},
		{"npm_run cannot run on host", "npm_run", false},
		{"write_file cannot run on host", "write_file", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanRunOnHost(tt.toolName)
			if got != tt.want {
				t.Errorf("CanRunOnHost(%s) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestGetServiceForTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     string
	}{
		{"go_test maps to dev-go", "go_test", "dev-go"},
		{"go_build maps to dev-go", "go_build", "dev-go"},
		{"npm_test maps to dev-node", "npm_test", "dev-node"},
		{"npm_run maps to dev-node", "npm_run", "dev-node"},
		{"cargo_test maps to dev-rust", "cargo_test", "dev-rust"},
		{"unknown tool defaults to dev-go", "unknown_tool", "dev-go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetServiceForTool(tt.toolName)
			if got != tt.want {
				t.Errorf("GetServiceForTool(%s) = %s, want %s", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestMapPaths(t *testing.T) {
	cr := &ContainerRunner{
		workDir: "/home/user/project",
	}

	tests := []struct {
		name   string
		result *builtin.Result
		want   map[string]any
	}{
		{
			name: "output path mapping",
			result: &builtin.Result{
				Success: true,
				Data: map[string]any{
					"output": "/workspace/src/main.go:10",
				},
			},
			want: map[string]any{
				"output": "/home/user/project/src/main.go:10",
			},
		},
		{
			name: "file path mapping",
			result: &builtin.Result{
				Success: true,
				Data: map[string]any{
					"path": "/workspace/README.md",
				},
			},
			want: map[string]any{
				"path": "/home/user/project/README.md",
			},
		},
		{
			name: "no mapping needed",
			result: &builtin.Result{
				Success: true,
				Data: map[string]any{
					"count": 5,
				},
			},
			want: map[string]any{
				"count": 5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr.mapPaths(tt.result)

			for key, wantVal := range tt.want {
				gotVal, ok := tt.result.Data[key]
				if !ok {
					t.Errorf("Key %s not found in result.Data", key)
					continue
				}

				if gotVal != wantVal {
					t.Errorf("result.Data[%s] = %v, want %v", key, gotVal, wantVal)
				}
			}
		})
	}
}

func TestContainerRunner_WrapsToolInterface(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()
	mockTool.EXPECT().Description().Return("A test tool").AnyTimes()
	mockTool.EXPECT().Parameters().Return(builtin.ParameterSchema{Type: "object"}).AnyTimes()

	cr := NewContainerRunner("/path/to/compose.yml", "dev-go", "/workspace", mockTool)

	if cr.Name() != "test_tool" {
		t.Errorf("Name() = %s, want test_tool", cr.Name())
	}

	if cr.Description() != "A test tool" {
		t.Errorf("Description() = %s, want 'A test tool'", cr.Description())
	}

	params := cr.Parameters()
	if params.Type != "object" {
		t.Errorf("Parameters().Type = %s, want object", params.Type)
	}
}

func TestContainerRunner_ExecuteReadOnlyTool(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("read_file").AnyTimes()
	mockTool.EXPECT().Execute(gomock.Any()).Return(&builtin.Result{
		Success: true,
		Data:    map[string]any{"executed": true},
	}, nil)

	cr := NewContainerRunner("/path/to/compose.yml", "dev-go", "/workspace", mockTool)

	// read_file should run on host (not in container)
	result, err := cr.Execute(map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Error("Expected successful execution for read-only tool")
	}

	if executed, ok := result.Data["executed"].(bool); !ok || !executed {
		t.Error("Expected tool to be executed on host")
	}
}
