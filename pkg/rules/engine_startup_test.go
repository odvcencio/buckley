package rules

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestEngine_ColdStartAllocationBudget(t *testing.T) {
	const helperEnv = "BUCKLEY_RULES_COLD_START_HELPER"
	if os.Getenv(helperEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEngine_ColdStartAllocationBudget$", "-test.v")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cold rule engine startup: %v\n%s", err, out)
		}
		t.Logf("%s", out)
		return
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	engine, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(engine)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("cold engine: %d domains, %d allocated bytes", len(engine.Domains()), allocated)
	const maxAllocated = 64 << 20
	if allocated > maxAllocated {
		t.Fatalf("cold rule engine allocated %d bytes, budget is %d; check for runtime grammar generation", allocated, maxAllocated)
	}
}
