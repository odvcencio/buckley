package tool

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func BenchmarkToModelOutput_PayloadSize(b *testing.B) {
	b.Cleanup(func() { SetResultEncoding(true) })
	for _, useToon := range []bool{false, true} {
		for _, size := range []int{2048, 20 << 10, 1 << 20} {
			b.Run(fmt.Sprintf("toon=%t/bytes=%d", useToon, size), func(b *testing.B) {
				SetResultEncoding(useToon)
				r := &builtin.Result{Success: true, Data: map[string]any{"output": strings.Repeat("x", size), "count": 3}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					s, err := ToModelOutput(r)
					if err != nil || len(s) > DefaultModelOutputBytes {
						b.Fatalf("encoding: %d bytes %v", len(s), err)
					}
				}
			})
		}
	}
}
