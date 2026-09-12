package builtin

import (
	"fmt"
	"strconv"
	"strings"
)

func isPatchFileHeader(line string) bool {
	return strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")
}

func patchHeaderPath(line string) (string, bool) {
	if !isPatchFileHeader(line) {
		return "", false
	}
	raw := strings.TrimSuffix(line[4:], "\r")
	if raw == "" {
		return "", false
	}
	if raw[0] == '"' {
		escaped := false
		for index := 1; index < len(raw); index++ {
			switch {
			case escaped:
				escaped = false
			case raw[index] == '\\':
				escaped = true
			case raw[index] == '"':
				path, err := strconv.Unquote(raw[:index+1])
				if err != nil || path == "" {
					return "", false
				}
				suffix := raw[index+1:]
				if suffix != "" && !strings.HasPrefix(suffix, "\t") {
					return "", false
				}
				return path, true
			}
		}
		return "", false
	}
	if tab := strings.IndexByte(raw, '\t'); tab >= 0 {
		raw = raw[:tab]
	}
	return raw, raw != ""
}

func unifiedPatchHeaderPaths(rawPatch string) ([]string, error) {
	lines := strings.Split(rawPatch, "\n")
	paths := make([]string, 0)
	oldRemaining := 0
	newRemaining := 0
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		if oldRemaining > 0 || newRemaining > 0 {
			if line == `\ No newline at end of file` {
				continue
			}
			if line == "" {
				return nil, fmt.Errorf("patch contains a truncated unified diff hunk")
			}
			switch line[0] {
			case ' ':
				oldRemaining--
				newRemaining--
			case '-':
				oldRemaining--
			case '+':
				newRemaining--
			default:
				return nil, fmt.Errorf("patch contains a malformed unified diff hunk")
			}
			if oldRemaining < 0 || newRemaining < 0 {
				return nil, fmt.Errorf("patch contains a malformed unified diff hunk")
			}
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			var err error
			oldRemaining, newRemaining, err = unifiedHunkCounts(line)
			if err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to ") || strings.HasPrefix(line, "copy from ") || strings.HasPrefix(line, "copy to ") {
			return nil, fmt.Errorf("git rename and copy metadata are not supported by apply_patch")
		}
		if !strings.HasPrefix(line, "--- ") {
			if strings.HasPrefix(line, "+++ ") {
				return nil, fmt.Errorf("patch contains an unpaired unified diff file header")
			}
			continue
		}
		oldPath, ok := patchHeaderPath(line)
		if !ok || index+1 >= len(lines) {
			return nil, fmt.Errorf("patch contains a malformed file header")
		}
		index++
		newLine := strings.TrimSuffix(lines[index], "\r")
		newPath, ok := patchHeaderPath(newLine)
		if !ok || !strings.HasPrefix(newLine, "+++ ") {
			return nil, fmt.Errorf("patch contains unpaired unified diff file headers")
		}
		paths = append(paths, oldPath, newPath)
	}
	if oldRemaining != 0 || newRemaining != 0 {
		return nil, fmt.Errorf("patch contains a truncated unified diff hunk")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("patch must contain paired unified diff file headers")
	}
	return paths, nil
}

func unifiedHunkCounts(line string) (int, int, error) {
	end := strings.Index(line[3:], " @@")
	if end < 0 {
		return 0, 0, fmt.Errorf("patch contains a malformed unified diff hunk header")
	}
	fields := strings.Fields(line[3 : 3+end])
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("patch contains a malformed unified diff hunk header")
	}
	oldCount, err := unifiedRangeCount(fields[0], '-')
	if err != nil {
		return 0, 0, err
	}
	newCount, err := unifiedRangeCount(fields[1], '+')
	if err != nil {
		return 0, 0, err
	}
	return oldCount, newCount, nil
}

func unifiedRangeCount(value string, prefix byte) (int, error) {
	if len(value) < 2 || value[0] != prefix {
		return 0, fmt.Errorf("patch contains a malformed unified diff range")
	}
	rangeParts := strings.Split(value[1:], ",")
	if len(rangeParts) > 2 {
		return 0, fmt.Errorf("patch contains a malformed unified diff range")
	}
	if _, err := strconv.Atoi(rangeParts[0]); err != nil {
		return 0, fmt.Errorf("patch contains a malformed unified diff range")
	}
	if len(rangeParts) == 1 {
		return 1, nil
	}
	count, err := strconv.Atoi(rangeParts[1])
	if err != nil || count < 0 {
		return 0, fmt.Errorf("patch contains a malformed unified diff range")
	}
	return count, nil
}
