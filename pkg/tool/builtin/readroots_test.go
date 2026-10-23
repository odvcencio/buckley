package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layout: a workdir and a separate "hyphae" read root with a spore, secrets and
// a symlink that points out of the root.
func readRootFixture(t *testing.T) (work, root string) {
	t.Helper()
	base := t.TempDir()
	work = filepath.Join(base, "work")
	root = filepath.Join(base, "hyphae")
	for _, d := range []string{work, filepath.Join(root, "spaces", "s", "inbox"), filepath.Join(root, ".catalog", "identities"), filepath.Join(base, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		filepath.Join(work, "main.go"):                              {"package main\n", 0o644},
		filepath.Join(root, "spaces", "s", "inbox", "spore.md"):     {"# Spore title\n", 0o644},
		filepath.Join(root, "spaces", "s", "signing.key"):           {"-----BEGIN KEY-----\n", 0o644},
		filepath.Join(root, ".catalog", "identities", "owner.json"): {"{}\n", 0o644},
		filepath.Join(root, "spaces", "s", "private.md"):            {"private\n", 0o600},
		filepath.Join(root, "spaces", "s", "group.md"):              {"group-readable\n", 0o640},
		filepath.Join(root, "spaces", "s", ".env"):                  {"TOKEN=x\n", 0o644},
		filepath.Join(root, "spaces", "s", "server.pem"):            {"pem\n", 0o644},
		filepath.Join(root, "spaces", "s", "id_ed25519"):            {"key\n", 0o644},
		filepath.Join(root, "spaces", "s", "credentials.json"):      {"{}\n", 0o644},
		filepath.Join(base, "outside", "secret.txt"):                {"outside\n", 0o644},
	}
	for path, f := range files {
		if err := os.WriteFile(path, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "outside", "secret.txt"), filepath.Join(root, "spaces", "s", "escape.md")); err != nil {
		t.Fatal(err)
	}
	return work, root
}

func TestReadRoots_ReadOnlyToolsReachRootsAndNothingElse(t *testing.T) {
	work, root := readRootFixture(t)
	spore := filepath.Join(root, "spaces", "s", "inbox", "spore.md")

	read := &ReadFileTool{}
	read.SetWorkDir(work)
	if res, _ := read.Execute(map[string]any{"path": spore}); res == nil || res.Success {
		t.Fatalf("without a read root the spore must stay fenced: %+v", res)
	}
	read.SetReadRoots([]string{root})
	res, err := read.Execute(map[string]any{"path": spore})
	if err != nil || res == nil || !res.Success || !strings.Contains(readRootText(res.Data), "Spore title") {
		t.Fatalf("spore not readable through a read root: %+v err=%v", res, err)
	}
	if res, _ := read.Execute(map[string]any{"path": filepath.Join(filepath.Dir(root), "outside", "secret.txt")}); res == nil || res.Success {
		t.Fatalf("a path outside the workdir and every root was readable: %+v", res)
	}

	list := &ListDirectoryTool{}
	list.SetWorkDir(work)
	list.SetReadRoots([]string{root})
	if res, _ := list.Execute(map[string]any{"path": filepath.Join(root, "spaces", "s", "inbox")}); res == nil || !res.Success {
		t.Fatalf("list_directory could not list a read root: %+v", res)
	}

	find := &FindFilesTool{}
	find.SetWorkDir(work)
	find.SetReadRoots([]string{root})
	res, _ = find.Execute(map[string]any{"pattern": "**/*", "base_path": root})
	if res == nil || !res.Success {
		t.Fatalf("find_files could not search a read root: %+v", res)
	}
	found := readRootText(res.Data)
	if !strings.Contains(found, "spore.md") {
		t.Fatalf("find_files missed the spore: %s", found)
	}
	for _, secret := range []string{"signing.key", "owner.json", "private.md", ".env", "server.pem", "id_ed25519", "credentials.json"} {
		if strings.Contains(found, secret) {
			t.Errorf("find_files listed secret file %s", secret)
		}
	}
}

func TestReadRoots_SecretsAreRefusedWithAReason(t *testing.T) {
	work, root := readRootFixture(t)
	read := &ReadFileTool{}
	read.SetWorkDir(work)
	read.SetReadRoots([]string{root})
	for _, name := range []string{
		"spaces/s/signing.key", ".catalog/identities/owner.json", "spaces/s/private.md",
		"spaces/s/.env", "spaces/s/server.pem", "spaces/s/id_ed25519", "spaces/s/credentials.json",
	} {
		res, _ := read.Execute(map[string]any{"path": filepath.Join(root, name)})
		if res == nil || res.Success {
			t.Errorf("%s was readable", name)
			continue
		}
		if !strings.Contains(res.Error, "secret files are not readable through tools") {
			t.Errorf("%s: refusal %q does not say why", name, res.Error)
		}
	}
	// A symlink out of the root is refused even though the link itself is public.
	res, _ := read.Execute(map[string]any{"path": filepath.Join(root, "spaces", "s", "escape.md")})
	if res == nil || res.Success {
		t.Fatalf("symlink escaping the root was readable: %+v", res)
	}
}

func TestReadRoots_WriteToolsStayFenced(t *testing.T) {
	work, root := readRootFixture(t)
	target := filepath.Join(root, "spaces", "s", "inbox", "new.md")

	write := &WriteFileTool{}
	write.SetWorkDir(work)
	write.SetReadRoots([]string{root})
	if res, _ := write.Execute(map[string]any{"path": target, "content": "x"}); res == nil || res.Success {
		t.Fatalf("write_file wrote into a read root: %+v", res)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file exists after refused write: %v", err)
	}
	edit := &EditFileTool{}
	edit.SetWorkDir(work)
	edit.SetReadRoots([]string{root})
	spore := filepath.Join(root, "spaces", "s", "inbox", "spore.md")
	if res, _ := edit.Execute(map[string]any{"path": spore, "old_string": "Spore", "new_string": "Edited"}); res == nil || res.Success {
		t.Fatalf("edit_file edited inside a read root: %+v", res)
	}
	body, _ := os.ReadFile(spore)
	if !strings.Contains(string(body), "Spore title") {
		t.Fatalf("spore changed: %s", body)
	}
}

func TestReadRoots_UnreadableRootEntriesAreNotListed(t *testing.T) {
	work, root := readRootFixture(t)
	list := &ListDirectoryTool{}
	list.SetWorkDir(work)
	list.SetReadRoots([]string{root})
	res, _ := list.Execute(map[string]any{"path": filepath.Join(root, "spaces", "s")})
	if res == nil || !res.Success {
		t.Fatalf("list failed: %+v", res)
	}
	listing := readRootText(res.Data)
	if strings.Contains(listing, "signing.key") || strings.Contains(listing, "private.md") {
		t.Errorf("listing shows secret files: %s", listing)
	}
}

func readRootText(v any) string { return fmt.Sprintf("%v", v) }

func TestReadRoots_GroupReadableFileIsAllowed(t *testing.T) {
	work, root := readRootFixture(t)
	read := &ReadFileTool{}
	read.SetWorkDir(work)
	read.SetReadRoots([]string{root})
	res, _ := read.Execute(map[string]any{"path": filepath.Join(root, "spaces", "s", "group.md")})
	if res == nil || !res.Success {
		t.Fatalf("a file readable by its group is not private: %+v", res)
	}
}

// A root may name one file: that file is readable and its siblings are not.
func TestReadRoots_SingleFileRoot(t *testing.T) {
	work, root := readRootFixture(t)
	spore := filepath.Join(root, "spaces", "s", "inbox", "spore.md")
	sibling := filepath.Join(root, "spaces", "s", "inbox", "sibling.md")
	if err := os.WriteFile(sibling, []byte("sibling\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := &ReadFileTool{}
	read.SetWorkDir(work)
	read.SetReadRoots([]string{spore})
	if res, _ := read.Execute(map[string]any{"path": spore}); res == nil || !res.Success {
		t.Fatalf("the named file is not readable: %+v", res)
	}
	if res, _ := read.Execute(map[string]any{"path": sibling}); res == nil || res.Success {
		t.Fatalf("a sibling of a single-file root was readable: %+v", res)
	}
	if res, _ := read.Execute(map[string]any{"path": filepath.Dir(spore)}); res != nil && res.Success {
		t.Fatalf("the parent directory of a single-file root was readable: %+v", res)
	}
}

func TestReadRoots_SearchFiltersSecretContentAndContext(t *testing.T) {
	if !toolExists("rg") {
		t.Skip("ripgrep is required")
	}
	work, root := readRootFixture(t)
	search := &SearchTextTool{}
	search.SetWorkDir(work)
	search.SetReadRoots([]string{root})
	res, err := search.Execute(map[string]any{"path": root, "query": ".", "context_before": 2, "context_after": 2})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("search failed: %+v %v", res, err)
	}
	got := readRootText(res.Data)
	if !strings.Contains(got, "Spore title") {
		t.Fatalf("missing allowed content: %s", got)
	}
	for _, forbidden := range []string{"BEGIN KEY", "TOKEN=x", "private.md", "owner.json", "credentials.json", "outside"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("search exposed %q: %s", forbidden, got)
		}
	}
}

func TestReadRoots_ExistenceAndOutlineDenySecrets(t *testing.T) {
	work, root := readRootFixture(t)
	exists := &FileExistsTool{}
	exists.SetWorkDir(work)
	exists.SetReadRoots([]string{root})
	outline := &CodeOutlineTool{}
	outline.SetWorkDir(work)
	outline.SetReadRoots([]string{root})
	secret := filepath.Join(root, "spaces", "s", "signing.key")
	if res, _ := exists.Execute(map[string]any{"path": secret}); res == nil || res.Success {
		t.Fatalf("existence probe accepted secret: %+v", res)
	}
	if res, _ := outline.Execute(map[string]any{"path": secret}); res == nil || res.Success {
		t.Fatalf("outline accepted secret: %+v", res)
	}
}
