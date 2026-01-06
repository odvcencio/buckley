package containerexec

import "testing"

func TestParseStatus(t *testing.T) {
	input := `{"Name":"dev-go","Service":"dev-go","State":"running","Health":"healthy"}
{"Name":"dev-node","Service":"dev-node","State":"running","Health":""}`
	statuses, err := parseStatus([]byte(input))
	if err != nil {
		t.Fatalf("parseStatus error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Service != "dev-go" || statuses[0].Health != "healthy" {
		t.Fatalf("unexpected first status: %+v", statuses[0])
	}
}
