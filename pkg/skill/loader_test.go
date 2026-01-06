package skill

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSkillFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		want        *Skill
		wantErr     bool
		errContains string
	}{
		{
			name: "valid skill with all fields",
			content: `---
name: test-skill
description: A test skill
phase: execute
requires_todo: true
priority: 10
allowed_tools: ["read", "write"]
todo_template: "- Step 1\n- Step 2"
---

# Test Skill

This is the skill content.`,
			want: &Skill{
				Name:         "test-skill",
				Description:  "A test skill",
				Phase:        "execute",
				RequiresTodo: true,
				Priority:     10,
				AllowedTools: []string{"read", "write"},
				TodoTemplate: "- Step 1\n- Step 2",
				Content:      "# Test Skill\n\nThis is the skill content.",
			},
			wantErr: false,
		},
		{
			name: "minimal valid skill",
			content: `---
name: minimal-skill
description: Minimal skill
---

Content here.`,
			want: &Skill{
				Name:        "minimal-skill",
				Description: "Minimal skill",
				Content:     "Content here.",
			},
			wantErr: false,
		},
		{
			name: "missing frontmatter",
			content: `# Not a valid skill file

Just content without frontmatter.`,
			wantErr:     true,
			errContains: "missing YAML frontmatter",
		},
		{
			name: "invalid YAML",
			content: `---
name: test
description: [invalid: yaml: structure
---

Content`,
			wantErr:     true,
			errContains: "failed to parse YAML frontmatter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := NewLoader()
			got, err := loader.parseSkillFile(tt.content)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseSkillFile() error = nil, wantErr %v", tt.wantErr)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("parseSkillFile() error = %v, want error containing %v", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("parseSkillFile() unexpected error = %v", err)
				return
			}

			if got.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", got.Name, tt.want.Name)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %v, want %v", got.Description, tt.want.Description)
			}
			if got.Phase != tt.want.Phase {
				t.Errorf("Phase = %v, want %v", got.Phase, tt.want.Phase)
			}
			if got.RequiresTodo != tt.want.RequiresTodo {
				t.Errorf("RequiresTodo = %v, want %v", got.RequiresTodo, tt.want.RequiresTodo)
			}
			if got.Content != tt.want.Content {
				t.Errorf("Content = %v, want %v", got.Content, tt.want.Content)
			}
		})
	}
}

func TestLoadPersonal(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, ".buckley", "skills")
	err := os.MkdirAll(skillsDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create a test skill file
	skillContent := `---
name: personal-skill
description: A personal test skill
phase: planning
---

# Personal Skill

Test content.`

	skillFile := filepath.Join(skillsDir, "personal-skill.md")
	err = os.WriteFile(skillFile, []byte(skillContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test skill file: %v", err)
	}

	// Override home directory for test
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	// Load skills
	loader := NewLoader()
	skills := make(map[string]*Skill)
	err = loader.LoadPersonal(skills)
	if err != nil {
		t.Errorf("LoadPersonal() error = %v", err)
		return
	}

	// Verify skill was loaded
	skill, ok := skills["personal-skill"]
	if !ok {
		t.Fatal("personal-skill not loaded")
	}

	if skill.Name != "personal-skill" {
		t.Errorf("Name = %v, want personal-skill", skill.Name)
	}
	if skill.Source != "personal" {
		t.Errorf("Source = %v, want personal", skill.Source)
	}
	if skill.Phase != "planning" {
		t.Errorf("Phase = %v, want planning", skill.Phase)
	}
	if skill.LoadedAt.IsZero() {
		t.Error("LoadedAt should be set")
	}
}

func TestLoadProject(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, ".buckley", "skills")
	err := os.MkdirAll(skillsDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create a test skill file
	skillContent := `---
name: project-skill
description: A project test skill
phase: execute
---

# Project Skill

Test content.`

	skillFile := filepath.Join(skillsDir, "project-skill.md")
	err = os.WriteFile(skillFile, []byte(skillContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test skill file: %v", err)
	}

	// Change to test directory
	originalWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalWd)

	// Load skills
	loader := NewLoader()
	skills := make(map[string]*Skill)
	err = loader.LoadProject(skills)
	if err != nil {
		t.Errorf("LoadProject() error = %v", err)
		return
	}

	// Verify skill was loaded
	skill, ok := skills["project-skill"]
	if !ok {
		t.Fatal("project-skill not loaded")
	}

	if skill.Name != "project-skill" {
		t.Errorf("Name = %v, want project-skill", skill.Name)
	}
	if skill.Source != "project" {
		t.Errorf("Source = %v, want project", skill.Source)
	}
	if skill.Phase != "execute" {
		t.Errorf("Phase = %v, want execute", skill.Phase)
	}
}

func TestLoadFromDirectory_Precedence(t *testing.T) {
	// Create temporary directory with multiple skill files
	tmpDir := t.TempDir()

	// Create first version of skill
	skillContent1 := `---
name: test-skill
description: First version
priority: 5
---

First content.`

	skillFile1 := filepath.Join(tmpDir, "test-skill.md")
	err := os.WriteFile(skillFile1, []byte(skillContent1), 0644)
	if err != nil {
		t.Fatalf("Failed to write first skill file: %v", err)
	}

	// Load first version
	loader := NewLoader()
	skills := make(map[string]*Skill)
	err = loader.loadFromDirectory(tmpDir, "test", skills)
	if err != nil {
		t.Errorf("loadFromDirectory() error = %v", err)
		return
	}

	if skills["test-skill"].Priority != 5 {
		t.Errorf("Priority = %v, want 5", skills["test-skill"].Priority)
	}

	// Now load a second version with higher priority (simulating override)
	skillContent2 := `---
name: test-skill
description: Second version
priority: 10
---

Second content.`

	skillFile2 := filepath.Join(tmpDir, "override", "test-skill.md")
	err = os.MkdirAll(filepath.Dir(skillFile2), 0755)
	if err != nil {
		t.Fatalf("Failed to create override directory: %v", err)
	}
	err = os.WriteFile(skillFile2, []byte(skillContent2), 0644)
	if err != nil {
		t.Fatalf("Failed to write second skill file: %v", err)
	}

	err = loader.loadFromDirectory(filepath.Dir(skillFile2), "test", skills)
	if err != nil {
		t.Errorf("loadFromDirectory() error = %v", err)
		return
	}

	// Second version should override first
	if skills["test-skill"].Priority != 10 {
		t.Errorf("Priority = %v, want 10 (override should take effect)", skills["test-skill"].Priority)
	}
	if skills["test-skill"].Description != "Second version" {
		t.Errorf("Description = %v, want 'Second version'", skills["test-skill"].Description)
	}
}

func TestLoadFromDirectory_SKILL_MD(t *testing.T) {
	// Create temporary directory with subdirectory containing SKILL.md
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	err := os.MkdirAll(skillDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create skill directory: %v", err)
	}

	skillContent := `---
name: my-skill
description: Skill in subdirectory
---

Content from SKILL.md.`

	skillFile := filepath.Join(skillDir, "SKILL.md")
	err = os.WriteFile(skillFile, []byte(skillContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write SKILL.md: %v", err)
	}

	// Load skills
	loader := NewLoader()
	skills := make(map[string]*Skill)
	err = loader.loadFromDirectory(tmpDir, "test", skills)
	if err != nil {
		t.Errorf("loadFromDirectory() error = %v", err)
		return
	}

	// Verify skill was loaded
	skill, ok := skills["my-skill"]
	if !ok {
		t.Fatal("my-skill not loaded from subdirectory SKILL.md")
	}

	if skill.Description != "Skill in subdirectory" {
		t.Errorf("Description = %v, want 'Skill in subdirectory'", skill.Description)
	}
}

func TestLoadFromDirectory_NonExistent(t *testing.T) {
	loader := NewLoader()
	skills := make(map[string]*Skill)

	// Loading from non-existent directory should not error
	err := loader.loadFromDirectory("/nonexistent/path/to/skills", "test", skills)
	if err != nil {
		t.Errorf("loadFromDirectory() should not error on non-existent directory, got: %v", err)
	}

	if len(skills) != 0 {
		t.Errorf("skills map should be empty, got %d skills", len(skills))
	}
}

func TestSkill_Validation(t *testing.T) {
	tests := []struct {
		name        string
		skill       *Skill
		wantErr     bool
		errContains string
	}{
		{
			name: "valid skill",
			skill: &Skill{
				Name:        "valid",
				Description: "A valid skill",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			skill: &Skill{
				Description: "Missing name",
			},
			wantErr:     true,
			errContains: "name is required",
		},
		{
			name: "missing description",
			skill: &Skill{
				Name: "no-description",
			},
			wantErr:     true,
			errContains: "description is required",
		},
		{
			name: "name too long",
			skill: &Skill{
				Name:        "this-is-a-very-long-skill-name-that-exceeds-the-maximum-allowed-length-of-sixty-four-characters",
				Description: "Valid description",
			},
			wantErr:     true,
			errContains: "name must be 64 characters or less",
		},
		{
			name: "description too long",
			skill: &Skill{
				Name: "valid-name",
				Description: func() string {
					// Generate a description over 1024 characters
					desc := ""
					for i := 0; i < 130; i++ {
						desc += "verylongdescription"
					}
					return desc
				}(),
			},
			wantErr:     true,
			errContains: "description must be 1024 characters or less",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.skill.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() error = nil, wantErr %v", tt.wantErr)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("Validate() error = %v, want error containing %v", err, tt.errContains)
				}
			} else if err != nil {
				t.Errorf("Validate() unexpected error = %v", err)
			}
		})
	}
}

func TestSkill_IsPhaseSkill(t *testing.T) {
	tests := []struct {
		name  string
		skill *Skill
		want  bool
	}{
		{
			name:  "skill with phase",
			skill: &Skill{Phase: "execute"},
			want:  true,
		},
		{
			name:  "skill without phase",
			skill: &Skill{Phase: ""},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.skill.IsPhaseSkill(); got != tt.want {
				t.Errorf("IsPhaseSkill() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkill_HasToolRestrictions(t *testing.T) {
	tests := []struct {
		name  string
		skill *Skill
		want  bool
	}{
		{
			name:  "skill with tool restrictions",
			skill: &Skill{AllowedTools: []string{"read", "write"}},
			want:  true,
		},
		{
			name:  "skill without tool restrictions",
			skill: &Skill{AllowedTools: []string{}},
			want:  false,
		},
		{
			name:  "skill with nil tool restrictions",
			skill: &Skill{AllowedTools: nil},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.skill.HasToolRestrictions(); got != tt.want {
				t.Errorf("HasToolRestrictions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActiveSkill_Getters(t *testing.T) {
	now := time.Now()
	skill := &Skill{Name: "test"}
	activeSkill := &ActiveSkill{
		Skill:       skill,
		Scope:       "test-scope",
		ActivatedAt: now,
		ActivatedBy: "user",
	}

	if activeSkill.GetScope() != "test-scope" {
		t.Errorf("GetScope() = %v, want test-scope", activeSkill.GetScope())
	}
	if activeSkill.GetActivatedAt() != now {
		t.Errorf("GetActivatedAt() = %v, want %v", activeSkill.GetActivatedAt(), now)
	}
	if activeSkill.GetActivatedBy() != "user" {
		t.Errorf("GetActivatedBy() = %v, want user", activeSkill.GetActivatedBy())
	}
	if activeSkill.GetSkill() != skill {
		t.Errorf("GetSkill() returned different skill instance")
	}
}

func TestFormatTodoRequirement(t *testing.T) {
	tests := []struct {
		name           string
		skill          *Skill
		hasTodos       bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "skill requires TODO and none exist",
			skill: &Skill{
				RequiresTodo: true,
				TodoTemplate: "- Step 1\n- Step 2",
			},
			hasTodos: false,
			wantContains: []string{
				"⚠️",
				"REQUIRES TODO tracking",
				"must create",
				"Recommended TODO structure",
				"Step 1",
				"Step 2",
			},
		},
		{
			name: "skill requires TODO and they exist",
			skill: &Skill{
				RequiresTodo: true,
				TodoTemplate: "- Step 1\n- Step 2",
			},
			hasTodos: true,
			wantContains: []string{
				"Recommended TODO structure",
				"Step 1",
			},
			wantNotContain: []string{
				"⚠️",
				"must create",
			},
		},
		{
			name: "skill requires TODO without template",
			skill: &Skill{
				RequiresTodo: true,
				TodoTemplate: "",
			},
			hasTodos: false,
			wantContains: []string{
				"⚠️",
				"REQUIRES TODO tracking",
			},
			wantNotContain: []string{
				"Recommended TODO structure",
			},
		},
		{
			name: "skill doesn't require TODO",
			skill: &Skill{
				RequiresTodo: false,
				TodoTemplate: "- Should not appear",
			},
			hasTodos:     false,
			wantContains: []string{},
			wantNotContain: []string{
				"TODO",
				"Should not appear",
			},
		},
		{
			name: "nil metadata",
			skill: &Skill{
				RequiresTodo: true,
			},
			hasTodos: false, // irrelevant when metadata is nil
			wantContains: []string{
				"⚠️",
				"REQUIRES TODO tracking",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock metadata
			var metadata SkillConversationMetadata
			if tt.name != "nil metadata" {
				metadata = &mockMetadata{hasTodos: tt.hasTodos}
			}

			result := FormatTodoRequirement(tt.skill, metadata)

			// Check expected content
			for _, want := range tt.wantContains {
				if !contains(result, want) {
					t.Errorf("FormatTodoRequirement() missing expected content %q\nGot: %s", want, result)
				}
			}

			// Check unwanted content
			for _, unwanted := range tt.wantNotContain {
				if contains(result, unwanted) {
					t.Errorf("FormatTodoRequirement() contains unwanted content %q\nGot: %s", unwanted, result)
				}
			}
		})
	}
}

func TestSkillGetters(t *testing.T) {
	skill := &Skill{
		Name:         "test-skill",
		Description:  "Test description",
		Content:      "Test content",
		AllowedTools: []string{"read", "write"},
		RequiresTodo: true,
		TodoTemplate: "- Task 1",
		Phase:        "execute",
	}

	tests := []struct {
		name     string
		getter   func() string
		expected string
	}{
		{"GetName", skill.GetName, "test-skill"},
		{"GetDescription", skill.GetDescription, "Test description"},
		{"GetContent", skill.GetContent, "Test content"},
		{"GetTodoTemplate", skill.GetTodoTemplate, "- Task 1"},
		{"GetPhase", skill.GetPhase, "execute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.getter()
			if got != tt.expected {
				t.Errorf("%s = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}

	// Test GetAllowedTools
	tools := skill.GetAllowedTools()
	if len(tools) != 2 {
		t.Errorf("GetAllowedTools() len = %d, want 2", len(tools))
	}
	if tools[0] != "read" || tools[1] != "write" {
		t.Errorf("GetAllowedTools() = %v, want [read write]", tools)
	}

	// Test GetRequiresTodo
	if !skill.GetRequiresTodo() {
		t.Error("GetRequiresTodo() = false, want true")
	}
}

func TestLoadAll(t *testing.T) {
	registry := NewRegistry()

	// LoadAll should not error even if directories don't exist
	err := registry.LoadAll()
	if err != nil {
		t.Errorf("LoadAll() error = %v, want nil", err)
	}
}

func TestLoadBundled(t *testing.T) {
	loader := NewLoader()
	skills := make(map[string]*Skill)

	// LoadBundled should load embedded skills
	err := loader.LoadBundled(skills)
	if err != nil {
		t.Errorf("LoadBundled() error = %v", err)
	}

	// Should have loaded some bundled skills
	// Note: This depends on what's in pkg/skill/bundled/
	if len(skills) == 0 {
		t.Log("LoadBundled() loaded 0 skills - check if bundled/ directory has .md files")
	}

	// Verify loaded skills have correct source
	for name, skill := range skills {
		if skill.Source != "bundled" {
			t.Errorf("Skill %s has Source = %s, want 'bundled'", name, skill.Source)
		}
		if skill.LoadedAt.IsZero() {
			t.Errorf("Skill %s has zero LoadedAt timestamp", name)
		}
	}
}

// Mock metadata for testing
type mockMetadata struct {
	hasTodos bool
}

func (m *mockMetadata) GetMetadata(key string) any {
	if key == MetadataKeyHasTodos {
		return m.hasTodos
	}
	return nil
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
