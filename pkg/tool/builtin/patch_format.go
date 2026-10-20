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
				if !strings.ContainsRune("abfnrtv\\\"01234567", rune(raw[index])) {
					return "", false
				}
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

type unifiedPatchFile struct {
	oldPath, newPath, indexPath           string
	removedBytes, addedBytes              int64
	creates, hasHunk, stripCR             bool
	removesUnterminated, addsUnterminated bool
	lastNewAddedUnterminated              bool
}

func parseUnifiedPatch(rawPatch string, bounded bool) ([]unifiedPatchFile, error) {
	files := make([]unifiedPatchFile, 0)
	oldRemaining, newRemaining := 0, 0
	var oldPath, indexPath string
	var lastBody byte
	needsHeaders := false
	for rawLine := range strings.SplitSeq(rawPatch, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if line == `\ No newline at end of file` {
			if len(files) > 0 {
				file := &files[len(files)-1]
				switch lastBody {
				case '-':
					file.removedBytes--
					file.removesUnterminated = true
				case '+':
					file.addsUnterminated = true
					file.lastNewAddedUnterminated = true
				}
			}
			lastBody = 0
			continue
		}
		if oldRemaining > 0 || newRemaining > 0 {
			if rawLine == "" {
				return nil, fmt.Errorf("patch contains a truncated unified diff hunk")
			}
			file := &files[len(files)-1]
			payload := rawLine[1:]
			if file.stripCR {
				payload = strings.TrimSuffix(payload, "\r")
			}
			lastBody = rawLine[0]
			switch lastBody {
			case ' ':
				file.lastNewAddedUnterminated = false
				oldRemaining--
				newRemaining--
			case '-':
				oldRemaining--
				file.removedBytes += int64(len(payload)) + 1
			case '+':
				file.lastNewAddedUnterminated = false
				newRemaining--
				file.addedBytes += int64(len(payload)) + 1
			default:
				return nil, fmt.Errorf("patch contains a malformed unified diff hunk")
			}
			if oldRemaining < 0 || newRemaining < 0 {
				return nil, fmt.Errorf("patch contains a malformed unified diff hunk")
			}
			continue
		}
		lastBody = 0
		if oldPath != "" {
			newPath, ok := patchHeaderPath(line)
			if !ok || !strings.HasPrefix(line, "+++ ") {
				return nil, fmt.Errorf("patch contains unpaired unified diff file headers")
			}
			files = append(files, unifiedPatchFile{oldPath: oldPath, newPath: newPath, indexPath: indexPath, stripCR: strings.HasSuffix(rawLine, "\r")})
			oldPath, indexPath = "", ""
			needsHeaders = false
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			if len(files) == 0 || (bounded && needsHeaders) {
				return nil, fmt.Errorf("patch contains a hunk without file headers")
			}
			var err error
			oldRemaining, newRemaining, err = unifiedHunkCounts(line)
			if err != nil {
				return nil, err
			}
			file := &files[len(files)-1]
			if !file.hasHunk {
				file.creates = oldRemaining == 0
			}
			file.hasHunk = true
			continue
		}
		if strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to ") || strings.HasPrefix(line, "copy from ") || strings.HasPrefix(line, "copy to ") {
			return nil, fmt.Errorf("git rename and copy metadata are not supported by apply_patch")
		}
		if strings.HasPrefix(line, "--- ") {
			var ok bool
			oldPath, ok = patchHeaderPath(line)
			if !ok {
				return nil, fmt.Errorf("patch contains a malformed file header")
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			return nil, fmt.Errorf("patch contains an unpaired unified diff file header")
		}
		if strings.HasPrefix(line, "diff ") {
			needsHeaders = true
			continue
		}
		if strings.HasPrefix(line, "Index: ") {
			needsHeaders = true
			indexPath, _ = patchHeaderPath("--- " + strings.TrimPrefix(line, "Index: "))
			if len(files) > 0 && !files[len(files)-1].hasHunk {
				file := &files[len(files)-1]
				file.indexPath, file.stripCR = indexPath, strings.HasSuffix(rawLine, "\r")
				needsHeaders = false
				indexPath = ""
			}
			continue
		}
		switch {
		case line == "", line == "-- ", strings.HasPrefix(line, "index "), strings.HasPrefix(line, "==="):
		case strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "), strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "):
			mode := line[strings.LastIndexByte(line, ' ')+1:]
			if bounded && (len(mode) != 6 || !strings.HasPrefix(mode, "100")) {
				return nil, fmt.Errorf("file size policy requires regular patch targets")
			}
		default:
			if bounded {
				return nil, fmt.Errorf("file size policy requires standard unified diff without extra patch commands")
			}
		}
	}
	if oldRemaining != 0 || newRemaining != 0 {
		return nil, fmt.Errorf("patch contains a truncated unified diff hunk")
	}
	if oldPath != "" {
		return nil, fmt.Errorf("patch contains unpaired unified diff file headers")
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("patch must contain paired unified diff file headers")
	}
	return files, nil
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
	if start, err := strconv.Atoi(rangeParts[0]); err != nil || start < 0 {
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
