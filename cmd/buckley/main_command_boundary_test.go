package main

import (
	"reflect"
	"testing"
)

func TestParseStartupOptions_PreservesWrappedVerificationCommand(t *testing.T) {
	t.Setenv("BUCKLEY_QUIET", "0")
	want := []string{"verify", "record", "--paths", "**", "--", "sh", "-c", "printf proof", "--quiet", "--config=child.yaml"}
	opts, err := parseStartupOptions(append([]string{"--config", "review.yaml"}, want...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.args, want) || opts.configPath != "review.yaml" || opts.quiet {
		t.Fatalf("wrapped command changed: args=%q config=%q quiet=%v", opts.args, opts.configPath, opts.quiet)
	}
}
