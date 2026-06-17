package skill

import "testing"

func TestRuntimeStateToolFilterDistinguishesEmptyFromUnset(t *testing.T) {
	state := NewRuntimeState(nil)

	if got := state.ToolFilter(); got != nil {
		t.Fatalf("new state filter=%v, want nil", got)
	}

	state.SetToolFilter([]string{})
	got := state.ToolFilter()
	if got == nil {
		t.Fatal("empty filter should remain explicitly set")
	}
	if len(got) != 0 {
		t.Fatalf("empty filter len=%d, want 0", len(got))
	}

	state.SetToolFilter([]string{"read_file"})
	got = state.ToolFilter()
	if len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("filter=%v, want [read_file]", got)
	}

	state.ClearToolFilter()
	if got := state.ToolFilter(); got != nil {
		t.Fatalf("cleared filter=%v, want nil", got)
	}
}
