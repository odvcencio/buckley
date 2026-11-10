package embeddings

import (
	"math"
	"testing"
)

var benchmarkEmbedding []float64

func BenchmarkEmbeddingCache(b *testing.B) {
	cache := NewCache(b.TempDir())
	vector := make([]float64, 1536)
	for i := range vector {
		vector[i] = math.Sin(float64(i))
	}
	if err := cache.Set("vector", vector); err != nil {
		b.Fatal(err)
	}
	b.Run("read", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var ok bool
			benchmarkEmbedding, ok = cache.Get("vector")
			if !ok {
				b.Fatal("cache miss")
			}
		}
	})
	b.Run("write", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := cache.Set("vector", vector); err != nil {
				b.Fatal(err)
			}
		}
	})
}
