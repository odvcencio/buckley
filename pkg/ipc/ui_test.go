package ipc

import (
	"bytes"
	"io"
	"testing"
)

func TestGetEmbeddedUIContainsIndex(t *testing.T) {
	fsys, err := GetEmbeddedUI()
	if err != nil {
		t.Fatalf("GetEmbeddedUI: %v", err)
	}

	f, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("open index.html: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !bytes.Contains(bytes.ToLower(data), []byte("<!doctype html>")) {
		t.Fatalf("expected embedded index.html to contain doctype")
	}
}
