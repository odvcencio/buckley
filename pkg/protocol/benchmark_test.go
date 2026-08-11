package protocol

import (
	"testing"

	"m31labs.dev/buckley/pkg/rules"
)

func BenchmarkCompilerCompilePinnedProfile(b *testing.B) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		b.Fatal(err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic, AutoCodeMode: true, MaxFanout: 4})
	profile := testProfile(ClassFrontier)
	profile.Capabilities.ParallelToolCalls = true
	profile.Capabilities.Continuation = true
	profile.Capabilities.CodeMode = true
	profile.Metrics.ParallelCallReliability = 0.95
	profile.Metrics.ContinuationReliability = 0.95
	request := TaskRequest{Phase: "execution", TaskClass: "refactor", Risk: "medium", Parallelizable: true, NeedsArtifact: true, CandidateTools: []string{"read_file", "find_files", "exec_program", "run_tests", "write_file", "git_diff"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := compiler.Compile(request, profile); err != nil {
			b.Fatal(err)
		}
	}
}
