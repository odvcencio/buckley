package oneshot

import (
	"bytes"
	"os/exec"
	"strings"
)

// generatedByAttrs asks git which of paths carry a generated marking:
// linguist-generated set (or true), or diff unset (-diff). With cached it reads
// attributes from the index, so a staged .gitattributes takes effect. It returns
// nil when git cannot answer, which leaves the path-based classification alone.
func generatedByAttrs(paths []string, cached bool) func(string) bool {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"--no-pager", "check-attr"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "-z", "--stdin", "linguist-generated", "diff")
	cmd := exec.Command("git", args...)
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
