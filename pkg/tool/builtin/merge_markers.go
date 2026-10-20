package builtin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func parseConflictMarkerSize(path, output string) (int64, error) {
	fields := strings.Split(output, "\x00")
	if len(fields) != 4 || fields[0] != path || fields[1] != "conflict-marker-size" || fields[3] != "" {
		return 0, fmt.Errorf("invalid conflict-marker-size attributes from Git")
	}
	size, err := strconv.ParseInt(fields[2], 10, 32)
	if err != nil || size <= 0 {
		size = 7
	}
	return size, nil
}

// readConflictLine drains long lines without retaining them. The returned
// prefix may borrow the reader buffer until the next read.
func readConflictLine(ctx context.Context, r *bufio.Reader, size int64, prefixLimit int) (byte, []byte, int64, error) {
	var prefix []byte
	var total, run int64
	var first, kind byte
	decided := false
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, 0, err
		}
		part, err := r.ReadSlice('\n')
		if !decided {
			for _, b := range part {
				if run == 0 {
					first = b
					if b != '<' && b != '>' && b != '=' && b != '|' {
						decided = true
						break
					}
				}
				if run < size && b == first {
					run++
					continue
				}
				space := b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
				if run == size && space && ((first != '<' && first != '>') || b == ' ') {
					kind = first
				}
				decided = true
				break
			}
		}
		if total == 0 && err != bufio.ErrBufferFull && len(part) <= prefixLimit {
			if err == io.EOF && len(part) > 0 {
				err = nil
			}
			return kind, part, int64(len(part)), err
		}
		total += int64(len(part))
		if take := min(len(part), prefixLimit-len(prefix)); take > 0 {
			prefix = append(prefix, part[:take]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && total > 0 {
			err = nil
		}
		return kind, prefix, total, err
	}
}
