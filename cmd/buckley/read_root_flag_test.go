package main

import (
	"reflect"
	"testing"
)

func TestParseStartupOptions_ReadRootIsRepeatable(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--model", "x", "--read-root", "~/a", "--read-root=/b", "-p", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"~/a", "/b"}; !reflect.DeepEqual(opts.readRoots, want) {
		t.Fatalf("read roots = %v, want %v", opts.readRoots, want)
	}
	if opts.prompt != "hello" {
		t.Fatalf("prompt = %q", opts.prompt)
	}
	if _, err := parseStartupOptions([]string{"--read-root"}); err == nil {
		t.Fatal("--read-root without a path was accepted")
	}
}
