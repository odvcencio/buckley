package headless

import "testing"

func TestBuildToolRegistryNilConfigDoesNotPanic(t *testing.T) {
	r := &Registry{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("nil-config registry construction panicked: %v", rec)
		}
	}()
	tools, closer := r.buildToolRegistry("sess", "")
	if tools == nil {
		t.Fatal("expected a registry even with nil config")
	}
	if closer != nil {
		_ = closer.Close()
	}
}
