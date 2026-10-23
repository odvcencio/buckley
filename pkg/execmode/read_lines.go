package execmode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
)

const maxLineReadScanBytes = 8 << 20

func (b *Broker) filesReadLines(ctx context.Context, params map[string]any) (any, error) {
	startText, _ := params["start_line"].(string)
	endText, _ := params["end_line"].(string)
	start, startErr := strconv.Atoi(startText)
	end, endErr := strconv.Atoi(endText)
	if startErr != nil || endErr != nil || start < 1 || end < start {
		return nil, fmt.Errorf("files.read lines require decimal strings with 1 <= start_line <= end_line")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, _ := params["path"].(string)
	f, info, err := b.openReadFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := io.NewSectionReader(f, 0, min(info.Size(), maxLineReadScanBytes+1))
	data, next, err := readLineRange(ctx, reader, start, end)
	if err != nil {
		return nil, err
	}
	return map[string]any{"data": data, "next_line": next}, nil
}

// readLineRange keeps whole lines in each bounded page. Oversized lines and
// scan exhaustion fail explicitly so callers can switch to byte chunks.
func readLineRange(ctx context.Context, input io.Reader, start, end int) ([]byte, int, error) {
	reader := bufio.NewReaderSize(input, 32<<10)
	data := make([]byte, 0)
	line, lineStart, scanned := 1, 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		part, err := reader.ReadSlice('\n')
		scanned += len(part)
		if scanned > maxLineReadScanBytes {
			return nil, 0, fmt.Errorf("files.read lines exceeded the %d byte scan budget; use ReadFileChunk", maxLineReadScanBytes)
		}
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return nil, 0, err
		}
		if line >= start {
			if len(data)+len(part) > maxReadBytes {
				if lineStart == 0 {
					return nil, 0, fmt.Errorf("files.read line %d exceeds %d bytes; use ReadFileChunk", line, maxReadBytes)
				}
				return data[:lineStart], line, nil
			}
			data = append(data, part...)
		}
		if err == io.EOF {
			return data, 0, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if line == end {
			return data, 0, nil
		}
		line++
		lineStart = len(data)
	}
}
