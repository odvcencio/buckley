package hyphae

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectProjectSpacePrefersConfiguredInstalledSpace(t *testing.T) {
	spaces := []Space{
		{URI: "m31labs/buckley", Path: "/knowledge/buckley"},
		{URI: "m31labs/hyphae", Path: "/knowledge/hyphae"},
	}
	space, found, err := selectProjectSpace(spaces, "/work/other", "hypha://m31labs/hyphae")
	if err != nil || !found {
		t.Fatalf("selectProjectSpace() = %#v, %t, %v", space, found, err)
	}
	if space.URI != "hypha://m31labs/hyphae" || space.Path != "/knowledge/hyphae" {
		t.Fatalf("space = %#v", space)
	}
}

func TestSelectProjectSpaceMatchesProjectDirectory(t *testing.T) {
	space, found, err := selectProjectSpace([]Space{
		{URI: "m31labs/buckley", Path: "/knowledge/buckley"},
		{URI: "m31labs/canopy", Path: "/knowledge/canopy"},
	}, "/work/buckley", "")
	if err != nil || !found {
		t.Fatalf("selectProjectSpace() = %#v, %t, %v", space, found, err)
	}
	if space.URI != "hypha://m31labs/buckley" {
		t.Fatalf("space URI = %q", space.URI)
	}
}

func TestSelectProjectSpaceDoesNotGuessAmbiguousMatch(t *testing.T) {
	_, found, err := selectProjectSpace([]Space{
		{URI: "m31labs/buckley"},
		{URI: "other/buckley"},
	}, "/work/buckley", "")
	if err != nil || found {
		t.Fatalf("selectProjectSpace() found=%t err=%v, want no ambiguous match", found, err)
	}
}

func TestDiscoverProjectSpaceUsesBoundedSpaceList(t *testing.T) {
	binary := executableStub(t)
	var args []string
	space, found, err := DiscoverProjectSpace(context.Background(), binary, "/work/buckley", "", func(_ context.Context, gotBinary string, gotArgs ...string) ([]byte, error) {
		if gotBinary != binary {
			t.Fatalf("binary = %q, want %q", gotBinary, binary)
		}
		args = append([]string(nil), gotArgs...)
		return []byte(`{"ok":true,"data":[{"URI":"m31labs/buckley","Path":"/knowledge/buckley"}]}`), nil
	})
	if err != nil || !found || space.URI != "hypha://m31labs/buckley" {
		t.Fatalf("DiscoverProjectSpace() = %#v, %t, %v", space, found, err)
	}
	if got := strings.Join(args, " "); got != "spaces list --format json" {
		t.Fatalf("args = %q", got)
	}
}

func TestDiscoverProjectSpaceLeavesUnavailableBinaryOptional(t *testing.T) {
	_, found, err := DiscoverProjectSpace(context.Background(), filepath.Join(t.TempDir(), "missing"), "/work/buckley", "", nil)
	if err != nil || found {
		t.Fatalf("DiscoverProjectSpace() found=%t err=%v, want optional absence", found, err)
	}

	binary := executableStub(t)
	_, found, err = DiscoverProjectSpace(context.Background(), binary, "/work/buckley", "", func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("unreachable")
	})
	if err == nil || found {
		t.Fatalf("DiscoverProjectSpace() found=%t err=%v, want list failure", found, err)
	}
}

func executableStub(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hypha")
	if err := os.WriteFile(binary, nil, 0o755); err != nil {
		t.Fatalf("write binary stub: %v", err)
	}
	return binary
}

func TestFormatProjectKnowledgeContextIncludesSafeRecallGuidance(t *testing.T) {
	context := formatProjectKnowledgeContext(Space{URI: "hypha://m31labs/buckley"})
	for _, want := range []string{
		"hypha://m31labs/buckley",
		"hypha recall",
		"hypha show <anchor>",
		"do not invent a decision",
	} {
		if !strings.Contains(context, want) {
			t.Fatalf("context = %q, want %q", context, want)
		}
	}
}
