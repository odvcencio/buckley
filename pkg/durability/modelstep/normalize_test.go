package modelstep

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
)

func legacyNormalizeErrorText(text string) string {
	text = strings.TrimSpace(string(evidence.Redact([]byte(text))))
	runes := []rune(text)
	if len(runes) <= MaxPersistedErrorRunes {
		return text
	}
	return string(runes[:MaxPersistedErrorRunes-1]) + "…"
}

func TestNormalizeErrorText_LegacyParity(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"whitespace", " \t\n\u2003"},
		{"short", " \tprovider failed\n"},
		{"below limit", strings.Repeat("a", MaxPersistedErrorRunes-1)},
		{"at limit", strings.Repeat("a", MaxPersistedErrorRunes)},
		{"over limit", strings.Repeat("a", MaxPersistedErrorRunes+1)},
		{"unicode at limit", strings.Repeat("🌳", MaxPersistedErrorRunes)},
		{"unicode over limit", strings.Repeat("🌳", MaxPersistedErrorRunes+1)},
		{"invalid below limit", strings.Repeat("\xff\xfe", 100)},
		{"invalid at limit", strings.Repeat("\xff\xfe", MaxPersistedErrorRunes/2)},
		{"invalid over limit", strings.Repeat("\xff\xfe", MaxPersistedErrorRunes)},
		{"invalid at cutoff", strings.Repeat("x", MaxPersistedErrorRunes-2) + "\xff\xfeafter"},
		{"redaction before clipping", strings.Repeat("x ", MaxPersistedErrorRunes/2-5) + "sk-" + strings.Repeat("a", 100)},
		{"redaction shortens below limit", strings.Repeat("sk-"+strings.Repeat("a", 100)+"\n", 30)},
		{"large", strings.Repeat("diagnostic 🌳\xff\n", 100000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := NormalizeErrorText(tc.text), legacyNormalizeErrorText(tc.text); got != want {
				t.Fatalf("normalization differs: got %q, want %q", got, want)
			}
		})
	}
}

func FuzzNormalizeErrorText_LegacyParity(f *testing.F) {
	for _, seed := range []string{"", "failed", strings.Repeat("🌳\xff\xfe ", 1024), strings.Repeat("sk-"+strings.Repeat("a", 40)+"\n", 100)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if got, want := NormalizeErrorText(text), legacyNormalizeErrorText(text); got != want {
			t.Fatal("normalization changed legacy output")
		}
	})
}

func BenchmarkNormalizeErrorText(b *testing.B) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"short", "provider timed out"},
		{"at_limit", strings.Repeat("x", MaxPersistedErrorRunes)},
		{"plain_32KiB", strings.Repeat("diagnostic line\n", 2048)},
		{"secret_32KiB", strings.Repeat("failed sk-abcdefghijklmnopqrstuvwxyz123456\n", 800)},
		{"unicode_32KiB", strings.Repeat("🌳", 8192)},
		{"invalid_32KiB", strings.Repeat("\xff\xfe", 16384)},
		{"plain_1MiB", strings.Repeat("x", 1<<20)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.text)))
			for range b.N {
				if NormalizeErrorText(tc.text) == "" {
					b.Fatal("empty normalized error")
				}
			}
		})
	}
}
