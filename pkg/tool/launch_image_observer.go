package tool

import (
	"m31labs.dev/buckley/pkg/launchimage"
)

// LaunchImageObserver is retained as a source-compatible alias. The
// cycle-free launchimage leaf owns verification and proof construction.
type LaunchImageObserver = launchimage.Verifier
