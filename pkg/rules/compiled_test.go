package rules

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/arbiter"
	"m31labs.dev/arbiter/bundle"
)

var updateCompiledRules = flag.Bool("update-compiled-rules", false, "regenerate embedded compiled rule defaults")

func TestCompiledDefaults_MatchSources(t *testing.T) {
	loader := NewLoader("")
	domains, err := loader.Domains()
	if err != nil {
		t.Fatal(err)
	}
	archive := compiledRuleArchive{Version: compiledRulesVersion, Domains: make(map[string]compiledRuleDomain, len(domains))}
	for _, domain := range domains {
		src, err := loader.Load(domain)
		if err != nil {
			t.Fatal(err)
		}
		program, err := arbiter.Compile(src)
		if err != nil {
			t.Fatalf("compile %s: %v", domain, err)
		}
		if len(program.Workers) > 0 || len(program.Arbiters) > 0 || program.Expert != nil {
			t.Fatalf("domain %s has runtime artifacts not supported by compiled defaults", domain)
		}
		ruleset, err := bundle.Marshal(program.Ruleset)
		if err != nil {
			t.Fatal(err)
		}
		cached := compiledRuleDomain{SourceSHA256: fmt.Sprintf("%x", sha256.Sum256(src)), Ruleset: ruleset, IR: program.IR, Warnings: program.Warnings}
		for _, segment := range program.Segments.All() {
			ruleset, err := bundle.Marshal(segment.Ruleset)
			if err != nil {
				t.Fatal(err)
			}
			cached.Segments = append(cached.Segments, compiledRuleSegment{Name: segment.Name, Source: segment.Source, Ruleset: ruleset})
		}
		archive.Domains[domain] = cached
	}
	want, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if *updateCompiledRules {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(want); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(compiledRulesPath, compressed.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("generated %d domains, %d compressed bytes", len(domains), compressed.Len())
		return
	}
	data, err := defaultRules.ReadFile(compiledRulesPath)
	if err != nil {
		t.Fatalf("read compiled defaults: %v; run go generate ./pkg/rules", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("compiled rules differ from sources or compiler; run go generate ./pkg/rules")
	}
}

func TestCompiledDefaults_ReloadIsIsolated(t *testing.T) {
	dir := t.TempDir()
	a, err := NewEngine(WithUserOverrides(dir))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	override := strings.ReplaceAll(validArbOverride, "Direct", "Custom")
	if err := os.WriteFile(filepath.Join(dir, "complexity.arb"), []byte(override), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload("complexity"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		engine *Engine
		want   string
	}{{a, "Custom"}, {b, "Direct"}} {
		got, err := Eval(tc.engine, "complexity", TaskFacts{WordCount: 3})
		if err != nil || len(got) == 0 || got[0].Action != tc.want {
			t.Fatalf("reloaded engines lost independence: %v %v, want %s", got, err, tc.want)
		}
	}
}

func TestCompiledDefaults_PreserveGovernedRulesAndStrategies(t *testing.T) {
	source := []byte(`
segment vip { user.tier == "gold" }
rule VIPOffer {
 when segment vip { user.cart > 50 }
 then Offer { discount: 20 }
}
outcome Path { target: string }
strategy Route returns Path {
 when { user.country == "US" } then Domestic { target: "domestic" }
 else Global { target: "global" }
}
`)
	original, err := arbiter.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := bundle.Marshal(original.Ruleset)
	if err != nil {
		t.Fatal(err)
	}
	domain := compiledRuleDomain{Ruleset: ruleset, IR: original.IR}
	for _, segment := range original.Segments.All() {
		ruleset, err := bundle.Marshal(segment.Ruleset)
		if err != nil {
			t.Fatal(err)
		}
		domain.Segments = append(domain.Segments, compiledRuleSegment{Name: segment.Name, Source: segment.Source, Ruleset: ruleset})
	}
	encoded, err := json.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	var decoded compiledRuleDomain
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restored, err := decoded.program()
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{"gold", "standard"} {
		for _, country := range []string{"US", "GB"} {
			ctx := map[string]any{"user": map[string]any{"tier": tier, "cart": 100, "country": country}}
			want, _, err := arbiter.EvalGoverned(original, arbiter.DataFromMap(ctx, original), original.Segments, ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := arbiter.EvalGoverned(restored, arbiter.DataFromMap(ctx, restored), restored.Segments, ctx)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("governed result changed for %v: got %v, want %v, err %v", ctx, got, want, err)
			}
			if (len(got) > 0) != (tier == "gold") {
				t.Fatalf("segment decision changed: %v %v", tier, got)
			}
			before, err := original.Strategies.Evaluate("Route", ctx)
			if err != nil {
				t.Fatal(err)
			}
			after, err := restored.Strategies.Evaluate("Route", ctx)
			if err != nil || before.Outcome != after.Outcome || before.Selected != after.Selected || !reflect.DeepEqual(before.Params, after.Params) {
				t.Fatalf("strategy decision changed: %+v %+v %v", before, after, err)
			}
		}
	}
}

func TestEngine_InvalidOverrideFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "complexity.arb"), []byte(invalidArbContent), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEngine(WithUserOverrides(dir)); err == nil {
		t.Fatal("malformed override silently fell back to compiled defaults")
	}
}
