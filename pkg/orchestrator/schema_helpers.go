package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/odvcencio/buckley/pkg/encoding/toon"
)

// renderSchema encodes structured examples using TOON when enabled, falling
// back to pretty JSON when TOON is unavailable or disabled.
func renderSchema(spec any, useToon bool) string {
	if spec == nil {
		return ""
	}

	if useToon {
		if data, err := toon.New(true).Marshal(spec); err == nil {
			return string(data)
		}
	}

	if data, err := json.MarshalIndent(spec, "", "  "); err == nil {
		return string(data)
	}

	return fmt.Sprintf("%v", spec)
}
