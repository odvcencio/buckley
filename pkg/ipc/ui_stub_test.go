//go:build !webui

package ipc

import "testing"

func TestGetEmbeddedUIReturnsErrorWithoutWebUITag(t *testing.T) {
	fsys, err := GetEmbeddedUI()
	if err == nil {
		t.Fatalf("expected GetEmbeddedUI to fail without the webui build tag")
	}
	if fsys != nil {
		t.Fatalf("expected nil filesystem, got %v", fsys)
	}
}
