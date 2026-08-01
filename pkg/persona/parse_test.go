package persona

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse_TillerFileShapes(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		want     Persona
		wantBody string // substring expected in Prompt
	}{
		{
			name: "real tiller worker file (Claude Code shape)",
			file: "testdata/tiller-worker.md",
			want: Persona{
				Name:         "tiller-worker",
				Description:  "Use to write or modify code, run builds/tests, or execute any file-mutating work — runs on sonnet. Delegate here for all implementation, editing, and execution tasks.",
				Mode:         ModeSubagent,
				Model:        "sonnet",
				Tier:         "",
				AllowedTools: []string{"Read", "Glob", "Grep", "Edit", "Write", "Bash"},
			},
			wantBody: "You are tiller-worker, a focused execution agent running on sonnet.",
		},
		{
			name: "real tiller investigator file (Claude Code shape)",
			file: "testdata/tiller-investigator.md",
			want: Persona{
				Name:         "tiller-investigator",
				Description:  "Use for deep read-only investigation, code tracing, or adversarial verification — runs on opus. Delegate here when you need to understand how something works, trace a call chain, or verify a claim against source code. Does not write files.",
				Mode:         ModeSubagent,
				Model:        "opus",
				Tier:         "",
				AllowedTools: []string{"Read", "Glob", "Grep", "WebFetch", "Bash"},
			},
			wantBody: "You are tiller-investigator, a read-only research agent running on opus.",
		},
		{
			name: "buckley superset fields (additive, still parses)",
			file: "testdata/buckley-superset.md",
			want: Persona{
				Name:         "buckley-reviewer",
				Description:  "Buckley-superset persona exercising the additive fields (mode, tier, permission, step_cap, color) layered on top of tiller's Claude Code shape.",
				Mode:         ModeSubagent,
				Model:        "opus",
				Tier:         TierScrutiny,
				AllowedTools: []string{"Read", "Glob", "Grep"},
				StepCap:      12,
				Color:        "purple",
				PermissionOverrides: map[string]string{
					"edit": "deny",
					"bash": "allow",
					"task": "\"*\": deny\n\"tiller-investigator\": allow",
				},
			},
			wantBody: "You are buckley-reviewer, a read-only review persona.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}
			got, err := Parse(data, tc.file)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotBody := got.Prompt
			got.Prompt = ""
			got.SourcePath = ""
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse mismatch:\n got  = %+v\n want = %+v", got, tc.want)
			}
			if tc.wantBody != "" && !strings.Contains(gotBody, tc.wantBody) {
				t.Fatalf("Prompt = %q, want substring %q", gotBody, tc.wantBody)
			}
		})
	}
}

func TestParse_MissingFrontmatterIsSkippable(t *testing.T) {
	_, err := Parse([]byte("# Not a persona file\n\njust prose.\n"), "README.md")
	if !errors.Is(err, ErrNotAPersonaFile) {
		t.Fatalf("err = %v, want ErrNotAPersonaFile", err)
	}
}

func TestParse_UnterminatedFrontmatterErrors(t *testing.T) {
	_, err := Parse([]byte("---\nname: broken\n"), "broken.md")
	if err == nil {
		t.Fatalf("expected an error for unterminated frontmatter")
	}
	if errors.Is(err, ErrNotAPersonaFile) {
		t.Fatalf("unterminated frontmatter should not be classified as ErrNotAPersonaFile")
	}
}

func TestParse_MissingNameFallsBackToFilename(t *testing.T) {
	data := []byte("---\ndescription: no explicit name\n---\nbody\n")
	p, err := Parse(data, filepath.Join("some", "dir", "tiller-scout.md"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Name != "tiller-scout" {
		t.Fatalf("Name = %q, want tiller-scout", p.Name)
	}
}

func TestParse_MissingNameNoPathErrors(t *testing.T) {
	data := []byte("---\ndescription: no name, no path\n---\nbody\n")
	if _, err := Parse(data, ""); err == nil {
		t.Fatalf("expected error when name is unresolvable")
	}
}

func TestParse_ToolsFieldSplitsAndTrims(t *testing.T) {
	data := []byte("---\nname: x\ntools: Read,  Glob ,Grep\n---\nbody\n")
	p, err := Parse(data, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"Read", "Glob", "Grep"}
	if !reflect.DeepEqual(p.AllowedTools, want) {
		t.Fatalf("AllowedTools = %v, want %v", p.AllowedTools, want)
	}
}

func TestParse_OmittedToolsIsNil(t *testing.T) {
	data := []byte("---\nname: x\n---\nbody\n")
	p, err := Parse(data, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.AllowedTools != nil {
		t.Fatalf("AllowedTools = %v, want nil", p.AllowedTools)
	}
}

func TestParse_PrimaryMode(t *testing.T) {
	data := []byte("---\nname: orchestrator\nmode: primary\n---\nbody\n")
	p, err := Parse(data, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Mode != ModePrimary {
		t.Fatalf("Mode = %q, want %q", p.Mode, ModePrimary)
	}
	if p.EffectiveMode() != ModePrimary {
		t.Fatalf("EffectiveMode = %q, want %q", p.EffectiveMode(), ModePrimary)
	}
}
