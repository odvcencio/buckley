package commiteval

import (
	"testing"

	"m31labs.dev/buckley/pkg/rules"
)

func TestEmbeddedCasesCoverRequiredKinds(t *testing.T) {
	cases, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("cases = %d, want at least 30", len(cases))
	}
	kinds := map[string]int{}
	for _, c := range cases {
		kinds[c.Kind]++
	}
	for _, want := range []string{"rename", "bundle", "release", "feature"} {
		if kinds[want] == 0 {
			t.Errorf("no %q case", want)
		}
	}
	planted := 0
	for _, c := range cases {
		if len(c.Planted) > 0 && len(c.Leaky) > 0 {
			planted++
		}
	}
	if planted < 3 {
		t.Errorf("planted-name rename cases = %d, want at least 3", planted)
	}
}

// TestOfflineGate is the CI gate: every hard check on every case must pass,
// with the default embedded rules and with no rules engine.
func TestOfflineGate(t *testing.T) {
	cases, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	for name, eng := range map[string]*rules.Engine{"engine": engine, "no engine": nil} {
		t.Run(name, func(t *testing.T) {
			rep := RunOffline(cases, eng)
			for _, res := range rep.Results {
				for _, c := range res.Failed() {
					t.Errorf("%s: %s failed: %s", res.Case.Name, c.Name, c.Detail)
				}
			}
			if rep.Checks() < len(cases)*2 {
				t.Fatalf("only %d checks ran for %d cases", rep.Checks(), len(cases))
			}
		})
	}
}
