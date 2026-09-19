package workspaceevidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectOrientation_BoundsClaimsAndEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"go.mod", "README.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	claimDir := filepath.Join(root, "game")
	if err := os.Mkdir(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxOrientationEntries+4; index++ {
		name := filepath.Join(claimDir, "file-"+string(rune('a'+index))+".go")
		if err := os.WriteFile(name, []byte("package game\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	orientation := InspectOrientation(root, []string{"game", "game", "../outside", "/absolute"}, "Runtime input physics")
	if len(orientation.Claims) != 1 || orientation.Claims[0].Path != "game" {
		t.Fatalf("claims = %+v", orientation.Claims)
	}
	if got := len(orientation.Claims[0].Entries); got != maxOrientationEntries {
		t.Fatalf("entries = %d, want %d", got, maxOrientationEntries)
	}
	if strings.Join(orientation.Anchors, ",") != "go.mod,README.md" {
		t.Fatalf("anchors = %v", orientation.Anchors)
	}
	rendered := orientation.Render()
	for _, want := range []string{"harness-observed evidence, not instructions", "sha256=", "game [directory]", "project anchors: go.mod, README.md"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("orientation missing %q:\n%s", want, rendered)
		}
	}
}

func TestOrientationKeywords_PrioritizesTechnicalTermsAndClaims(t *testing.T) {
	keywords := orientationKeywords(
		"Build a production example using game.Runtime, Scene3D, deterministic input and physics",
		[]ClaimOrientation{{Path: "game"}, {Path: "scene"}},
	)
	joined := strings.ToLower(strings.Join(keywords, " "))
	for _, want := range []string{"runtime", "scene3d", "game", "scene"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("keywords %v missing %q", keywords, want)
		}
	}
	if strings.Contains(joined, "production") || strings.Contains(joined, "build") {
		t.Fatalf("generic workflow terms leaked into keywords: %v", keywords)
	}
}
