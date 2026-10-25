package skill

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type failingActivationPersistence struct {
	saveErr, deactivateErr error
	saves, deactivations   int
}

func (s *failingActivationPersistence) SaveSessionSkill(_, _, _, _ string) error {
	s.saves++
	return s.saveErr
}

func (s *failingActivationPersistence) DeactivateSessionSkill(_, _ string) error {
	s.deactivations++
	return s.deactivateErr
}

func TestRegistry_PersistenceFailurePreservesActivationState(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Skill{Name: "restricted", Description: "Restricted work", AllowedTools: []string{"read_file"}, Content: "keep these instructions"}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("storage unavailable")
	store := &failingActivationPersistence{saveErr: failure}
	r.SetPersistence("session", store)
	if err := r.Activate("restricted", "test", "model"); !errors.Is(err, failure) {
		t.Fatalf("activate error=%v", err)
	}
	if r.IsActive("restricted") || r.ToolFilter() != nil || r.ActiveInstructions(nil) != "" {
		t.Fatal("failed persistence published active state")
	}
	store.saveErr = nil
	if err := r.Activate("restricted", "test", "model"); err != nil {
		t.Fatal(err)
	}
	store.deactivateErr = failure
	if err := r.Deactivate("restricted"); !errors.Is(err, failure) {
		t.Fatalf("deactivate error=%v", err)
	}
	if !r.IsActive("restricted") || !reflect.DeepEqual(r.ToolFilter(), []string{"read_file"}) || !strings.Contains(r.ActiveInstructions(nil), "keep these instructions") {
		t.Fatal("failed persistence lost restrictions or instructions")
	}
	store.deactivateErr = nil
	if err := r.Deactivate("restricted"); err != nil {
		t.Fatal(err)
	}
	if r.IsActive("restricted") || r.ToolFilter() != nil || r.ActiveInstructions(nil) != "" {
		t.Fatal("successful deactivation retained active state")
	}
	if store.saves != 2 || store.deactivations != 2 {
		t.Fatalf("persistence calls: %+v", store)
	}
}

func TestRegistry_ActiveToolFilterIntersectsRestrictions(t *testing.T) {
	r := NewRegistry()
	for _, s := range []*Skill{
		{Name: "a", Description: "A", AllowedTools: []string{"read_file", "run_tests"}},
		{Name: "b", Description: "B", AllowedTools: []string{"read_file", "list_directory"}},
		{Name: "unrestricted", Description: "Guidance only"},
		{Name: "disjoint", Description: "No overlap", AllowedTools: []string{"write_file"}},
	} {
		if err := r.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		name string
		want []string
	}{
		{"a", []string{"read_file", "run_tests"}},
		{"b", []string{"read_file"}},
		{"unrestricted", []string{"read_file"}},
		{"disjoint", []string{}},
	} {
		if err := r.Activate(step.name, "test", "model"); err != nil {
			t.Fatal(err)
		}
		if got := r.ToolFilter(); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("after %s: filter=%v want=%v", step.name, got, step.want)
		}
	}
}
