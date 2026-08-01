package tool

import "testing"

func TestEnableConfiguredHooksDisabledIsNoOp(t *testing.T) {
	r := NewRegistry()
	closer, err := r.EnableConfiguredHooks(false, 0)
	if err != nil || closer != nil {
		t.Fatalf("disabled hooks must be a no-op, got closer=%v err=%v", closer, err)
	}
}

func TestEnableConfiguredHooksNoManifestsIsNoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	r := NewRegistry()
	closer, err := r.EnableConfiguredHooks(true, 0)
	if err != nil || closer != nil {
		t.Fatalf("no hook manifests must be a no-op, got closer=%v err=%v", closer, err)
	}
}
