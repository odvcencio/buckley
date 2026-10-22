package oneshot

import (
	"strings"
	"testing"
)

func FuzzBoundedAgentEvidenceText(f *testing.F) {
	for _, value := range []string{
		"", " \t\r\n", "small evidence", strings.Repeat("a", 200), strings.Repeat("界", 200),
		strings.Repeat("a\xff\xfe界", 40), "\u00a0" + strings.Repeat("a", 200) + "\u3000",
		strings.Repeat("evidence \n", 2000),
	} {
		for _, limit := range []int{-1, 0, 1, 2, 3, 63, 64, 65, 66, 127, 200, 2000} {
			f.Add(value, limit)
		}
	}
	f.Fuzz(func(t *testing.T, value string, limit int) {
		text := strings.TrimSpace(value)
		runes := []rune(text)
		want := text
		if limit > 0 && len(runes) > limit {
			if limit < 64 {
				want = string(runes[:limit])
			} else {
				head := (limit - 64) / 2
				tail := limit - 64 - head
				want = string(runes[:head]) + "\n... durable evidence abridged ...\n" + string(runes[len(runes)-tail:])
			}
		}
		if got := boundedAgentEvidenceText(value, limit); got != want {
			t.Fatalf("boundedAgentEvidenceText(%q, %d) = %q, want %q", value, limit, got, want)
		}
	})
}
