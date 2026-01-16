package tui

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/compositor"
)

func TestStyleCache_GetCaches(t *testing.T) {
	cache := NewStyleCache()
	style := compositor.DefaultStyle().WithFG(compositor.RGB(10, 20, 30))

	first := cache.Get(style)
	second := cache.Get(style)
	if first != second {
		t.Fatal("expected cached style to match")
	}
	if len(cache.cache) != 1 {
		t.Fatalf("expected cache size 1, got %d", len(cache.cache))
	}
}

func TestStyleCache_Clear(t *testing.T) {
	cache := NewStyleCache()
	style := compositor.DefaultStyle().WithFG(compositor.RGB(5, 6, 7))

	_ = cache.Get(style)
	cache.Clear()
	if len(cache.cache) != 0 {
		t.Fatalf("expected cache to be empty, got %d", len(cache.cache))
	}
}
