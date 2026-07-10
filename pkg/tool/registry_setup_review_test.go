package tool

import "testing"

func TestRunVerificationIsNotGloballyRegistered(t *testing.T) {
	registry := NewRegistry()
	if _, exists := registry.Get("run_verification"); exists {
		t.Fatal("snapshot-bound run_verification must be registered only by review registries")
	}
}
