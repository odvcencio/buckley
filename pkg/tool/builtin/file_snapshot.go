package builtin

import (
	"io"
	"math"
	"os"
	"strings"
)

func readFileSnapshot(path string, limit int64) (content string, size int64, exceeded bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, false, err
	}
	size = info.Size()
	if size > limit {
		return "", size, true, nil
	}
	content, exceeded, err = readSnapshotString(file, size, limit)
	if exceeded {
		if current, statErr := file.Stat(); statErr == nil {
			size = current.Size()
		}
	}
	return content, size, exceeded, err
}

// The reported size is only an allocation hint; the read itself enforces the
// limit for growing files and virtual files that report zero bytes.
func readSnapshotString(file *os.File, size, limit int64) (string, bool, error) {
	limit = min(limit, int64(math.MaxInt))
	var buffer strings.Builder
	if size > 0 {
		buffer.Grow(int(min(size, limit)))
	}
	remaining := limit
	var small [4096]byte
	scratch := small[:]
	if size > int64(len(small)) {
		var large [32 << 10]byte
		scratch = large[:]
	}
	for remaining > 0 {
		n, err := file.Read(scratch[:int(min(int64(len(scratch)), remaining))])
		buffer.Write(scratch[:n])
		remaining -= int64(n)
		if err == io.EOF {
			return buffer.String(), false, nil
		}
		if err != nil {
			return "", false, err
		}
	}
	// Peek without adding one to a limit that can be MaxInt64.
	var extra [1]byte
	if n, err := file.Read(extra[:]); n > 0 {
		return "", true, nil
	} else if err != nil && err != io.EOF {
		return "", false, err
	}
	return buffer.String(), false, nil
}
