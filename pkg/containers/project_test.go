package containers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"m31labs.dev/buckley/pkg/envdetect"
)

func TestGenerator_ProjectIsolation(t *testing.T) {
	root := t.TempDir()
	generator, err := NewGenerator()
	if err != nil {
		t.Fatal(err)
	}
	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{{Name: "go", Version: "1.26"}},
		Services:  []envdetect.Service{{Type: "postgres", Version: "16"}},
	}
	names := make(map[string]bool)
	for _, repo := range []string{"one", "two"} {
		dir := filepath.Join(root, repo, "Same Branch")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		var firstName string
		for _, file := range []string{"compose.yaml", "docker-compose.worktree.yml"} {
			path := filepath.Join(dir, file)
			if err := generator.Generate(profile, path); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var compose ComposeFile
			if err := yaml.Unmarshal(data, &compose); err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^buckley-same-branch-[0-9a-f]{16}$`).MatchString(compose.Name) {
				t.Fatalf("invalid project name %q", compose.Name)
			}
			if firstName == "" {
				firstName = compose.Name
			} else if compose.Name != firstName {
				t.Fatalf("regeneration changed project: %q -> %q", firstName, compose.Name)
			}
			for name, svc := range compose.Services {
				if svc.ContainerName != "" {
					t.Errorf("service %s has global name %q", name, svc.ContainerName)
				}
			}
		}
		if names[firstName] {
			t.Fatalf("same-basename worktrees share project %q", firstName)
		}
		names[firstName] = true
	}
}

func TestProjectName_PathAliases(t *testing.T) {
	dir := t.TempDir()
	want, err := ProjectName(dir)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, path := range []string{dir + string(filepath.Separator) + ".", alias} {
		got, err := ProjectName(path)
		if err != nil || got != want {
			t.Errorf("ProjectName(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestProjectName_ValidLabel(t *testing.T) {
	for _, label := range []string{"---", "日本語", ".UPPER !", strings.Repeat("a", 120)} {
		t.Run(label, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), label)
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			name, err := ProjectName(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(name) > 63 || !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]+$`).MatchString(name) {
				t.Errorf("invalid Compose project name %q", name)
			}
		})
	}
}

func TestGenerator_ExplicitNamesStayLocal(t *testing.T) {
	generator, err := NewGenerator()
	if err != nil {
		t.Fatal(err)
	}
	profile := &envdetect.EnvironmentProfile{Languages: []envdetect.Language{{Name: "go"}}}
	for _, explicit := range []bool{true, false} {
		dir := t.TempDir()
		overridesDir := filepath.Join(dir, ".buckley", "containers")
		if err := os.MkdirAll(overridesDir, 0755); err != nil {
			t.Fatal(err)
		}
		overrides := "services:\n  dev-go:\n    image: custom/go:latest\n"
		if explicit {
			overrides += "    container_name: my-dev\nname: my-project\n"
		}
		if err := os.WriteFile(filepath.Join(overridesDir, "overrides.yaml"), []byte(overrides), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "compose.yaml")
		if err := generator.Generate(profile, path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var compose ComposeFile
		if err := yaml.Unmarshal(data, &compose); err != nil {
			t.Fatal(err)
		}
		if explicit {
			if compose.Name != "my-project" || compose.Services["dev-go"].ContainerName != "my-dev" {
				t.Fatalf("explicit names lost: %+v", compose)
			}
		} else if compose.Name == "my-project" || compose.Services["dev-go"].ContainerName != "" {
			t.Fatalf("previous worktree overrides leaked: %+v", compose)
		}
	}
}
