package artifactv1

import (
	"context"
	"testing"
)

func BenchmarkRenderArtifactV1JSON(b *testing.B) {
	artifact := fullArtifact()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RenderJSON(artifact); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeArtifactV1(b *testing.B) {
	raw, err := RenderJSON(fullArtifact())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := DecodeProviderOutput(context.Background(), raw, OutputNativeJSONSchema, DecodeOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
