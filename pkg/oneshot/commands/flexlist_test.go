package commands

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBulletList_UnmarshalArray(t *testing.T) {
	var b bulletList
	if err := json.Unmarshal([]byte(`["one","two"]`), &b); err != nil {
		t.Fatalf("unmarshal array: %v", err)
	}
	if !reflect.DeepEqual([]string(b), []string{"one", "two"}) {
		t.Fatalf("got %v", b)
	}
}

func TestBulletList_UnmarshalFlattenedString(t *testing.T) {
	var b bulletList
	in := `"- added sky wire IR\n- flipped IBL cell\n\n* rebuilt bundles"`
	if err := json.Unmarshal([]byte(in), &b); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	want := []string{"added sky wire IR", "flipped IBL cell", "rebuilt bundles"}
	if !reflect.DeepEqual([]string(b), want) {
		t.Fatalf("got %v, want %v", b, want)
	}
}

func TestBulletList_UnmarshalSingleLineString(t *testing.T) {
	var b bulletList
	if err := json.Unmarshal([]byte(`"one change"`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual([]string(b), []string{"one change"}) {
		t.Fatalf("got %v", b)
	}
}

func TestBulletList_UnmarshalInvalidType(t *testing.T) {
	var b bulletList
	if err := json.Unmarshal([]byte(`42`), &b); err == nil {
		t.Fatal("expected error for number")
	}
}

func TestPRDefinition_ValidateAcceptsFlattenedChanges(t *testing.T) {
	raw := []byte(`{"action":"fix","title":"tolerate flattened lists","summary":"s","changes":"- a\n- b"}`)
	if err := (PRDefinition{}).Validate(raw); err != nil {
		t.Fatalf("validate: %v", err)
	}
	got, err := (PRDefinition{}).Unmarshal(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pr := got.(*PRResult)
	if len(pr.Changes) != 2 {
		t.Fatalf("changes = %v", pr.Changes)
	}
}
