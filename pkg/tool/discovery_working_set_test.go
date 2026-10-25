package tool

import (
	"reflect"
	"strings"
	"testing"
)

func TestSetDiscoveredTools_ReplacesSpecializedWorkingSet(t *testing.T) {
	registry := NewRegistry()
	registry.EnableDynamicDiscovery([]string{"read_file"})
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"git_log"}, "discover_tools,git_log,read_file"},
		{[]string{"git_blame"}, "discover_tools,git_blame,read_file"},
		{nil, "discover_tools,read_file"},
	} {
		registry.SetDiscoveredTools(tc.names...)
		definitions := registry.ToOpenAIFunctionsGoverned(nil, "", "", nil, 0)
		if got := strings.Join(functionNames(definitions), ","); got != tc.want {
			t.Fatalf("working set=%s want=%s", got, tc.want)
		}
	}
}

func TestSetDiscoveredTools_DisabledDiscoveryPreservesCatalog(t *testing.T) {
	registry := NewRegistry()
	before := registry.ToOpenAIFunctionsGoverned(nil, "", "", nil, 0)
	registry.SetDiscoveredTools("git_log")
	after := registry.ToOpenAIFunctionsGoverned(nil, "", "", nil, 0)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("working-set refresh changed an explicit catalog without discovery")
	}
}
