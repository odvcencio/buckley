package toolrunner

import (
	"fmt"
	"hash/fnv"
	"io"
)

type toolResultDeduper struct {
	seen map[uint64]string
}

func newToolResultDeduper() *toolResultDeduper {
	return &toolResultDeduper{seen: make(map[uint64]string)}
}

func (d *toolResultDeduper) messageFor(record ToolCallRecord) string {
	if d == nil {
		return record.Result
	}
	if record.Result == "" {
		return record.Result
	}
	key := hashToolResult(record.Name, record.Result)
	if key == 0 {
		return record.Result
	}
	if prev, ok := d.seen[key]; ok && prev != "" && prev != record.ID {
		return fmt.Sprintf("[deduplicated tool result; same as tool call %s]", prev)
	}
	if record.ID != "" {
		d.seen[key] = record.ID
	} else {
		d.seen[key] = "previous"
	}
	return record.Result
}

func hashToolResult(name, result string) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, name)
	_, _ = io.WriteString(h, "\n")
	_, _ = io.WriteString(h, result)
	return h.Sum64()
}
