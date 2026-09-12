package main

import (
	"reflect"
	"testing"
)

func TestStartupPromptCommandBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		raw, wantArgs          []string
		wantPrompt, wantConfig string
	}{
		{name: "experiment documented flags", raw: []string{"experiment", "run", "probe", "-m", "openai_compatible/glm5.3flash", "-p", "read and summarize"}, wantArgs: []string{"experiment", "run", "probe", "-m", "openai_compatible/glm5.3flash", "-p", "read and summarize"}},
		{name: "global config and experiment prompt", raw: []string{"-c", "particle.yaml", "experiment", "run", "probe", "-p", "literal prompt"}, wantArgs: []string{"experiment", "run", "probe", "-p", "literal prompt"}, wantConfig: "particle.yaml"},
		{name: "repeated local prompt", raw: []string{"experiment", "run", "probe", "-p", "first", "-p", "second"}, wantArgs: []string{"experiment", "run", "probe", "-p", "first", "-p", "second"}},
		{name: "empty local value", raw: []string{"experiment", "run", "probe", "-p", ""}, wantArgs: []string{"experiment", "run", "probe", "-p", ""}},
		{name: "local missing value belongs to command", raw: []string{"experiment", "run", "probe", "-p"}, wantArgs: []string{"experiment", "run", "probe", "-p"}},
		{name: "one shot remains global", raw: []string{"-p", "inspect files", "-c", "particle.yaml"}, wantArgs: []string{}, wantPrompt: "inspect files", wantConfig: "particle.yaml"},
		{name: "global and local are distinct", raw: []string{"-p", "global", "experiment", "run", "probe", "-p", "local"}, wantArgs: []string{"experiment", "run", "probe", "-p", "local"}, wantPrompt: "global"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseStartupOptions(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(opts.args, tc.wantArgs) || opts.prompt != tc.wantPrompt || opts.configPath != tc.wantConfig {
				t.Fatalf("args=%q prompt=%q config=%q; want args=%q prompt=%q config=%q", opts.args, opts.prompt, opts.configPath, tc.wantArgs, tc.wantPrompt, tc.wantConfig)
			}
		})
	}
}
