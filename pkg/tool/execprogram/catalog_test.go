package execprogram

import (
	"reflect"
	"strings"
	"testing"
)

func TestProgramToolParameters_AvailableDialects(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		fw        bool
		languages []string
	}{
		{"Go only", false, []string{"go"}},
		{"optional FW installed", true, []string{"go", "fw"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			program := &ProgramTool{fwAvailable: scenario.fw}
			if got := program.Parameters().Properties["language"].Enum; !reflect.DeepEqual(got, scenario.languages) {
				t.Fatalf("advertised languages = %v, want %v", got, scenario.languages)
			}
			if got := strings.Contains(program.Description(), `Set language to "fw"`); got != scenario.fw {
				t.Fatalf("description advertises FW = %v, installed = %v", got, scenario.fw)
			}
		})
	}
}
