package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestParseReviewPRCommandOptions_ValueFlagsAroundReference(t *testing.T) {
	for _, scenario := range []struct{ name, value string }{
		{"model", "fixture/model"},
		{"effort", "low"},
		{"critic-model", "fixture/critic"},
		{"timeout", "2m"},
		{"output", "review.md"},
		{"budget", "0.25"},
		{"max-turns", "4"},
		{"max-tool-calls", "8"},
		{"max-diff-bytes", "4096"},
		{"require-verification", "receipt"},
		{"evidence-timeout", "30s"},
		{"max-context-tokens", "8192"},
		{"max-validation-attempts", "2"},
		{"depth", "balanced"},
		{"shards", "2"},
		{"concurrency", "2"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			want, err := parseReviewPRCommandOptions([]string{"--verbose", "--cost=false", "123", "--" + scenario.name + "=" + scenario.value})
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"--verbose", "--" + scenario.name, scenario.value, "123", "--cost=false"},
				{"--verbose", "123", "--" + scenario.name, scenario.value, "--cost=false"},
			} {
				got, err := parseReviewPRCommandOptions(args)
				if err != nil {
					t.Fatalf("parse %v: %v", args, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("parse %v = %+v, want %+v", args, got, want)
				}
			}
		})
	}
}

func TestParseReviewPRCommandOptions_CombinedEvidenceFlags(t *testing.T) {
	got, err := parseReviewPRCommandOptions([]string{"123", "--require-verification", "receipt", "--evidence-timeout", "30s", "--run-verification", "--post=false"})
	if err != nil {
		t.Fatal(err)
	}
	if got.prRef != "123" || got.requireVerification != "receipt" || got.evidenceTimeout != 30*time.Second || !got.runVerification || got.post {
		t.Fatalf("evidence options = %+v", got)
	}
}

func TestInterspersedReviewPRArgs_UsesRegisteredFlagTypes(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	value := fs.String("future-option", "", "a newly registered value flag")
	boolean := fs.Bool("toggle", false, "a boolean flag")
	if err := fs.Parse(interspersedReviewPRArgs(fs, []string{"123", "--future-option", "-literal-value", "--toggle"})); err != nil {
		t.Fatal(err)
	}
	if *value != "-literal-value" || !*boolean || !reflect.DeepEqual(fs.Args(), []string{"123"}) {
		t.Fatalf("value=%q boolean=%v args=%v", *value, *boolean, fs.Args())
	}
}

func TestParseReviewPRCommandOptions_InvalidValueFlags(t *testing.T) {
	for _, args := range [][]string{
		{"123", "--evidence-timeout", "invalid"},
		{"123", "--evidence-timeout"},
		{"123", "--unregistered", "value"},
		{"123", "456", "--require-verification", "receipt"},
	} {
		if _, err := parseReviewPRCommandOptions(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
