package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"m31labs.dev/buckley/pkg/containers"
)

func TestResolveEnvValue(t *testing.T) {
	// Set test environment variable
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain string",
			input: "plain_value",
			want:  "plain_value",
		},
		{
			name:  "existing env var",
			input: "${TEST_VAR}",
			want:  "test_value",
		},
		{
			name:  "non-existent env var",
			input: "${NONEXISTENT}",
			want:  "",
		},
		{
			name:  "not an env var reference",
			input: "not${a}reference",
			want:  "not${a}reference",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEnvValue(tt.input)
			if got != tt.want {
				t.Errorf("resolveEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestManager_generateComposeFromSpec(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(cwd, "/worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath := t.TempDir()

	tests := []struct {
		name     string
		spec     *ContainerSpec
		wantErr  bool
		validate func(*testing.T, string)
	}{
		{
			name: "basic spec",
			spec: &ContainerSpec{
				BaseImage: "golang:1.22",
				Workdir:   "/app",
			},
			wantErr: false,
			validate: func(t *testing.T, content string) {
				if !strings.Contains(content, "golang:1.22") {
					t.Error("compose file should contain base image")
				}
				if !strings.Contains(content, "working_dir: /app") {
					t.Error("compose file should contain working_dir")
				}
			},
		},
		{
			name: "spec with container name",
			spec: &ContainerSpec{
				BaseImage: "alpine:latest",
				Name:      "test-container",
				Workdir:   "/workspace",
			},
			wantErr: false,
			validate: func(t *testing.T, content string) {
				if !strings.Contains(content, "container_name: test-container") {
					t.Error("compose file should contain container_name")
				}
			},
		},
		{
			name: "spec with mounts",
			spec: &ContainerSpec{
				BaseImage:      "alpine:latest",
				Workdir:        "/app",
				MountWorkspace: boolPtr(true),
				Mounts:         []string{"/host:/container", "/another:/path"},
			},
			wantErr: false,
			validate: func(t *testing.T, content string) {
				if !strings.Contains(content, "volumes:") {
					t.Error("compose file should contain volumes section")
				}
				if !strings.Contains(content, "/host:/container") {
					t.Error("compose file should contain custom mount")
				}
			},
		},
		{
			name: "spec with ports",
			spec: &ContainerSpec{
				BaseImage: "nginx:latest",
				Workdir:   "/app",
				Ports:     []string{"8080:80", "8443:443"},
			},
			wantErr: false,
			validate: func(t *testing.T, content string) {
				if !strings.Contains(content, "ports:") {
					t.Error("compose file should contain ports section")
				}
				if !strings.Contains(content, "8080:80") {
					t.Error("compose file should contain port mapping")
				}
			},
		},
		{
			name: "spec with environment",
			spec: &ContainerSpec{
				BaseImage: "alpine:latest",
				Workdir:   "/app",
				Env: map[string]string{
					"FOO": "bar",
					"BAZ": "qux",
				},
			},
			wantErr: false,
			validate: func(t *testing.T, content string) {
				if !strings.Contains(content, "environment:") {
					t.Error("compose file should contain environment section")
				}
				var compose containers.ComposeFile
				if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
					t.Fatal(err)
				}
				if compose.Services["dev"].Environment["FOO"] != "bar" {
					t.Error("compose file should contain env var FOO")
				}
			},
		},
		{
			name: "missing base image",
			spec: &ContainerSpec{
				Workdir: "/app",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure spec has defaults applied
			if tt.spec != nil {
				tt.spec.applyDefaults()
			}

			composePath, err := mgr.generateComposeFromSpec(wtPath, tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("generateComposeFromSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				// Read and validate the generated file
				content, readErr := os.ReadFile(composePath)
				if readErr != nil {
					t.Fatalf("failed to read generated compose file: %v", readErr)
				}

				contentStr := string(content)
				var compose map[string]any
				if err := yaml.Unmarshal(content, &compose); err != nil {
					t.Fatal(err)
				}
				wantName, err := containers.ProjectName(wtPath)
				if err != nil {
					t.Fatal(err)
				}
				if compose["name"] != wantName {
					t.Errorf("project name = %q, want %q", compose["name"], wantName)
				}

				// Basic validation
				if _, exists := compose["version"]; exists {
					t.Error("generated compose must omit the obsolete version attribute")
				}
				if !strings.Contains(contentStr, "init: true") {
					t.Error("generated container must reap orphaned processes")
				}
				if !strings.Contains(contentStr, "services:") {
					t.Error("compose file should contain services")
				}

				// Custom validation
				if tt.validate != nil {
					tt.validate(t, contentStr)
				}
			}
		})
	}
}

func TestManager_generateComposeFromSpec_EnvironmentValues(t *testing.T) {
	t.Setenv("BUCKLEY_TEST_COMPOSE_ENV", "resolved: \"value\"\nnext")
	tests := []struct {
		name, value, want string
	}{
		{"colon", "hello: world", "hello: world"},
		{"multiline", "first\nsecond\n", "first\nsecond\n"},
		{"quotes", `say "hello" and 'goodbye'`, `say "hello" and 'goodbye'`},
		{"comment", "value # literal", "value # literal"},
		{"whitespace", "  padded\tvalue  ", "  padded\tvalue  "},
		{"empty", "", ""},
		{"boolean", "false", "false"},
		{"null", "null", "null"},
		{"number", "00123", "00123"},
		{"backslashes", `C:\path\file`, `C:\path\file`},
		{"compose escape", "literal $$HOME", "literal $$HOME"},
		{"resolved variable", "${BUCKLEY_TEST_COMPOSE_ENV}", "resolved: \"value\"\nnext"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &Manager{}
			path, err := mgr.generateComposeFromSpec(t.TempDir(), &ContainerSpec{
				BaseImage: "alpine:latest",
				Workdir:   "/workspace",
				Env:       map[string]string{"MESSAGE": tt.value},
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var compose containers.ComposeFile
			if err := yaml.Unmarshal(data, &compose); err != nil {
				t.Fatal(err)
			}
			if got := compose.Services["dev"].Environment["MESSAGE"]; got != tt.want {
				t.Errorf("MESSAGE = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManager_generateComposeFromSpec_QuotedPaths(t *testing.T) {
	wtPath := filepath.Join(t.TempDir(), `work "tree"`)
	if err := os.Mkdir(wtPath, 0755); err != nil {
		t.Fatal(err)
	}
	spec := &ContainerSpec{
		Name:      "explicit-dev",
		BaseImage: "alpine:latest",
		Workdir:   `/workspace "quoted"`,
		Mounts:    []string{`/host "cache":/cache:ro`},
		Ports:     []string{"127.0.0.1::8080", "8081:81"},
		Env:       map[string]string{"Z": "last", "A": "first"},
	}
	mgr := &Manager{}
	path, err := mgr.generateComposeFromSpec(wtPath, spec)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compose containers.ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}
	dev := compose.Services["dev"]
	wantVolumes := append([]string{filepath.ToSlash(wtPath) + ":" + spec.Workdir}, spec.Mounts...)
	if dev.ContainerName != spec.Name || dev.Image != spec.BaseImage || dev.WorkingDir != spec.Workdir || !dev.Init {
		t.Fatalf("service settings changed: %+v", dev)
	}
	if !reflect.DeepEqual(dev.Volumes, wantVolumes) || !reflect.DeepEqual(dev.Ports, spec.Ports) {
		t.Fatalf("mounts or ports changed: %+v", dev)
	}
	if want := []string{"/bin/sh", "-c", "while sleep 3600; do :; done"}; !reflect.DeepEqual(dev.Command, want) {
		t.Errorf("command = %q, want %q", dev.Command, want)
	}
	if _, err := mgr.generateComposeFromSpec(wtPath, spec); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Error("regeneration changed compose content")
	}
}

func TestManager_renderComposeTemplate(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(cwd, "/worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath := "/path/to/worktree"
	spec := &ContainerSpec{
		Workdir: "/app",
		Env: map[string]string{
			"custom_var": "custom_value",
		},
	}

	template := `version: '3.9'
services:
  dev:
    image: test:latest
    volumes:
      - {{WORKTREE_PATH}}:{{WORKDIR}}
      - {{REPO_PATH}}:/repo
    environment:
      - CUSTOM_VAR={{ENV_CUSTOM_VAR}}
`

	rendered := mgr.renderComposeTemplate(template, wtPath, spec)

	// Verify substitutions
	if !strings.Contains(rendered, wtPath) {
		t.Errorf("rendered template should contain worktree path, got: %s", rendered)
	}
	if !strings.Contains(rendered, cwd) {
		t.Errorf("rendered template should contain repo path, got: %s", rendered)
	}
	if !strings.Contains(rendered, spec.Workdir) {
		t.Errorf("rendered template should contain workdir, got: %s", rendered)
	}
	if !strings.Contains(rendered, "custom_value") {
		t.Errorf("rendered template should contain custom env var, got: %s", rendered)
	}
}

func TestManager_prepareComposeFile_CustomFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(cwd, "/worktrees")
	if err != nil {
		t.Fatal(err)
	}

	// Create a custom compose file in temp dir
	tmpDir := t.TempDir()
	customCompose := filepath.Join(tmpDir, "custom-compose.yml")
	customContent := `name: user-project
version: '3.9'
services:
  dev:
    image: custom:latest
    volumes:
      - {{WORKTREE_PATH}}:/workspace
`
	if err := os.WriteFile(customCompose, []byte(customContent), 0644); err != nil {
		t.Fatal(err)
	}

	wtPath := t.TempDir()
	spec := &ContainerSpec{
		ComposeFile: customCompose, // Use absolute path
		Workdir:     "/workspace",
	}

	composePath, err := mgr.prepareComposeFile(wtPath, spec)
	if err != nil {
		t.Fatalf("prepareComposeFile() error = %v", err)
	}

	// Verify the file was created in worktree
	if !strings.HasPrefix(composePath, wtPath) {
		t.Errorf("compose file should be in worktree path")
	}

	// Verify content was rendered
	content, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, wtPath) {
		t.Error("rendered compose should contain worktree path")
	}
	if contentStr != strings.ReplaceAll(customContent, "{{WORKTREE_PATH}}", wtPath) {
		t.Error("custom compose changed beyond template substitution")
	}
}

func TestManager_prepareComposeFile_NoCustomFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(cwd, "/worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath := t.TempDir()
	spec := &ContainerSpec{
		BaseImage: "golang:1.22",
		Workdir:   "/app",
	}
	spec.applyDefaults()

	composePath, err := mgr.prepareComposeFile(wtPath, spec)
	if err != nil {
		t.Fatalf("prepareComposeFile() error = %v", err)
	}

	// Should generate compose file from spec
	if !strings.HasPrefix(composePath, wtPath) {
		t.Errorf("compose file should be in worktree path")
	}

	// Verify file exists
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		t.Error("compose file was not created")
	}
}

func TestContainerSpec_workspaceMountEnabled_InGeneration(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(cwd, "/worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath := t.TempDir()

	tests := []struct {
		name           string
		mountWorkspace *bool
		wantMount      bool
	}{
		{
			name:           "mount enabled by default",
			mountWorkspace: nil,
			wantMount:      true,
		},
		{
			name:           "mount explicitly enabled",
			mountWorkspace: boolPtr(true),
			wantMount:      true,
		},
		{
			name:           "mount explicitly disabled",
			mountWorkspace: boolPtr(false),
			wantMount:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &ContainerSpec{
				BaseImage:      "alpine:latest",
				Workdir:        "/app",
				MountWorkspace: tt.mountWorkspace,
			}
			spec.applyDefaults()

			composePath, err := mgr.generateComposeFromSpec(wtPath, spec)
			if err != nil {
				t.Fatalf("generateComposeFromSpec() error = %v", err)
			}

			content, err := os.ReadFile(composePath)
			if err != nil {
				t.Fatal(err)
			}

			contentStr := string(content)
			containsMount := strings.Contains(contentStr, wtPath)

			if tt.wantMount && !containsMount {
				t.Error("compose file should contain workspace mount")
			}
			if !tt.wantMount && containsMount {
				t.Error("compose file should not contain workspace mount")
			}
		})
	}
}
