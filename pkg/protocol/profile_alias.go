package protocol

import (
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
)

// Profile facts live in a dependency-free domain package so both the protocol
// compiler and the SQLite adapter can depend inward without an import cycle.
// These aliases retain the protocol-facing API for existing callers.
const (
	ProfileSchemaVersion  = modelprofile.SchemaVersion
	ProtocolSchemaVersion = "buckley.protocol/v1"
)

type (
	ModelClass         = modelprofile.Class
	Capabilities       = modelprofile.Capabilities
	BehaviorMetrics    = modelprofile.Metrics
	BehaviorProfile    = modelprofile.Profile
	Observation        = modelprofile.Observation
	ProfileStore       = modelprofile.Store
	MemoryProfileStore = modelprofile.MemoryStore
)

const (
	ClassWeak     = modelprofile.ClassWeak
	ClassBalanced = modelprofile.ClassBalanced
	ClassFrontier = modelprofile.ClassFrontier
)

func NewMemoryProfileStore() *MemoryProfileStore { return modelprofile.NewMemoryStore() }

func Aggregate(base BehaviorProfile, observations []Observation, measuredAt time.Time) (BehaviorProfile, error) {
	return modelprofile.Aggregate(base, observations, measuredAt)
}
