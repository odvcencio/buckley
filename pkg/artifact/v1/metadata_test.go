package artifactv1

import (
	"reflect"
	"testing"
)

func TestArtifactNormalizationPreservesMetadata(t *testing.T) {
	metadata := map[string]string{" subagent_run_id ": " child-one ", "provider": "particle"}
	a := Artifact{SchemaVersion: SchemaVersion, Kind: KindSubagentResult, Status: StatusCompleted, Title: "result", Summary: "source evidence", Metadata: metadata}
	got, err := NormalizeAndValidate(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Metadata, map[string]string{"subagent_run_id": "child-one", "provider": "particle"}) {
		t.Fatalf("metadata lost: %#v", got.Metadata)
	}
	if !reflect.DeepEqual(metadata, map[string]string{" subagent_run_id ": " child-one ", "provider": "particle"}) {
		t.Fatal("normalization changed caller metadata")
	}
	got.Metadata["provider"] = "changed"
	if metadata["provider"] != "particle" {
		t.Fatal("normalization retained a mutable alias")
	}
	metadata["provider"] = "caller changed"
	if got.Metadata["provider"] != "changed" {
		t.Fatal("caller mutation changed normalized artifact")
	}

	a.Metadata = map[string]string{"subagent_run_id": "child-one"}
	first := a.Normalized()
	a.Metadata = map[string]string{"subagent_run_id": "child-two"}
	second := a.Normalized()
	if first.ArtifactID == second.ArtifactID {
		t.Fatal("distinct metadata collapsed to the same content identity")
	}
	if repeated := first.Normalized(); repeated.ArtifactID != first.ArtifactID || !reflect.DeepEqual(repeated.Metadata, first.Metadata) {
		t.Fatal("normalization is not idempotent")
	}
	for _, empty := range []map[string]string{nil, {}} {
		a.Metadata = empty
		if got := a.Normalized(); !reflect.DeepEqual(got.Metadata, empty) {
			t.Fatalf("nil/empty distinction changed: %#v", got.Metadata)
		}
	}
}
