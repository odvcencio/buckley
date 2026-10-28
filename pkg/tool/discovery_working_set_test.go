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

func TestDiscoveryTool_NoMatchesPreservesWorkingSet(t *testing.T) {
	registry := NewRegistry()
	registry.EnableDynamicDiscovery([]string{"read_file"})
	discovery, ok := registry.Get("discover_tools")
	if !ok {
		t.Fatal("discover_tools was not registered")
	}
	for _, tc := range []struct {
		name    string
		search  string
		success bool
		want    string
	}{
		{"initial miss", "no_such_tool_91827", false, "discover_tools,read_file"},
		{"first match", "git_log", true, "discover_tools,git_log,read_file"},
		{"subsequent miss", "no_such_tool_91827", false, "discover_tools,git_log,read_file"},
		{"replacement match", "git_blame", true, "discover_tools,git_blame,read_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := discovery.Execute(map[string]any{"names": []string{tc.search}})
			if err != nil || result == nil || result.Success != tc.success {
				t.Fatalf("discovery result=%+v err=%v, want success=%v", result, err, tc.success)
			}
			definitions := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
			if got := strings.Join(functionNames(definitions), ","); got != tc.want {
				t.Fatalf("working set=%s want=%s", got, tc.want)
			}
		})
	}
}
