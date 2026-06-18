package widgets

import "testing"

func TestBase_Focus(t *testing.T) {
	var b Base

	if b.IsFocused() {
		t.Fatal("new base should not be focused")
	}

	b.Focus()
	if !b.IsFocused() {
		t.Fatal("base should be focused after Focus")
	}

	b.Blur()
	if b.IsFocused() {
		t.Fatal("base should not be focused after Blur")
	}
}

func TestFocusableBase_CanFocus(t *testing.T) {
	var b Base
	var fb FocusableBase

	if b.CanFocus() {
		t.Fatal("base should not be focusable")
	}
	if !fb.CanFocus() {
		t.Fatal("focusable base should be focusable")
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxWidth int
		want     string
	}{
		{input: "Hello", maxWidth: 10, want: "Hello"},
		{input: "Hello World", maxWidth: 8, want: "Hello..."},
		{input: "Hi", maxWidth: 2, want: "Hi"},
		{input: "Hello", maxWidth: 3, want: "Hel"},
	}

	for _, tt := range tests {
		if got := truncateString(tt.input, tt.maxWidth); got != tt.want {
			t.Fatalf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
		}
	}
}

func TestMax(t *testing.T) {
	if got := max(1, 3); got != 3 {
		t.Fatalf("max(1, 3) = %d", got)
	}
	if got := max(4, 2); got != 4 {
		t.Fatalf("max(4, 2) = %d", got)
	}
}
