package artifactv1

import (
	"encoding/json"
)

// RenderJSON returns indented, deterministic JSON after verifying the typed
// artifact contract.
func RenderJSON(artifact Artifact) ([]byte, error) {
	if err := artifact.ValidateStrict(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(artifact, "", "  ")
}
