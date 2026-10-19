package skill

import (
	"reflect"
	"testing"
)

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

func TestRuntimeState_ToolCeilingSurvivesSkillChanges(t *testing.T) {
	state := NewRuntimeState(nil)
	caller := []string{"read_file", "edit_file"}
	state.SetToolCeiling(caller)
	caller[0] = "run_shell"
	for _, tc := range []struct {
		name   string
		filter []string
		clear  bool
		want   []string
	}{
		{name: "caller before activation", clear: true, want: []string{"read_file", "edit_file"}},
		{name: "skill narrows", filter: []string{"read_file"}, want: []string{"read_file"}},
		{name: "skill cannot expand", filter: []string{"run_shell", "edit_file"}, want: []string{"edit_file"}},
		{name: "disjoint skill stays empty", filter: []string{"run_shell"}, want: []string{}},
		{name: "empty skill stays empty", filter: []string{}, want: []string{}},
		{name: "deactivation restores caller", clear: true, want: []string{"read_file", "edit_file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.clear {
				state.ClearToolFilter()
			} else {
				state.SetToolFilter(tc.filter)
			}
			got := state.ToolFilter()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("filter = %#v, want %#v", got, tc.want)
			}
			if len(got) > 0 {
				got[0] = "run_shell"
				if !reflect.DeepEqual(state.ToolFilter(), tc.want) {
					t.Fatal("returned filter aliases caller limit")
				}
			}
		})
	}
	state.SetToolCeiling([]string{})
	state.ClearToolFilter()
	if got := state.ToolFilter(); got == nil || len(got) != 0 {
		t.Fatalf("empty caller ceiling became unrestricted: %#v", got)
	}
	state.SetToolCeiling(nil)
	if got := state.ToolFilter(); got != nil {
		t.Fatalf("caller could not remove ceiling: %#v", got)
	}
}
