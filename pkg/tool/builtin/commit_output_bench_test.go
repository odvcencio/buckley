package builtin

import (
	"encoding/json"
	"testing"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func BenchmarkCommitChangesTool_Output(b *testing.B) {
	helper := newFakeCommitHelper(b)
	for _, mode := range []string{"spam", "spam-fail"} {
		b.Run(mode, func(b *testing.B) {
			tool := &CommitChangesTool{Executable: helper}
			tool.SetWorkDir(b.TempDir())
			tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": mode})
			params := map[string]any{"paths": []any{"selected.txt"}}
			result, err := tool.Execute(params)
			if err != nil || result.Success != (mode == "spam") {
				b.Fatalf("warm execution: %v, %+v", err, result)
			}
			jsonResult, _ := json.Marshal(result)
			toonResult, _ := toon.New(true).MarshalString(result)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(params)
				if err != nil || result.Success != (mode == "spam") {
					b.Fatalf("execute: %v, %+v", err, result)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(len(jsonResult)), "json_bytes/result")
			b.ReportMetric(float64(len(toonResult)), "toon_bytes/result")
		})
	}
}
