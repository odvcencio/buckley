package transparency

import (
	"math"
	"testing"
)

func TestCalculatePricesReportedCacheReadsAndWrites(t *testing.T) {
	reads := 600_000
	tokens := TokenUsage{Input: 1_000_000, Output: 100_000, ReportedCachedInput: &reads, ReportedCacheWrite: 300_000}
	// Claude Fable 5.2: $8 in, $40 out, $0.20 read, $10 write.
	pricing := ModelPricing{InputPerMillion: 8, OutputPerMillion: 40, CachedInputPerMillion: 0.2, CacheWritePerMillion: 10}
	if CostUnknownForUsage(tokens, pricing) {
		t.Fatal("cache usage with cache prices was marked unknown")
	}
	// 100k plain at $8, 600k reads at $0.20, 300k writes at $10, 100k out at $40.
	if got, want := pricing.Calculate(tokens), 0.8+0.12+3+4; math.Abs(got-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	// Without a write price the cost cannot be confirmed.
	pricing.CacheWritePerMillion = 0
	if !CostUnknownForUsage(tokens, pricing) {
		t.Fatal("a cache write without a write price was priced")
	}
}
