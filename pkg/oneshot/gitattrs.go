package oneshot

import (
	"bytes"
	"os/exec"
	"strings"

	"m31labs.dev/buckley/internal/gitversion"
)

// HeadAttributesSupported reports whether the installed git can read
// gitattributes from a commit (--attr-source, git 2.40 and newer), and the
// version it found. A caller that gathers evidence for a head ref uses it to
// tell the user when git will read the working tree's attributes instead.
func HeadAttributesSupported() (ok bool, installed string) {
	return gitversion.AtLeast(2, 40)
}

// headAttrSource returns the ref whose tree supplies gitattributes for a
// source that names a head, or "" when they come from the working tree.
//
// Git applies attributes when it renders a diff: a "-diff" line turns a text
// file into "Binary files differ", and the line can come from an uncommitted
// .gitattributes or from the branch the worktree has checked out. Neither
// belongs to the head. Reading attributes from the head's tree keeps the
// evidence a function of commits. A git older than 2.40 cannot do that, so it
// reads the working tree, as it did before heads existed.
func headAttrSource(params map[string]string) string {
	head := params["head"]
	if head == "" || params["staged"] == "true" {
		return ""
	}
	if ok, _ := HeadAttributesSupported(); !ok {
		return ""
	}
	return head
}

// attrSourceArgs returns the global git option that reads gitattributes from
// the tree of source. It goes before the subcommand.
func attrSourceArgs(source string) []string {
	if source == "" {
		return nil
	}
	return []string{"--attr-source=" + source}
}

// generatedByAttrs asks git which of paths carry a generated marking:
// linguist-generated set (or true), or diff unset (-diff). With cached it reads
// attributes from the index, so a staged .gitattributes takes effect. With a
// source ref it reads them from that commit's tree instead of the working tree.
// It returns nil when git cannot answer, which leaves the path-based
// classification alone.
//
// A diff names files from the root of the work tree, and git resolves the paths
// it is given against its current directory, so the lookup runs from the root.
func generatedByAttrs(paths []string, cached bool, source, workDir string) func(string) bool {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"--no-pager"}
	if !cached {
		args = append(args, attrSourceArgs(source)...)
	}
	args = append(args, "check-attr")
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "-z", "--stdin", "linguist-generated", "diff")
	cmd := exec.Command("git", args...)
	cmd.Dir = workTreeRoot(workDir)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	generated := parseCheckAttr(out)
	if len(generated) == 0 {
		return nil
	}
	return func(path string) bool { return generated[path] }
}

// workTreeRoot returns the top of the work tree that holds dir ("" is the
// current directory). When git cannot say, for instance in a bare repository, it
// returns dir unchanged.
func workTreeRoot(dir string) string {
	cmd := exec.Command("git", "--no-pager", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return dir
	}
	root := strings.TrimSuffix(string(out), "\n")
	if root == "" {
		return dir
	}
	return root
}

// parseCheckAttr reads `git check-attr -z` output: path, attribute, and value,
// each NUL-terminated.
func parseCheckAttr(out []byte) map[string]bool {
	fields := bytes.Split(out, []byte{0})
	generated := map[string]bool{}
	for i := 0; i+2 < len(fields); i += 3 {
		path, attr, value := string(fields[i]), string(fields[i+1]), string(fields[i+2])
		switch attr {
		case "linguist-generated":
			if value == "set" || value == "true" {
				generated[path] = true
			}
		case "diff":
			if value == "unset" {
				generated[path] = true
			}
		}
	}
	return generated
}
