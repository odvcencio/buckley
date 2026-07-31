package commands

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bulletList is a []string that tolerates a model flattening the list into
// a single JSON string. Providers do not all hard-enforce array schemas
// (observed: generate_pull_request returning "changes" as prose), so the
// string form splits on newlines with bullet markers and blanks dropped.
type bulletList []string

func (b *bulletList) UnmarshalJSON(data []byte) error {
	var items []string
	if err := json.Unmarshal(data, &items); err == nil {
		*b = items
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("expected array of strings or string: %w", err)
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(trimBulletMarker(strings.TrimSpace(line)))
		if line != "" {
			out = append(out, line)
		}
	}
	*b = out
	return nil
}
