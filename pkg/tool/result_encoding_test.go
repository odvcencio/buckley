package tool

import (
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestSetResultEncoding_ConcurrentChangesPreserveCompletePayloads(t *testing.T) {
	t.Cleanup(func() { SetResultEncoding(true) })
	result := &builtin.Result{Success: true, Data: map[string]any{"output": strings.Repeat("Observed \\\"result\\\" 🎉\n", 2048)}}
	cases := []struct {
		name   string
		encode func() (string, error)
		want   map[string]bool
	}{
		{"full result", func() (string, error) { return ToJSON(result) }, map[string]bool{}},
		{"model result", func() (string, error) { return ToModelOutputWithLimit(result, 0) }, map[string]bool{}},
		{"bounded model result", func() (string, error) { return ToModelOutputWithLimit(result, 2048) }, map[string]bool{}},
	}
	for _, useToon := range []bool{false, true} {
		SetResultEncoding(useToon)
		for _, tc := range cases {
			output, err := tc.encode()
			if err != nil {
				t.Fatal(err)
			}
			tc.want[output] = true
		}
	}
	start, stop := make(chan struct{}), make(chan struct{})
	var readers, writer sync.WaitGroup
	writer.Go(func() {
		<-start
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				SetResultEncoding(i%2 == 0)
			}
		}
	})
	for range 4 {
		readers.Go(func() {
			<-start
			for range 32 {
				for _, tc := range cases {
					output, err := tc.encode()
					if err != nil || !tc.want[output] {
						t.Errorf("%s returned mixed encoding or an error: bytes=%d error=%v", tc.name, len(output), err)
						return
					}
				}
			}
		})
	}
	close(start)
	readers.Wait()
	close(stop)
	writer.Wait()
}

func BenchmarkResultEncoding(b *testing.B) {
	b.Cleanup(func() { SetResultEncoding(true) })
	b.Run("toggle", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			SetResultEncoding(i%2 == 0)
		}
	})
	for _, useToon := range []bool{false, true} {
		name := "json"
		if useToon {
			name = "toon"
		}
		b.Run(name, func(b *testing.B) {
			SetResultEncoding(useToon)
			result := &builtin.Result{Success: true, Data: map[string]any{"passed": 3, "output": "Tests passed."}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ToModelOutput(result); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
