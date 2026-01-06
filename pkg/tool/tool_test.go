package tool

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"go.uber.org/mock/gomock"
)

func TestToOpenAIFunction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tool := NewMockTool(ctrl)
	tool.EXPECT().Name().Return("coverage_check")
	tool.EXPECT().Description().Return("Report test coverage")
	tool.EXPECT().Parameters().Return(builtin.ParameterSchema{
		Type: "object",
	})

	fn := ToOpenAIFunction(tool)
	function, ok := fn["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function map in response")
	}
	if function["name"] != "coverage_check" {
		t.Fatalf("expected function name coverage_check, got %v", function["name"])
	}
	if function["description"] != "Report test coverage" {
		t.Fatalf("unexpected description: %v", function["description"])
	}
}

func TestJSONRoundTrip(t *testing.T) {
	SetResultEncoding(false)
	t.Cleanup(func() { SetResultEncoding(true) })
	result := &builtin.Result{Success: true, Data: map[string]any{"files": 2}}
	jsonStr, err := ToJSON(result)
	if err != nil {
		t.Fatalf("ToJSON returned err: %v", err)
	}
	parsed, err := FromJSON(jsonStr)
	if err != nil {
		t.Fatalf("FromJSON returned err: %v", err)
	}
	if parsed.Success != result.Success {
		t.Fatalf("parsed result mismatch: %+v", parsed)
	}
	if parsed.Data["files"].(float64) != 2 {
		t.Fatalf("metadata not preserved: %+v", parsed.Data)
	}
}

func TestToJSONUsesToonByDefault(t *testing.T) {
	SetResultEncoding(true)
	jsonStr, err := ToJSON(&builtin.Result{Success: true})
	if err != nil {
		t.Fatalf("ToJSON returned err: %v", err)
	}
	if strings.HasPrefix(jsonStr, "{") {
		t.Fatalf("expected TOON payload, got %s", jsonStr)
	}
}
